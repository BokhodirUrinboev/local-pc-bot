package bot

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	maxMsgChars  = 3800
	cmdMaxWait   = 30 * time.Minute
	editInterval = 1500 * time.Millisecond

	// cwdMarker — komanda tugagandan keyin yangi CWD'ni ajratib olish uchun.
	// Foydalanuvchi tasodifan shu satrni chiqarmaydi degan farazda.
	cwdMarker = "<<<__REMOFY_CWD_MARKER__>>>"
)

// Session — bir Telegram foydalanuvchisining lokal shell konteksti (CWD + Claude state).
// Bot bilan bir vaqtda bir nechta foydalanuvchi ishlay oladi (alohida sessiya).
type Session struct {
	TelegramID int64
	ChatID     int64
	bot        *tgbotapi.BotAPI

	mu        sync.Mutex
	cwd       string
	runCancel context.CancelFunc // aktiv RunCommand kontekstini bekor qilish

	cmdMu sync.Mutex // RunCommand / RunClaude — bir vaqtda bittasi ishlasin

	// Claude rejimi
	claudeMode      bool
	claudeSessionID string             // claude --resume uchun
	claudeBinary    string             // probed yo'l
	claudeCancel    context.CancelFunc // aktiv claude exec'ni uzish
}

// Manager — barcha sessiyalar ro'yxati. Sessiya birinchi xabar kelganda lazy yaratiladi.
type Manager struct {
	mu         sync.Mutex
	sessions   map[int64]*Session
	bot        *tgbotapi.BotAPI
	defaultCwd string
}

func NewManager(bot *tgbotapi.BotAPI, defaultCwd string) *Manager {
	if defaultCwd == "" {
		if home, err := os.UserHomeDir(); err == nil {
			defaultCwd = home
		} else {
			defaultCwd = "."
		}
	}
	return &Manager{
		sessions:   map[int64]*Session{},
		bot:        bot,
		defaultCwd: defaultCwd,
	}
}

// Get sessiyani qaytaradi yoki yangi yaratadi.
func (m *Manager) Get(tgID, chatID int64) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[tgID]
	if !ok {
		s = &Session{
			TelegramID: tgID,
			ChatID:     chatID,
			bot:        m.bot,
			cwd:        m.defaultCwd,
		}
		m.sessions[tgID] = s
	} else {
		// Yangi chatdan yozsa ChatID'ni yangilaymiz
		s.ChatID = chatID
	}
	return s
}

// Cwd joriy ish papkasini qaytaradi.
func (s *Session) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

// SendInterrupt aktiv RunCommand va Claude exec'ni bekor qiladi (Ctrl+C analog).
// cmdMu'ni KUTMAYDI — kutayotgan komandani uzish kerak.
func (s *Session) SendInterrupt() {
	s.mu.Lock()
	runCancel := s.runCancel
	claudeCancel := s.claudeCancel
	s.runCancel = nil
	s.claudeCancel = nil
	s.mu.Unlock()

	if runCancel != nil {
		runCancel()
	}
	if claudeCancel != nil {
		claudeCancel()
	}
}

