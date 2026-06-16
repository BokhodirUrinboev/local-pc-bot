package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	claudeEditInterval = 1500 * time.Millisecond
	claudeMaxWait      = 30 * time.Minute
	// Yangi prompt avvalgisi tugashini shu vaqtgacha kutadi. Cap > claudeMaxWait —
	// shunda timeout'ga uchragan avvalgi ish bekor bo'lgach, navbatdagisi ham yetadi.
	claudeQueueWait = claudeMaxWait + time.Minute
)

// lookupClaudeBinary platformga mos nomlarni sinab ko'radi (platform_*.go).
func lookupClaudeBinary() (string, error) {
	for _, name := range claudeBinaryCandidates() {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("claude not found in PATH")
}

// ensureClaudeBinary lazy probe qiladi.
func (s *Session) ensureClaudeBinary() (string, error) {
	s.mu.Lock()
	bin := s.claudeBinary
	s.mu.Unlock()
	if bin != "" {
		return bin, nil
	}
	bin, err := lookupClaudeBinary()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.claudeBinary = bin
	s.mu.Unlock()
	return bin, nil
}

// RunClaude Claude agentni stream-json rejimida ishga tushiradi.
// `--dangerously-skip-permissions` orqali to'liq tool/MCP access beriladi —
// workspace'dagi `.mcp.json` va `.claude/settings.json` avto-pickup qilinadi.
// threadID — forum topic ID (0 bo'lsa General/oddiy chat).
func (s *Session) RunClaude(prompt string, threadID int) {
	// Bu prompt qaysi "navbat avlodi"ga tegishli ekanini eslab qolamiz.
	// /stop bosilsa, shu avlod cancel qilinadi va biz darrov bail qilamiz —
	// foydalanuvchining hayoliga zid ravishda avtomatik ishga tushib ketmaymiz.
	gen := s.CurrentQueueGen()

	// Slot acquire: avvaliga darrov urinamiz. Band bo'lsa — foydalanuvchiga
	// "navbatdaman" deb status yuboramiz va kutamiz. Cap claudeQueueWait —
	// shunda hech qachon abadiy ilinmaymiz, lekin uzoq legit ishlarga joy bor.
	var queueMsgID int
	select {
	case s.cmdSlot <- struct{}{}:
		// darrov olindi
	case <-gen.ctx.Done():
		// /stop biz harakat qilmasdan oldin tushdi (juda kam ehtimol) — jim bail.
		return
	default:
		sent, err := SendInThread(s.bot, s.ChatID, threadID,
			"📥 <i>Avvalgi promtim hali ishlayapti — navbatda turibman.</i>\n<i>Tezroq kerak bo'lsa: <code>/stop</code></i>",
			tgbotapi.ModeHTML, nil)
		if err == nil && sent.MessageID != 0 {
			queueMsgID = sent.MessageID
		}
		select {
		case s.cmdSlot <- struct{}{}:
			// kutib oldik
		case <-gen.ctx.Done():
			// /stop navbat'ni bekor qildi — bizning prompt'ga ham endi keraksiz.
			if queueMsgID != 0 {
				_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(s.ChatID, queueMsgID))
			}
			return
		case <-time.After(claudeQueueWait):
			if queueMsgID != 0 {
				edit := tgbotapi.NewEditMessageText(s.ChatID, queueMsgID,
					"⌛ Navbat vaqti tugadi — avvalgi promtim hali tirik. <code>/stop</code> bilan to'xtatib qayta yuboring.")
				edit.ParseMode = tgbotapi.ModeHTML
				_, _ = s.bot.Send(edit)
			}
			return
		}
	}
	defer func() { <-s.cmdSlot }()

	// Slot'ni olib bo'lganimizdan keyin ham tekshiramiz: navbatda kutayotganda
	// /stop bo'lib o'tgan bo'lishi mumkin (oraliq race).
	if gen.ctx.Err() != nil {
		if queueMsgID != 0 {
			_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(s.ChatID, queueMsgID))
		}
		return
	}

	// "Navbatdaman" status xabarini olib tashlaymiz — endi haqiqiy ish boshlanyapti.
	if queueMsgID != 0 {
		_, _ = s.bot.Send(tgbotapi.NewDeleteMessage(s.ChatID, queueMsgID))
	}

	binary, err := s.ensureClaudeBinary()
	if err != nil {
		text := "⚠️ <code>claude</code> CLI PATH'da topilmadi. " +
			"Bu kompyuterga Claude Code o'rnatilganmi?\n<code>where.exe claude</code>"
		_, _ = SendInThread(s.bot, s.ChatID, threadID, text, tgbotapi.ModeHTML, nil)
		return
	}

	s.mu.Lock()
	sessionID := s.claudeSessionID
	workdir := s.workdir
	systemPrompt := s.systemPrompt
	s.mu.Unlock()

	sent, err := SendInThread(s.bot, s.ChatID, threadID, "🤖 <i>Najim ishlayapti…</i>", tgbotapi.ModeHTML, nil)
	if err != nil {
		log.Printf("claude placeholder (chat=%d, thread=%d): %v", s.ChatID, threadID, err)
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

	// `-p` argumentsiz — claude promptni stdin'dan oladi.
	claudeArgs := []string{
		"-p",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--dangerously-skip-permissions",
	}
	if systemPrompt != "" {
		claudeArgs = append(claudeArgs, "--append-system-prompt", systemPrompt)
	}
	// Agar workdir ichida .mcp.json bo'lsa, aniq pass qilamiz.
	if mcpPath := filepath.Join(workdir, ".mcp.json"); fileExists(mcpPath) {
		claudeArgs = append(claudeArgs, "--mcp-config", mcpPath)
	}
	if sessionID != "" {
		claudeArgs = append(claudeArgs, "--resume", sessionID)
	}

	// Platformga mos komanda quramiz (platform_*.go): Windows'da claude PowerShell
	// orqali wrap qilinadi (OAuth/keychain handle + 32K cheklov + UTF-8), Unix'da
	// to'g'ridan-to'g'ri exec qilinadi. Har ikkalasida prompt stdin orqali beriladi.
	cmd, cleanup, err := buildClaudeCmd(ctx, binary, prompt, claudeArgs)
	if err != nil {
		s.editClaudeText(sent.MessageID, "⚠️ Komanda tayyorlash xato: "+err.Error(), true)
		return
	}
	defer cleanup()
	cmd.Dir = workdir
	// Context cancel default holatda faqat to'g'ridan-to'g'ri bolani o'ldiradi,
	// lekin uning farzand `claude.exe`/node MCP'larini qoldiradi — orphan bo'lib
	// token sarflaydi. killTree butun daraxtni o'ldiradi (Windows: taskkill /T,
	// Unix: process-group SIGKILL).
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		killTree(cmd.Process.Pid)
		return nil
	}
	// Cancel'dan keyin Wait'ni cheksiz kutmaymiz — kill biror sababga ko'ra
	// ishlamasa, 5 sek dan keyin Go o'zi forceful kill qiladi.
	cmd.WaitDelay = 5 * time.Second
	setProcessGroup(cmd)

	// stdout'ni io.Pipe orqali olamiz (cmd.StdoutPipe() EMAS): cmd.Wait()'ni
	// alohida goroutine'da chaqirib, process o'lgach pw'ni yopamiz. Shunda quyidagi
	// scanner EOF oladi — hatto claude.exe nevaralari (MCP npx/node) stdout handle'ni
	// ushlab tursa ham. StdoutPipe'ni main goroutine'da to'g'ridan-to'g'ri o'qish
	// /stop'da DEADLOCK berardi: nevara handle'ni ushlasa scanner bloklanardi,
	// cmd.Wait() (va uning WaitDelay kafolati) esa hech qachon ishga tushmasdi.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		s.editClaudeText(sent.MessageID, "⚠️ Start xato: "+err.Error(), true)
		return
	}

	// PID'ni SendInterrupt to'g'ridan-to'g'ri taskkill qilishi uchun saqlaymiz —
	// context cancel path biror sababga ko'ra ilinsa ham, /stop kafolatlangan o'ldirish bo'ladi.
	s.mu.Lock()
	s.claudePID = cmd.Process.Pid
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.claudePID = 0
		s.mu.Unlock()
	}()

	// cmd.Wait()'ni alohida goroutine'da: process o'lgach (WaitDelay bilan 5s kafolat)
	// pw yopiladi → pastdagi scanner EOF oladi va slot bo'shaydi.
	waitErrCh := make(chan error, 1)
	go func() {
		waitErrCh <- cmd.Wait()
		_ = pw.Close()
	}()

	var (
		text       strings.Builder
		lastEdit   time.Time
		lastSent   string
		capturedID string
		gotErr     string
	)

	scanner := bufio.NewScanner(pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		ev := parseClaudeEvent(line)
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" && ev.SessionID != "" {
				capturedID = ev.SessionID
			}
		case "stream_event":
			if ev.Delta != "" {
				text.WriteString(ev.Delta)
				if time.Since(lastEdit) >= claudeEditInterval {
					if cur := text.String(); cur != lastSent {
						s.editClaudeText(sent.MessageID, cur, false)
						lastSent = cur
						lastEdit = time.Now()
					}
				}
			}
		case "assistant":
			if text.Len() == 0 && ev.AssistantText != "" {
				text.WriteString(ev.AssistantText)
				s.editClaudeText(sent.MessageID, text.String(), false)
				lastSent = text.String()
				lastEdit = time.Now()
			}
		case "result":
			if ev.IsError && ev.Result != "" {
				gotErr = ev.Result
			} else if ev.Result != "" && text.Len() == 0 {
				text.WriteString(ev.Result)
			}
			if ev.SessionID != "" {
				capturedID = ev.SessionID
			}
		}
	}
	if err := scanner.Err(); err != nil {
		log.Printf("claude scanner (chat=%d): %v", s.ChatID, err)
	}

	waitErr := <-waitErrCh

	if capturedID != "" {
		s.mu.Lock()
		s.claudeSessionID = capturedID
		s.mu.Unlock()
	}

	final := strings.TrimSpace(text.String())
	switch {
	case ctx.Err() == context.Canceled:
		s.editClaudeText(sent.MessageID, "⏹ Foydalanuvchi to'xtatdi.\n\n"+final, true)
	case ctx.Err() == context.DeadlineExceeded:
		s.editClaudeText(sent.MessageID, "⌛ Vaqt tugadi.\n\n"+final, true)
	case gotErr != "":
		s.editClaudeText(sent.MessageID, "⚠️ Claude xato: "+gotErr, true)
	case final == "" && waitErr != nil:
		s.editClaudeText(sent.MessageID, "⚠️ Claude ishlamadi: "+waitErr.Error(), true)
	case final == "":
		s.editClaudeText(sent.MessageID, "<i>(bo'sh javob)</i>", true)
	default:
		s.editClaudeText(sent.MessageID, final, true)
	}
}

