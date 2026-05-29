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

// SessionMode — chat default rejimi: free-text qaysi handlerga ketadi.
type SessionMode string

const (
	ModePowerShell SessionMode = "powershell" // default: free-text → PS
	ModeClaude     SessionMode = "claude"     // free-text → Claude
)

// Session — bitta Telegram chat (private yoki group) uchun konteks.
// Group chat'da hamma a'zo shu bitta sessiyani baham ko'radi (jamoa xotirasi).
type Session struct {
	ChatID       int64
	bot          *tgbotapi.BotAPI
	workdir      string
	systemPrompt string // --append-system-prompt uchun (persona)

	mu              sync.Mutex
	mode            SessionMode        // free-text default rejimi
	claudeSessionID string             // claude --resume uchun
	claudeBinary    string             // probed yo'l (lazy)
	claudeCancel    context.CancelFunc // aktiv exec'ni uzish (Claude yoki PS)
	claudePID       int                // aktiv PowerShell wrapper PID (taskkill /T uchun)

	// cmdSlot — buferli kanal o'lchami 1: send-acquire/recv-release pattern bilan
	// mutex vazifasini bajaradi, lekin timed-acquire imkonini beradi (Mutex.Lock
	// timeout qabul qilmaydi). Yangi promtlar kutib turishi mumkin (long-running
	// 10-15 daqiqalik ishlar uchun navbat kerak), lekin cap claudeQueueWait bilan.
	cmdSlot chan struct{}

	// queueGen — joriy "navbat avlodi". /stop bosilganda butun avlod cancel
	// qilinadi (navbatdagi barcha promtlar bail qiladi), keyin yangi avlod
	// boshlanadi. Aks holda /stop faqat hozirgi promtni o'ldirardi va
	// navbatdagisi avtomatik ishga tushib ketardi.
	queueGen *queueGen
}

// queueGen — bitta navbat avlodi: kontekst va uni cancel qiluvchi func.
type queueGen struct {
	ctx    context.Context
	cancel context.CancelFunc
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
		mode:         ModePowerShell,
		cmdSlot:      make(chan struct{}, 1),
	}
	m.sessions[chatID] = s
	return s
}

// Mode joriy free-text rejimi.
func (s *Session) Mode() SessionMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

// SetMode rejimni o'zgartiradi.
func (s *Session) SetMode(m SessionMode) {
	s.mu.Lock()
	s.mode = m
	s.mu.Unlock()
}

// Workdir sessiya ishlayotgan papkani qaytaradi.
func (s *Session) Workdir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workdir
}

// Reset Claude sessiyasini tozalaydi (--resume bekor qilinadi). Mode o'zgarmaydi.
func (s *Session) Reset() {
	s.mu.Lock()
	s.claudeSessionID = ""
	s.mu.Unlock()
}

// CurrentQueueGen joriy navbat avlodini qaytaradi (yangi prompt qaysi
// generation'ga tegishli ekanini bilishi uchun). Hech qachon nil emas —
// agar bo'sh bo'lsa, yangi yaratiladi.
func (s *Session) CurrentQueueGen() *queueGen {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.queueGen == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.queueGen = &queueGen{ctx: ctx, cancel: cancel}
	}
	return s.queueGen
}

// SendInterrupt aktiv Claude exec'ni uzadi (Ctrl+C analog) VA navbatdagi
// barcha promtlarni ham bekor qiladi. cmd slot'ni KUTMAYDI.
// Uch yo'l bilan urinadi:
//
//	(1) context cancel — Go runtime cmd.Cancel'ni chaqiradi
//	(2) bevosita `taskkill /F /T /PID <ps_pid>` — Go internal'ida ilinib qolishlardan xalos
//	(3) joriy navbat avlodini cancel qiladi — navbatdagilar avtomatik
//	    ishga tushishini oldini oladi
func (s *Session) SendInterrupt() {
	s.mu.Lock()
	cancel := s.claudeCancel
	pid := s.claudePID
	oldGen := s.queueGen
	s.claudeCancel = nil
	s.claudePID = 0
	// Yangi avlod boshlaymiz — endi kelayotgan promtlar shu /stop'dan ta'sirlanmaydi.
	newCtx, newCancel := context.WithCancel(context.Background())
	s.queueGen = &queueGen{ctx: newCtx, cancel: newCancel}
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if pid > 0 {
		// /T — butun daraxt (PowerShell + claude.exe + node MCP children).
		_ = exec.Command("taskkill.exe", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
	}
	if oldGen != nil {
		// Navbatda kutayotgan barcha goroutine'lar shu ctx.Done()'ni ko'rib bail qiladi.
		oldGen.cancel()
	}
}

func (s *Session) replyError(text string) {
	msg := tgbotapi.NewMessage(s.ChatID, "⚠️ "+text)
	_, _ = s.bot.Send(msg)
}