// RunCommand foydalanuvchi yozgan matnni PowerShell komandasi sifatida bajaradi.
// Joriy CWD ostida ishga tushiriladi va komanda tugagach yangi CWD saqlanadi.
// Live update: chiqish ko'paygan sari Telegram xabari ~1.5s da yangilanadi.
func (s *Session) RunCommand(text string) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()

	s.mu.Lock()
	cwd := s.cwd
	s.mu.Unlock()

	// PowerShell wrapper:
	//  1) joriy papkaga o'tish
	//  2) foydalanuvchi komandasi
	//  3) marker + yangi CWD
	// $ErrorActionPreference = 'Continue' — non-terminating xatolarni ko'rsatadi
	// lekin to'xtatib qo'ymaydi (cmd-style behavior).
	script := fmt.Sprintf(`Set-Location -LiteralPath %s
$ErrorActionPreference = 'Continue'
%s

Write-Output '%s'
Write-Output (Get-Location).Path
`, psQuote(cwd), text, cwdMarker)

	ctx, cancel := context.WithTimeout(context.Background(), cmdMaxWait)
	defer cancel()

	s.mu.Lock()
	s.runCancel = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.runCancel != nil {
			s.runCancel = nil
		}
		s.mu.Unlock()
	}()

	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NoLogo", "-NonInteractive", "-Command", "-")
	cmd.Stdin = strings.NewReader(script)

	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		s.replyError("StdoutPipe: " + err.Error())
		return
	}
	stderrR, err := cmd.StderrPipe()
	if err != nil {
		s.replyError("StderrPipe: " + err.Error())
		return
	}

	if err := cmd.Start(); err != nil {
		s.replyError("Start xato: " + err.Error())
		return
	}

	// Birgalikdagi bufer — stdout va stderr birlashtiriladi
	var (
		bufMu sync.Mutex
		buf   bytes.Buffer
	)
	drain := func(r io.Reader) {
		scratch := make([]byte, 4096)
		for {
			n, rerr := r.Read(scratch)
			if n > 0 {
				bufMu.Lock()
				buf.Write(scratch[:n])
				bufMu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); drain(stdoutR) }()
	go func() { defer wg.Done(); drain(stderrR) }()

	// Placeholder xabar
	placeholder := tgbotapi.NewMessage(s.ChatID, "⏳ <i>ishlamoqda...</i>")
	placeholder.ParseMode = tgbotapi.ModeHTML
	sent, perr := s.bot.Send(placeholder)
	if perr != nil {
		log.Printf("placeholder send (tg=%d): %v", s.TelegramID, perr)
		// Davom etamiz — yakunda har holda buferni yuborishga harakat qilamiz
	}
	msgID := sent.MessageID

	// Live edit — 1.5s da bir marta xabarni yangilaymiz
	done := make(chan struct{})
	go func() {
		wg.Wait()
		_ = cmd.Wait()
		close(done)
	}()

	var lastText string
	ticker := time.NewTicker(editInterval)
	defer ticker.Stop()

editLoop:
	for {
		select {
		case <-done:
			break editLoop
		case <-ticker.C:
			if msgID == 0 {
				continue
			}
			bufMu.Lock()
			cur := buf.String()
			bufMu.Unlock()
			body, _ := splitCwd(cur)
			text := formatRunOutput(body, false)
			if text == lastText {
				continue
			}
			edit := tgbotapi.NewEditMessageText(s.ChatID, msgID, text)
			edit.ParseMode = tgbotapi.ModeHTML
			if _, err := s.bot.Send(edit); err != nil {
				if !strings.Contains(err.Error(), "not modified") {
					log.Printf("live edit (tg=%d): %v", s.TelegramID, err)
				}
			}
			lastText = text
		}
	}

	// Yakuniy
	bufMu.Lock()
	final := buf.String()
	bufMu.Unlock()

	body, newCwd := splitCwd(final)
	if newCwd != "" {
		s.mu.Lock()
		s.cwd = newCwd
		s.mu.Unlock()
	}

	// Cancel sabab? (Stop bosildi)
	canceled := ctx.Err() == context.Canceled
	timedOut := ctx.Err() == context.DeadlineExceeded

	finalText := formatRunOutput(body, true)
	if canceled {
		finalText = "⏹ <i>foydalanuvchi to'xtatdi</i>\n" + finalText
	} else if timedOut {
		finalText = "⌛ <i>vaqt tugadi (30 daq)</i>\n" + finalText
	}

	if msgID == 0 {
		// Placeholder yuborilmadi — yangi xabar yuboramiz
		msg := tgbotapi.NewMessage(s.ChatID, finalText)
		msg.ParseMode = tgbotapi.ModeHTML
		_, _ = s.bot.Send(msg)
		return
	}

	edit := tgbotapi.NewEditMessageText(s.ChatID, msgID, finalText)
	edit.ParseMode = tgbotapi.ModeHTML
	if _, err := s.bot.Send(edit); err != nil {
		if !strings.Contains(err.Error(), "not modified") {
			log.Printf("final edit (tg=%d): %v", s.TelegramID, err)
		}
	}
}

// splitCwd raw chiqishni ko'rsatiladigan tana va yangi CWD'ga ajratadi.
// cwdMarker'dan keyingi satr — yangi CWD. Marker topilmasa newCwd="" qaytadi
// (komanda tugamagan yoki crash bo'lgan).
func splitCwd(raw string) (body, newCwd string) {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	idx := strings.LastIndex(text, cwdMarker)
	if idx < 0 {
		return strings.TrimRight(text, "\n"), ""
	}
	head := text[:idx]
	tail := text[idx+len(cwdMarker):]
	tail = strings.TrimLeft(tail, "\n")
	eol := strings.IndexByte(tail, '\n')
	if eol < 0 {
		newCwd = strings.TrimSpace(tail)
	} else {
		newCwd = strings.TrimSpace(tail[:eol])
	}
	body = strings.TrimRight(head, "\n")
	return
}

// formatRunOutput Telegram xabari uchun matn tayyorlaydi.
func formatRunOutput(body string, final bool) string {
	body = stripANSI(body)
	body = strings.TrimRight(body, "\n")
	if body == "" {
		if final {
			return "✅ <i>(bo'sh chiqish)</i>"
		}
		return "⏳ <i>ishlamoqda...</i>"
	}
	if len(body) > maxMsgChars {
		body = "…\n" + body[len(body)-maxMsgChars:]
	}
	icon := "⏳"
	if final {
		icon = "✅"
	}
	return fmt.Sprintf("%s\n<pre>%s</pre>", icon, htmlEscape(body))
}

// psQuote PowerShell single-quote escaping (' → '').
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func (s *Session) replyError(text string) {
	msg := tgbotapi.NewMessage(s.ChatID, "⚠️ "+text)
	_, _ = s.bot.Send(msg)
}
