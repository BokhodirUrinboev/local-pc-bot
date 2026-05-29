package bot

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// psEditInterval — PowerShell stream uchun message edit ticker.
const psEditInterval = 1500 * time.Millisecond

// RunPowerShell foydalanuvchi yuborgan matnni shu kompyuterda PowerShell komandasi
// sifatida ishga tushiradi va stdout+stderr'ni Telegram xabariga edit-throttle bilan
// strim qiladi. Sessiya `cmdSlot` va `queueGen` navbati Claude bilan birga ishlaydi —
// shu chatda bir vaqtda faqat bitta exec bo'ladi.
func (s *Session) RunPowerShell(command string, threadID int) {
	gen := s.CurrentQueueGen()

	var queueMsgID int
	select {
	case s.cmdSlot <- struct{}{}:
	case <-gen.ctx.Done():
		return
	default:
		sent, err := SendInThread(s.bot, s.ChatID, threadID,
			"📥 <i>Avvalgi komanda hali ishlayapti — navbatda turibman.</i>\n<i>Tezroq kerak bo'lsa: <code>/stop</code></i>",
			tgbotapi.ModeHTML, nil)
		if err == nil && sent.MessageID != 0 {
			queueMsgID = sent.MessageID
		}
		select {
		case s.cmdSlot <- struct{}{}:
		case <-gen.ctx.Done():
			if queueMsgID != 0 {
				_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(s.ChatID, queueMsgID))
			}
			return
		case <-time.After(claudeQueueWait):
			if queueMsgID != 0 {
				edit := tgbotapi.NewEditMessageText(s.ChatID, queueMsgID,
					"⌛ Navbat vaqti tugadi — avvalgi komanda hali tirik. <code>/stop</code> bilan to'xtatib qayta yuboring.")
				edit.ParseMode = tgbotapi.ModeHTML
				_, _ = s.bot.Send(edit)
			}
			return
		}
	}
	defer func() { <-s.cmdSlot }()

	if gen.ctx.Err() != nil {
		if queueMsgID != 0 {
			_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(s.ChatID, queueMsgID))
		}
		return
	}
	if queueMsgID != 0 {
		_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(s.ChatID, queueMsgID))
	}

	s.mu.Lock()
	workdir := s.workdir
	s.mu.Unlock()

	sent, err := SendInThread(s.bot, s.ChatID, threadID, "🟦 <i>PowerShell ishlayapti…</i>", tgbotapi.ModeHTML, nil)
	if err != nil {
		log.Printf("ps placeholder (chat=%d, thread=%d): %v", s.ChatID, threadID, err)
		return
	}

	// Parent — gen.ctx: /stop navbat avlodini cancel qilganda bu exec ham
	// avtomatik o'ladi (cmd.Cancel → taskkill), claudeCancel set bo'lishini
	// kutmasdan. Aks holda slot-acquire bilan claudeCancel-set orasidagi
	// oraliqda /stop bosilsa, jarayon orphan bo'lib ishlab ketardi.
	ctx, cancel := context.WithTimeout(gen.ctx, claudeMaxWait)
	defer cancel()

	s.mu.Lock()
	s.claudeCancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.claudeCancel != nil {
			s.claudeCancel = nil
		}
		s.mu.Unlock()
	}()

	// Uzun komandalar uchun foydalanuvchi matnini temp UTF-8 fayldan o'qiymiz —
	// Windows CreateProcess (~32K) command line cheklovidan oshib ketmasligi va
	// Cyrillic/emoji aniq pass bo'lishi uchun.
	tmpFile, err := os.CreateTemp("", "remofy-pscmd-*.ps1")
	if err != nil {
		s.editPSText(sent.MessageID, "⚠️ Temp fayl xato: "+err.Error(), true)
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)
	if _, err := tmpFile.WriteString(command); err != nil {
		tmpFile.Close()
		s.editPSText(sent.MessageID, "⚠️ Temp fayl yozish xato: "+err.Error(), true)
		return
	}
	if err := tmpFile.Close(); err != nil {
		s.editPSText(sent.MessageID, "⚠️ Temp fayl yopish xato: "+err.Error(), true)
		return
	}

	// Encoding'ni UTF-8 ga majburlab, foydalanuvchi komandasini script-block sifatida
	// Invoke-Expression orqali bajaramiz. ScriptBlock — sintaksis foydalanuvchi yozgani
	// shaklda saqlanishi uchun.
	psCmd := "$OutputEncoding=[Console]::InputEncoding=[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new(); " +
		"Invoke-Expression ([System.IO.File]::ReadAllText(" + psQuote(tmpPath) + ",[System.Text.Encoding]::UTF8))"

	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NoLogo", "-NonInteractive", "-Command", psCmd)
	cmd.Dir = workdir
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return exec.Command("taskkill.exe", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	}
	cmd.WaitDelay = 5 * time.Second

	// stdout + stderr ni bitta pipe'ga birlashtiramiz — terminal ko'rinishi.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		s.editPSText(sent.MessageID, "⚠️ Start xato: "+err.Error(), true)
		return
	}

	s.mu.Lock()
	s.claudePID = cmd.Process.Pid
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.claudePID = 0
		s.mu.Unlock()
	}()

	waitErrCh := make(chan error, 1)
	go func() {
		waitErrCh <- cmd.Wait()
		_ = pw.Close()
	}()

	var (
		text     strings.Builder
		lastEdit time.Time
		lastSent string
	)

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		text.Write(scanner.Bytes())
		text.WriteByte('\n')
		if time.Since(lastEdit) >= psEditInterval {
			if cur := text.String(); cur != lastSent {
				s.editPSText(sent.MessageID, cur, false)
				lastSent = cur
				lastEdit = time.Now()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("ps scanner (chat=%d): %v", s.ChatID, err)
	}

	waitErr := <-waitErrCh

	final := strings.TrimSpace(text.String())
	switch {
	case ctx.Err() == context.Canceled:
		s.editPSText(sent.MessageID, "⏹ Foydalanuvchi to'xtatdi.\n\n"+final, true)
	case ctx.Err() == context.DeadlineExceeded:
		s.editPSText(sent.MessageID, "⌛ Vaqt tugadi.\n\n"+final, true)
	case final == "" && waitErr != nil:
		s.editPSText(sent.MessageID, "⚠️ PS xato: "+waitErr.Error(), true)
	case final == "":
		s.editPSText(sent.MessageID, "<i>(bo'sh chiqish)</i>", true)
	default:
		body := final
		if waitErr != nil {
			body = final + "\n\n⚠️ exit: " + waitErr.Error()
		}
		s.editPSText(sent.MessageID, body, true)
	}
}

// editPSText placeholder xabarni PS chiqishi bilan yangilaydi.
func (s *Session) editPSText(msgID int, body string, final bool) {
	body = stripANSI(body)
	body = strings.TrimRight(body, "\n")
	if r := []rune(body); len(r) > maxMsgChars {
		body = "…\n" + string(r[len(r)-maxMsgChars:])
	}
	icon := "🟦 ✍️"
	if final {
		icon = "🟦"
	}
	text := fmt.Sprintf("%s\n<pre>%s</pre>", icon, htmlEscape(body))

	edit := tgbotapi.NewEditMessageText(s.ChatID, msgID, text)
	edit.ParseMode = tgbotapi.ModeHTML
	if _, err := s.bot.Send(edit); err != nil {
		if !strings.Contains(err.Error(), "not modified") {
			log.Printf("ps edit (chat=%d): %v", s.ChatID, err)
		}
	}
}
