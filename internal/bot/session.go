package bot

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"remofy-bot/internal/models"
	"remofy-bot/internal/sshconn"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	cmdQuietWindow = 700 * time.Millisecond // shu vaqt davomida output bo'lmasa "tugagan" deb hisoblaymiz
	cmdMaxWait     = 15 * time.Second       // bitta komandaga maksimum kutish
	idleDefault    = 30 * time.Minute
	maxMsgChars    = 3800 // Telegram 4096 limit minus <pre>...</pre> tag joyi
)

// Session — bir Telegram foydalanuvchisining ulangan SSH sessiyasi.
// Har bir komanda → bitta javob xabari (1-to-1).
type Session struct {
	TelegramID  int64
	ChatID      int64
	Server      models.Server
	Conn        *sshconn.Conn
	LastInputAt time.Time

	bot         *tgbotapi.BotAPI
	idleTimeout time.Duration

	rawOut    chan []byte   // SSH stdout chunklari
	interrupt chan struct{} // Ctrl+C kutayotgan komandani uzish
	closeSig  chan struct{}
	closed    bool

	cmdMu sync.Mutex // bir vaqtda bitta komanda
	mu    sync.Mutex
}

type Manager struct {
	mu          sync.Mutex
	sessions    map[int64]*Session
	bot         *tgbotapi.BotAPI
	idleTimeout time.Duration
}

func NewManager(bot *tgbotapi.BotAPI, idle time.Duration) *Manager {
	if idle <= 0 {
		idle = idleDefault
	}
	return &Manager{sessions: map[int64]*Session{}, bot: bot, idleTimeout: idle}
}

func (m *Manager) Get(telegramID int64) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[telegramID]
}

func (m *Manager) Remove(telegramID int64) {
	m.mu.Lock()
	delete(m.sessions, telegramID)
	m.mu.Unlock()
}

// Open yangi sessiya ochadi (eski bo'lsa yopib). Welcome xabar reply keyboard bilan yuboriladi.
func (m *Manager) Open(ctx context.Context, telegramID, chatID int64, server models.Server) (*Session, error) {
	if old := m.Get(telegramID); old != nil {
		old.Close("yangi ulanish ochildi")
		m.Remove(telegramID)
	}

	conn, err := sshconn.Open(ctx, server)
	if err != nil {
		return nil, err
	}

	s := &Session{
		TelegramID:  telegramID,
		ChatID:      chatID,
		Server:      server,
		Conn:        conn,
		LastInputAt: time.Now(),
		bot:         m.bot,
		idleTimeout: m.idleTimeout,
		rawOut:      make(chan []byte, 64),
		interrupt:   make(chan struct{}, 1),
		closeSig:    make(chan struct{}),
	}

	welcome := fmt.Sprintf("🔌 <b>%s</b> ga ulandi (%s@%s:%d)\n<i>Komanda yozing — har biri uchun alohida javob keladi</i>",
		htmlEscape(server.Name), htmlEscape(server.Username), htmlEscape(server.Host), server.Port)
	msg := tgbotapi.NewMessage(chatID, welcome)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.ReplyMarkup = sessionReplyKeyboard()
	if _, err := m.bot.Send(msg); err != nil {
		conn.Close()
		return nil, fmt.Errorf("welcome send: %w", err)
	}

	m.mu.Lock()
	m.sessions[telegramID] = s
	m.mu.Unlock()

	go s.readPump()
	go s.idleWatchdog(m)
	go s.drainBanner() // motd, dastlabki prompt

	return s, nil
}

// Close sessiyani yopadi va keyboard'ni olib tashlaydi.
func (s *Session) Close(reason string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.closeSig)
	s.mu.Unlock()

	if s.Conn != nil {
		s.Conn.Close()
	}
	if reason != "" {
		msg := tgbotapi.NewMessage(s.ChatID, "🔌 Sessiya yopildi: "+reason)
		msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
		_, _ = s.bot.Send(msg)
	}
}

// RunCommand foydalanuvchi yozgan komandani bajaradi va outputni 1 ta xabarda yuboradi.
// cmdMu bilan ketma-ket bajariladi.
func (s *Session) RunCommand(text string) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()

	if !s.markActive() {
		return
	}

	s.drainAvailable()

	if _, err := io.WriteString(s.Conn.Stdin, text+"\n"); err != nil {
		s.replyError("Yozish xato: " + err.Error())
		return
	}

	out := s.collectUntilQuiet(cmdQuietWindow, cmdMaxWait)
	s.sendOutput(out)
}

// SendKey maxsus tugma (Tab, Enter, ↑↓, Esc, Disconnect emas) — komanda kabi yuboriladi,
// lekin newline qo'shilmaydi (raw kod).
func (s *Session) SendKey(raw string) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()

	if !s.markActive() {
		return
	}

	s.drainAvailable()

	if _, err := io.WriteString(s.Conn.Stdin, raw); err != nil {
		s.replyError("Yozish xato: " + err.Error())
		return
	}

	out := s.collectUntilQuiet(cmdQuietWindow, cmdMaxWait)
	s.sendOutput(out)
}

