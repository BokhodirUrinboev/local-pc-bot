package bot

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// shellEditInterval — shell stream uchun message edit ticker.
const shellEditInterval = 1500 * time.Millisecond

// RunShell foydalanuvchi yuborgan matnni shu kompyuterda platformga mos shell
// (Windows: PowerShell, Unix: bash) komandasi sifatida ishga tushiradi va
// stdout+stderr'ni Telegram xabariga edit-throttle bilan strim qiladi. Sessiya
// `cmdSlot` va `queueGen` navbati Claude bilan birga ishlaydi — shu chatda bir
// vaqtda faqat bitta exec bo'ladi.
func (s *Session) RunShell(command string, threadID int) {
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

	startDir := s.Cwd()

	sent, err := SendInThread(s.bot, s.ChatID, threadID, "🟦 <i>"+shellLabel()+" ishlayapti…</i>", tgbotapi.ModeHTML, nil)
	if err != nil {
		log.Printf("shell placeholder (chat=%d, thread=%d): %v", s.ChatID, threadID, err)
		return
	}

	// Parent — gen.ctx: /stop navbat avlodini cancel qilganda bu exec ham
	// avtomatik o'ladi (cmd.Cancel → killTree), claudeCancel set bo'lishini
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

	// Komanda tugagach yangi ish papkasini (Get-Location / $PWD) shu faylga yozamiz,
	// keyin o'qib sessiya cwd'sini yangilaymiz — `cd` keyingi komandaga saqlanadi.
	cwdFile, err := os.CreateTemp("", "remofy-cwd-*.txt")
	if err != nil {
		s.editShellText(sent.MessageID, "⚠️ Temp fayl xato: "+err.Error(), true)
		return
	}
	cwdPath := cwdFile.Name()
	cwdFile.Close()
	defer os.Remove(cwdPath)

	// Platformga mos komanda quramiz (platform_*.go): Windows'da PowerShell
	// Invoke-Expression + temp .ps1, Unix'da bash stdin orqali.
	cmd, cleanup, err := buildShellCmd(ctx, command, cwdPath)
	if err != nil {
		s.editShellText(sent.MessageID, "⚠️ Komanda tayyorlash xato: "+err.Error(), true)
		return
	}
	defer cleanup()
	cmd.Dir = startDir
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		killTree(cmd.Process.Pid)
		return nil
	}
	cmd.WaitDelay = 5 * time.Second
	setProcessGroup(cmd)

	// stdout + stderr ni bitta pipe'ga birlashtiramiz — terminal ko'rinishi.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		s.editShellText(sent.MessageID, "⚠️ Start xato: "+err.Error(), true)
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
		if time.Since(lastEdit) >= shellEditInterval {
			if cur := text.String(); cur != lastSent {
				s.editShellText(sent.MessageID, cur, false)
				lastSent = cur
				lastEdit = time.Now()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("shell scanner (chat=%d): %v", s.ChatID, err)
	}

	waitErr := <-waitErrCh

	// Yangi ish papkasini o'qib, mavjud bo'lsa sessiya cwd'sini yangilaymiz.
	if data, e := os.ReadFile(cwdPath); e == nil {
		if nd := strings.TrimSpace(string(data)); nd != "" {
			if st, e2 := os.Stat(nd); e2 == nil && st.IsDir() {
				s.SetCwd(nd)
			}
		}
	}

	final := strings.TrimSpace(text.String())
	switch {
	case ctx.Err() == context.Canceled:
		s.editShellText(sent.MessageID, "⏹ Foydalanuvchi to'xtatdi.\n\n"+final, true)
	case ctx.Err() == context.DeadlineExceeded:
		s.editShellText(sent.MessageID, "⌛ Vaqt tugadi.\n\n"+final, true)
	case final == "" && waitErr != nil:
		s.editShellText(sent.MessageID, "⚠️ "+shellLabel()+" xato: "+waitErr.Error(), true)
	case final == "":
		s.editShellText(sent.MessageID, "<i>(bo'sh chiqish)</i>", true)
	default:
		body := final
		if waitErr != nil {
			body = final + "\n\n⚠️ exit: " + waitErr.Error()
		}
		s.editShellText(sent.MessageID, body, true)
	}
}

// editShellText placeholder xabarni shell chiqishi bilan yangilaydi.
func (s *Session) editShellText(msgID int, body string, final bool) {
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
			log.Printf("shell edit (chat=%d): %v", s.ChatID, err)
		}
	}
}
