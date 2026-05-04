package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"remofy-bot/internal/models"
	"remofy-bot/internal/sshconn"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	bufferLines  = 30
	flushDelay   = 800 * time.Millisecond
	idleDefault  = 30 * time.Minute
	maxAnchorAge = 30 * time.Minute // bitta xabar maksimum shu vaqt edit qilinadi
)

// Session — bir Telegram foydalanuvchisining ulangan SSH sessiyasi.
type Session struct {
	TelegramID  int64
	ChatID      int64
	Server      models.Server
	Conn        *sshconn.Conn
	Buffer      *Buffer
	AnchorMsgID int
	AnchorAt    time.Time
	LastInputAt time.Time

	bot         *tgbotapi.BotAPI
	idleTimeout time.Duration

	flushSig chan struct{}
	closeSig chan struct{}
	closed   bool
	mu       sync.Mutex
}

// Manager — barcha aktiv sessiyalar.
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
	return &Manager{
		sessions:    map[int64]*Session{},
		bot:         bot,
		idleTimeout: idle,
	}
}

func (m *Manager) Get(telegramID int64) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[telegramID]
}

// Open yangi sessiya ochadi (eskisini yopib). Anchor xabar yaratiladi.
func (m *Manager) Open(ctx context.Context, telegramID, chatID int64, server models.Server) (*Session, error) {
	// Eski sessiya bo'lsa — yopamiz
	if old := m.Get(telegramID); old != nil {
		old.Close("yangi ulanish ochildi")
		m.mu.Lock()
		delete(m.sessions, telegramID)
		m.mu.Unlock()
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
		Buffer:      NewBuffer(bufferLines),
		LastInputAt: time.Now(),
		bot:         m.bot,
		idleTimeout: m.idleTimeout,
		flushSig:    make(chan struct{}, 1),
		closeSig:    make(chan struct{}),
	}

	// Anchor xabar yaratish
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("🔌 <b>%s</b> ga ulandi (%s@%s:%d)\n<i>Komanda yozing yoki /disconnect</i>",
		htmlEscape(server.Name), htmlEscape(server.Username), htmlEscape(server.Host), server.Port))
	msg.ParseMode = tgbotapi.ModeHTML
	msg.ReplyMarkup = sessionKeyboard()
	sent, err := m.bot.Send(msg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("anchor send: %w", err)
	}
	s.AnchorMsgID = sent.MessageID
	s.AnchorAt = time.Now()

	m.mu.Lock()
	m.sessions[telegramID] = s
	m.mu.Unlock()

	go s.readPump()
	go s.flushPump()
	go s.idleWatchdog(m)

	return s, nil
}

// Close sessiyani yopadi. reason — Telegramga yuboriladigan xabar.
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
		_, _ = s.bot.Send(msg)
	}
}

// Remove managerdan ham o'chiradi.
func (m *Manager) Remove(telegramID int64) {
	m.mu.Lock()
	delete(m.sessions, telegramID)
	m.mu.Unlock()
}

// WriteInput foydalanuvchi xabarini SSH stdin'ga yuboradi.
// trailingNewline — odatda true (komanda enter bilan tugashi kerak).
func (s *Session) WriteInput(data string, trailingNewline bool) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("session closed")
	}
	s.LastInputAt = time.Now()
	s.mu.Unlock()

	payload := data
	if trailingNewline {
		payload += "\n"
	}
	_, err := io.WriteString(s.Conn.Stdin, payload)
	return err
}

// readPump SSH stdout dan o'qib, Buffer ga yozadi va flushSig'ni signallaydi.
func (s *Session) readPump() {
	buf := make([]byte, 4096)
	for {
		n, err := s.Conn.Stdout.Read(buf)
		if n > 0 {
			s.Buffer.Append(buf[:n])
			select {
			case s.flushSig <- struct{}{}:
			default:
			}
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("read pump (tg=%d): %v", s.TelegramID, err)
			}
			s.Close("ulanish uzildi")
			return
		}
	}
}

// flushPump debounce qilib Telegram xabarini yangilaydi.
func (s *Session) flushPump() {
	var pending bool
	timer := time.NewTimer(time.Hour)
	timer.Stop()

	for {
		select {
		case <-s.closeSig:
			// Oxirgi marta yangilab chiqamiz
			if pending {
				s.flush()
			}
			return
		case <-s.flushSig:
			if !pending {
				pending = true
				timer.Reset(flushDelay)
			}
		case <-timer.C:
			pending = false
			s.flush()
		}
	}
}

// flush hozirgi buffer snapshot'ini Telegramga yuboradi (edit yoki yangi xabar).
func (s *Session) flush() {
	if s.Buffer.IsEmpty() {
		return
	}
	body := s.Buffer.Snapshot()
	if body == "" {
		return
	}
	text := "<pre>" + body + "</pre>"

	// Anchor juda eski yoki to'lib qolgan bo'lsa yangi xabar yaratamiz
	rotate := time.Since(s.AnchorAt) > maxAnchorAge

	if !rotate {
		edit := tgbotapi.NewEditMessageText(s.ChatID, s.AnchorMsgID, text)
		edit.ParseMode = tgbotapi.ModeHTML
		kb := sessionKeyboard()
		edit.ReplyMarkup = &kb
		if _, err := s.bot.Send(edit); err != nil {
			// Edit ishlamasa — yangi xabar
			rotate = true
		}
	}

	if rotate {
		msg := tgbotapi.NewMessage(s.ChatID, text)
		msg.ParseMode = tgbotapi.ModeHTML
		msg.ReplyMarkup = sessionKeyboard()
		sent, err := s.bot.Send(msg)
		if err != nil {
			log.Printf("flush new msg (tg=%d): %v", s.TelegramID, err)
			return
		}
		s.AnchorMsgID = sent.MessageID
		s.AnchorAt = time.Now()
		s.Buffer.Reset()
	}
}

// idleWatchdog idle timeoutdan o'tsa sessiyani yopadi.
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

// sessionKeyboard aktiv sessiya ostidagi tugmalar.
func sessionKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Ctrl+C", "key:ctrlc"),
			tgbotapi.NewInlineKeyboardButtonData("Tab", "key:tab"),
			tgbotapi.NewInlineKeyboardButtonData("Enter", "key:enter"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("↑", "key:up"),
			tgbotapi.NewInlineKeyboardButtonData("↓", "key:down"),
			tgbotapi.NewInlineKeyboardButtonData("Esc", "key:esc"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔌 Disconnect", "session:disconnect"),
		),
	)
}
