package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	claudeEditInterval = 1500 * time.Millisecond
	claudeMaxWait      = 5 * time.Minute
)

// IsClaudeMode aktiv Claude rejimini tekshiradi.
func (s *Session) IsClaudeMode() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claudeMode
}

// EnterClaudeMode rejimga kiradi. Aktiv live shortcut bo'lsa to'xtatiladi.
// PTY shell orqali `claude` binar yo'lini va PATH'ni probe qilib oladi —
// alohida ssh exec sessiya non-interactive bo'lgani uchun .bashrc/.profile'dagi
// PATH avtomatik yuklanmaydi va `claude` topilmaydi.
func (s *Session) EnterClaudeMode() {
	s.StopLive()

	s.mu.Lock()
	already := s.claudeMode
	s.mu.Unlock()
	if already {
		return
	}

	binPath, pathEnv, err := s.probeClaudeEnv()
	if err != nil {
		text := "⚠️ Claude rejimini yoqib bo'lmadi: " + htmlEscape(err.Error()) + "\n\n" +
			"Serverda <code>claude</code> CLI o'rnatilganligini tekshiring (<code>which claude</code>)."
		msg := tgbotapi.NewMessage(s.ChatID, text)
		msg.ParseMode = tgbotapi.ModeHTML
		_, _ = s.bot.Send(msg)
		return
	}

	s.mu.Lock()
	s.claudeMode = true
	s.claudeBinary = binPath
	s.claudePath = pathEnv
	s.mu.Unlock()

	text := "🤖 <b>Claude rejimi yoqildi</b>\n\n" +
		"Yozgan har bir xabaringiz <code>claude -p</code> orqali yuboriladi.\n" +
		"Javob real vaqtda strim qilib bitta xabarda yangilanadi.\n\n" +
		"<code>" + htmlEscape(binPath) + "</code>\n\n" +
		"<i>Ctrl+C</i> — aktiv promptni uzish\n" +
		"/endclaude — rejimdan chiqish"
	msg := tgbotapi.NewMessage(s.ChatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	_, _ = s.bot.Send(msg)
}

// probeClaudeEnv mavjud PTY shell orqali `claude` absolyut yo'lini va
// $PATH'ni topadi. Toza chiqish uchun `printf` BIN:/PATH: prefikslari ishlatiladi.
// runOnceLocked cmdMu'ni o'zi oladi, shuning uchun bu funksiya cmdMu ushlamasligi kerak.
func (s *Session) probeClaudeEnv() (string, string, error) {
	cmd := `printf 'BIN:%s\n' "$(command -v claude 2>/dev/null)"; printf 'PATH:%s\n' "$PATH"`

	out, ok := s.runOnceLocked(cmd)
	if !ok {
		return "", "", fmt.Errorf("sessiya yopilgan")
	}
	text := stripANSI(string(out))

	var binPath, pathEnv string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if v, found := strings.CutPrefix(line, "BIN:"); found {
			if v != "" && binPath == "" {
				binPath = v
			}
		} else if v, found := strings.CutPrefix(line, "PATH:"); found {
			if v != "" && pathEnv == "" {
				pathEnv = v
			}
		}
	}
	if binPath == "" {
		return "", "", fmt.Errorf("`claude` PATH'da topilmadi")
	}
	return binPath, pathEnv, nil
}

// ExitClaudeMode rejimdan chiqadi va sessiya idsini tozalaydi (keyingi /claude
// yangi suhbatdan boshlanadi).
func (s *Session) ExitClaudeMode() {
	s.mu.Lock()
	was := s.claudeMode
	s.claudeMode = false
	s.claudeSessionID = ""
	s.claudeBinary = ""
	s.claudePath = ""
	cancel := s.claudeCancel
	s.claudeCancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if was {
		_, _ = s.bot.Send(tgbotapi.NewMessage(s.ChatID, "🤖 Claude rejimi o'chirildi."))
	}
}

