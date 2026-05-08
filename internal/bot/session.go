package bot

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const maxMsgChars = 3800

// Session — bitta Telegram chat (private yoki group) uchun Claude agent konteksti.
// Group chat'da hamma a'zo shu bitta sessiyani baham ko'radi (jamoa xotirasi).
type Session struct {
	ChatID       int64
	bot          *tgbotapi.BotAPI
	workdir      string
	systemPrompt string // --append-system-prompt uchun (persona)
	logsDir      string // har bir prompt+javobning .log fayli bu yerga yoziladi

	mu              sync.Mutex
	claudeSessionID string             // claude --resume uchun
	claudeBinary    string             // probed yo'l (lazy)
	claudeCancel    context.CancelFunc // aktiv claude exec'ni uzish

	cmdMu sync.Mutex // bir chat ichida bir vaqtda bitta Claude prompti
}

// Manager — barcha chat sessiyalari ro'yxati. Lazy yaratiladi.
type Manager struct {
	mu           sync.Mutex
	sessions     map[int64]*Session
	bot          *tgbotapi.BotAPI
	workdir      string
	systemPrompt string
	logsDir      string // <workdir>/antiterror-logs
}

func NewManager(bot *tgbotapi.BotAPI, workdir, systemPrompt string) *Manager {
	if workdir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			workdir = home
		} else {
			workdir = "."
		}
	}
	logsDir := filepath.Join(workdir, "antiterror-logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		log.Printf("logs dir create (%s): %v", logsDir, err)
	}
	return &Manager{
		sessions:     map[int64]*Session{},
		bot:          bot,
		workdir:      workdir,
		systemPrompt: systemPrompt,
		logsDir:      logsDir,
	}
}

func (m *Manager) Workdir() string { return m.workdir }
func (m *Manager) LogsDir() string { return m.logsDir }

// Get chat bo'yicha sessiyani qaytaradi yoki yangi yaratadi.
func (m *Manager) Get(chatID int64) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[chatID]; ok {
		return s
	}
	s := &Session{
		ChatID:       chatID,
		bot:          m.bot,
		workdir:      m.workdir,
		systemPrompt: m.systemPrompt,
		logsDir:      m.logsDir,
	}
	m.sessions[chatID] = s
	return s
}

// Reset Claude sessiyasini tozalaydi (--resume bekor qilinadi).
func (s *Session) Reset() {
	s.mu.Lock()
	s.claudeSessionID = ""
	s.mu.Unlock()
}

// SendInterrupt aktiv Claude exec'ni uzadi (Ctrl+C analog).
// cmdMu'ni KUTMAYDI — turg'un promptni uzish kerak.
func (s *Session) SendInterrupt() {
	s.mu.Lock()
	cancel := s.claudeCancel
	s.claudeCancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Session) replyError(text string) {
	msg := tgbotapi.NewMessage(s.ChatID, "⚠️ "+text)
	_, _ = s.bot.Send(msg)
}