type claudeEvent struct {
	Type          string
	Subtype       string
	SessionID     string
	Delta         string
	AssistantText string
	Result        string
	IsError       bool
}

func parseClaudeEvent(line []byte) claudeEvent {
	trim := skipLeadingSpace(line)
	if len(trim) == 0 || trim[0] != '{' {
		return claudeEvent{}
	}

	var raw struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		SessionID string          `json:"session_id"`
		Result    string          `json:"result"`
		IsError   bool            `json:"is_error"`
		Event     json.RawMessage `json:"event"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(trim, &raw); err != nil {
		return claudeEvent{}
	}

	ev := claudeEvent{
		Type:      raw.Type,
		Subtype:   raw.Subtype,
		SessionID: raw.SessionID,
		Result:    raw.Result,
		IsError:   raw.IsError,
	}

	if raw.Type == "stream_event" && len(raw.Event) > 0 {
		var e struct {
			Type  string `json:"type"`
			Delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(raw.Event, &e); err == nil {
			if e.Type == "content_block_delta" && e.Delta.Type == "text_delta" {
				ev.Delta = e.Delta.Text
			}
		}
	}

	if raw.Type == "assistant" && len(raw.Message) > 0 {
		var m struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw.Message, &m); err == nil {
			var b strings.Builder
			for _, c := range m.Content {
				if c.Type == "text" {
					b.WriteString(c.Text)
				}
			}
			ev.AssistantText = b.String()
		}
	}

	return ev
}

func skipLeadingSpace(b []byte) []byte {
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return b[i:]
		}
	}
	return nil
}

// editClaudeText placeholder xabarni Claude javobi bilan yangilaydi.
func (s *Session) editClaudeText(msgID int, body string, final bool) {
	body = stripANSI(body)
	body = strings.TrimRight(body, "\n")
	if r := []rune(body); len(r) > maxMsgChars {
		body = "…\n" + string(r[len(r)-maxMsgChars:])
	}
	icon := "🤖 ✍️"
	if final {
		icon = "🤖"
	}
	text := fmt.Sprintf("%s\n<pre>%s</pre>", icon, htmlEscape(body))

	edit := tgbotapi.NewEditMessageText(s.ChatID, msgID, text)
	edit.ParseMode = tgbotapi.ModeHTML
	if _, err := s.bot.Send(edit); err != nil {
		if !strings.Contains(err.Error(), "not modified") {
			log.Printf("claude edit (chat=%d): %v", s.ChatID, err)
		}
	}
}
