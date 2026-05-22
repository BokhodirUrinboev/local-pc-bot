package bot

import (
	"context"
	"os"
	"os/exec"
	"strconv"
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

	mu              sync.Mutex
	claudeSessionID string             // claude --resume uchun
	claudeBinary    string             // probed yo'l (lazy)
	claudeCancel    context.CancelFunc // aktiv claude exec'ni uzish
	claudePID       int                // aktiv PowerShell wrapper PID (taskkill /T uchun)

	// cmdSlot — buferli kanal o'lchami 1: send-acquire/recv-release pattern bilan
	// mutex vazifasini bajaradi, lekin timed-acquire imkonini beradi (Mutex.Lock
	// timeout qabul qilmaydi). Yangi promtlar kutib turishi mumkin (long-running
	// 10-15 daqiqalik ishlar uchun navbat kerak), lekin cap claudeQueueWait bilan.
	cmdSlot chan struct{}
}

// Manager — barcha chat sessiyalari ro'yxati. Lazy yaratiladi.
type Manager struct {
	mu           sync.Mutex
	sessions     map[int64]*Session
	bot          *tgbotapi.BotAPI
	workdir      string
	systemPrompt string
}

func NewManager(bot *tgbotapi.BotAPI, workdir, systemPrompt string) *Manager {
	if workdir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			workdir = home
		} else {
			workdir = "."
		}
	}
	return &Manager{
		sessions:     map[int64]*Session{},
		bot:          bot,
		workdir:      workdir,
		systemPrompt: systemPrompt,
	}
}

func (m *Manager) Workdir() string { return m.workdir }

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
		cmdSlot:      make(chan struct{}, 1),
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
// Ikki yo'l bilan urinadi: (1) context cancel — Go runtime cmd.Cancel'ni chaqiradi,
// (2) bevosita `taskkill /F /T /PID <ps_pid>` — agar (1) Go internal'ida ilinib qolsa
// ham, daraxt aniq o'ladi. Bularning ikkalasi cmdMu'ni ushlamasdan ishlaydi.
func (s *Session) SendInterrupt() {
	s.mu.Lock()
	cancel := s.claudeCancel
	pid := s.claudePID
	s.claudeCancel = nil
	s.claudePID = 0
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if pid > 0 {
		// /T — butun daraxt (PowerShell + claude.exe + node MCP children).
		_ = exec.Command("taskkill.exe", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
	}
}

func (s *Session) replyError(text string) {
	msg := tgbotapi.NewMessage(s.ChatID, "⚠️ "+text)
	_, _ = s.bot.Send(msg)
}