// shellSingleQuote argumentni bash uchun xavfsiz single-quote ichiga oladi.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RunClaude bitta promptni claude -p orqali strim qilib ishlatadi.
// stream-json chiqishini chiziq-chiziq parse qilib, Telegram xabarini throttle
// bilan tahrirlab boradi.
func (s *Session) RunClaude(prompt string) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()

	if !s.markActive() {
		return
	}

	s.mu.Lock()
	sessionID := s.claudeSessionID
	binary := s.claudeBinary
	pathEnv := s.claudePath
	s.mu.Unlock()

	if binary == "" {
		// Holat: kimdir EnterClaudeMode'siz to'g'ridan to'g'ri RunClaude chaqirgan.
		s.replyError("Claude binari probe qilinmagan. /claude komandasini qayta yuboring.")
		return
	}

	cmd := buildClaudeCmd(binary, pathEnv, prompt, sessionID)

	placeholder := tgbotapi.NewMessage(s.ChatID, "🤖 <i>Claude o'ylayapti…</i>")
	placeholder.ParseMode = tgbotapi.ModeHTML
	sent, err := s.bot.Send(placeholder)
	if err != nil {
		log.Printf("claude placeholder send: %v", err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), claudeMaxWait)
	s.mu.Lock()
	s.claudeCancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		// Faqat o'zimizniki bo'lsa tozalaymiz
		if s.claudeCancel != nil {
			s.claudeCancel = nil
		}
		s.mu.Unlock()
		cancel()
	}()

	reader, wait, err := s.Conn.Exec(ctx, cmd)
	if err != nil {
		s.editClaudeText(sent.MessageID, "⚠️ Exec xato: "+err.Error(), true)
		return
	}

	var (
		text       strings.Builder
		lastEdit   time.Time
		lastSent   string
		capturedID string
		gotErr     string
	)

	scanner := bufio.NewScanner(reader)
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
			// Agar partial deltalar kelmagan bo'lsa, to'liq matnni olamiz
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
		log.Printf("claude scanner (tg=%d): %v", s.TelegramID, err)
	}

	waitErr := wait()

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
		s.editClaudeText(sent.MessageID, "⌛ Vaqt tugadi (5 daq).\n\n"+final, true)
	case gotErr != "":
		s.editClaudeText(sent.MessageID, "⚠️ Claude xato: "+gotErr, true)
	case final == "" && waitErr != nil:
		s.editClaudeText(sent.MessageID, "⚠️ Claude ishlatilmadi: "+waitErr.Error()+"\n\nServerda <code>claude</code> CLI o'rnatilganmi?", true)
	case final == "":
		s.editClaudeText(sent.MessageID, "<i>(bo'sh javob)</i>", true)
	default:
		s.editClaudeText(sent.MessageID, final, true)
	}
}

// buildClaudeCmd absolyut binar yo'li va probed PATH bilan komanda quradi.
// PATH explicit beriladi — claude shebang orqali node'ni env'dan topadi.
func buildClaudeCmd(binary, pathEnv, prompt, sessionID string) string {
	var sb strings.Builder
	if pathEnv != "" {
		sb.WriteString("PATH=")
		sb.WriteString(shellSingleQuote(pathEnv))
		sb.WriteString(" ")
	}
	sb.WriteString(shellSingleQuote(binary))
	sb.WriteString(" -p ")
	sb.WriteString(shellSingleQuote(prompt))
	sb.WriteString(" --output-format stream-json --include-partial-messages --verbose")
	if sessionID != "" {
		sb.WriteString(" --resume ")
		sb.WriteString(shellSingleQuote(sessionID))
	}
	// stderr ham streamga qo'shilsin (xato xabarlari ko'rinishi uchun)
	sb.WriteString(" 2>&1")
	return sb.String()
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
	// Faqat JSON satrlarni hisobga olamiz; non-JSON (masalan PATH dan claude topilmasa
	// bash xato) — Type="" qaytadi va caller waitErr bilan ishlov beradi.
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

// editClaudeText placeholder xabarni Claude javobi bilan yangilaydi. final=true
// bo'lsa "✍️" yozish indikatori olib tashlanadi.
func (s *Session) editClaudeText(msgID int, body string, final bool) {
	body = strings.TrimRight(body, "\n")
	if len(body) > maxMsgChars {
		body = "…\n" + body[len(body)-maxMsgChars:]
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
			log.Printf("claude edit (tg=%d): %v", s.TelegramID, err)
		}
	}
}