// SendInterrupt Ctrl+C — cmdMu'ni KUTMAYDI, to'g'ridan-to'g'ri stdin'ga yozadi
// va kutayotgan collectUntilQuiet'ni to'xtatadi.
func (s *Session) SendInterrupt() {
	if !s.markActive() {
		return
	}
	if _, err := io.WriteString(s.Conn.Stdin, "\x03"); err != nil {
		log.Printf("interrupt write (tg=%d): %v", s.TelegramID, err)
		return
	}
	select {
	case s.interrupt <- struct{}{}:
	default:
	}
}

// markActive timestamp yangilaydi va sessiya tirik ekanini tekshiradi.
func (s *Session) markActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.LastInputAt = time.Now()
	return true
}

// readPump SSH stdout'dan o'qib, rawOut kanaliga yuboradi.
func (s *Session) readPump() {
	buf := make([]byte, 8192)
	for {
		n, err := s.Conn.Stdout.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			select {
			case s.rawOut <- chunk:
			case <-s.closeSig:
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("readPump (tg=%d): %v", s.TelegramID, err)
			}
			s.Close("ulanish uzildi")
			return
		}
	}
}

// drainAvailable rawOut'da to'planib qolgan eski byte'larni tashlab yuboradi (oldingi
// komandadan kech kelgan prompt va h.k.). BLOK QILMAYDI.
func (s *Session) drainAvailable() {
	for {
		select {
		case <-s.rawOut:
		default:
			return
		}
	}
}

// collectUntilQuiet output kelishini kutadi. Birinchi byte kelganidan keyin "quiet" davomida
// hech narsa kelmasa — tugadi deb hisoblaydi. max — qattiq cheklov.
func (s *Session) collectUntilQuiet(quiet, max time.Duration) []byte {
	var out []byte
	deadline := time.NewTimer(max)
	defer deadline.Stop()

	// 1) Birinchi byte (yoki deadline / interrupt / close)
	select {
	case chunk, ok := <-s.rawOut:
		if !ok {
			return out
		}
		out = append(out, chunk...)
	case <-s.interrupt:
		return out
	case <-deadline.C:
		return out
	case <-s.closeSig:
		return out
	}

	// 2) Quiet oynasi
	for {
		quietT := time.NewTimer(quiet)
		select {
		case chunk, ok := <-s.rawOut:
			quietT.Stop()
			if !ok {
				return out
			}
			out = append(out, chunk...)
		case <-quietT.C:
			return out
		case <-s.interrupt:
			quietT.Stop()
			return out
		case <-deadline.C:
			quietT.Stop()
			return out
		case <-s.closeSig:
			quietT.Stop()
			return out
		}
	}
}

// drainBanner SSH ulanganda kelgan dastlabki output (motd, prompt) ni 1 ta xabarda yuboradi.
func (s *Session) drainBanner() {
	out := s.collectUntilQuiet(cmdQuietWindow, 5*time.Second)
	if len(out) > 0 {
		s.sendOutput(out)
	}
}

// sendOutput collected raw bytes'ni Telegramga jo'natadi.
func (s *Session) sendOutput(out []byte) {
	clean := strings.TrimRight(stripANSI(string(out)), "\n")
	if clean == "" {
		// Hech narsa qaytarmasa, foydalanuvchi bilsin (komanda ishladi)
		msg := tgbotapi.NewMessage(s.ChatID, "✓")
		_, _ = s.bot.Send(msg)
		return
	}
	if len(clean) > maxMsgChars {
		clean = "…\n" + clean[len(clean)-maxMsgChars:]
	}
	msg := tgbotapi.NewMessage(s.ChatID, "<pre>"+htmlEscape(clean)+"</pre>")
	msg.ParseMode = tgbotapi.ModeHTML
	if _, err := s.bot.Send(msg); err != nil {
		log.Printf("sendOutput (tg=%d): %v", s.TelegramID, err)
	}
}

// replyError xatolik xabarini yuboradi (oddiy text).
func (s *Session) replyError(text string) {
	msg := tgbotapi.NewMessage(s.ChatID, "⚠️ "+text)
	_, _ = s.bot.Send(msg)
}

// idleWatchdog idle bo'lsa avtomatik yopadi.
func (s *Session) idleWatchdog(m *Manager) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.closeSig:
			m.Remove(s.TelegramID)
			return
		case <-t.C:
			s.mu.Lock()
			idle := time.Since(s.LastInputAt)
			s.mu.Unlock()
			if idle >= s.idleTimeout {
				s.Close(fmt.Sprintf("idle timeout (%s)", s.idleTimeout))
				m.Remove(s.TelegramID)
				return
			}
		}
	}
}

// sessionReplyKeyboard sessiya davomida pastda turadigan persistent klaviatura.
func sessionReplyKeyboard() tgbotapi.ReplyKeyboardMarkup {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("Ctrl+C"),
			tgbotapi.NewKeyboardButton("Tab"),
			tgbotapi.NewKeyboardButton("Enter"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("↑"),
			tgbotapi.NewKeyboardButton("↓"),
			tgbotapi.NewKeyboardButton("Esc"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("🔌 Disconnect"),
		),
	)
	kb.ResizeKeyboard = true
	return kb
}
