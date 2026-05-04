package bot

import (
	"bytes"
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

// altScreenEnter — TUI dasturlari (htop, vim, less, nano) ekranni "alt screen buffer" ga
// ko'chirganda yuboradigan kod. Aniqlansa bot foydalanuvchini ogohlantiradi va Ctrl+C yuboradi.
var altScreenEnter = []byte("\x1b[?1049h")

const (
	cmdQuietWindow = 700 * time.Millisecond // shu vaqt davomida output bo'lmasa "tugagan" deb hisoblaymiz
	cmdMaxWait     = 15 * time.Second       // bitta komandaga maksimum kutish
	idleDefault    = 30 * time.Minute
	maxMsgChars    = 3800 // Telegram 4096 limit minus <pre>...</pre> tag joyi

	liveInterval = 2 * time.Second // har necha sekundda live xabar yangilanadi
	liveMaxTicks = 90              // ~3 daqiqa
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

	// Live shortcut state (faqat bitta aktiv bo'la oladi)
	liveStop chan struct{} // close qilinsa goroutine to'xtaydi
	liveMsg  int           // hozir tahrirlanayotgan xabar IDsi
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
	// Aktiv live task'ni ham to'xtatamiz
	if s.liveStop != nil {
		close(s.liveStop)
		s.liveStop = nil
	}
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
// Agar oqimda alt-screen-enter (TUI signal) aniqlansa darrov qaytadi —
// foydalanuvchini 15 sek kuttirmaslik uchun.
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
		if bytes.Contains(chunk, altScreenEnter) {
			return out
		}
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
			if bytes.Contains(chunk, altScreenEnter) {
				return out
			}
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
// cmdMu ushlab turadi — yo'qsa user tezda komanda yozsa rawOut ikki o'quvchi o'rtasida
// taqsimlanib, birinchi belgilar tushib qoladi.
func (s *Session) drainBanner() {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()
	out := s.collectUntilQuiet(cmdQuietWindow, 3*time.Second)
	if len(out) > 0 {
		s.sendOutput(out)
	}
}

// sendOutput collected raw bytes'ni Telegramga jo'natadi.
// TUI dasturlari (htop, vim, ...) aniqlansa — output o'rniga ogohlantiruv yuboriladi va Ctrl+C jo'natiladi.
func (s *Session) sendOutput(out []byte) {
	if bytes.Contains(out, altScreenEnter) {
		// TUI — to'liq ekranli dastur. Telegram'da ko'rsatib bo'lmaydi.
		// Avval Ctrl+C yuboramiz (ko'pchilik TUI undan chiqadi yoki signal handlerini ishga tushiradi),
		// keyin foydalanuvchini ogohlantiramiz.
		_, _ = io.WriteString(s.Conn.Stdin, "\x03")
		text := "⚠️ Interaktiv (TUI) komanda aniqlandi va to'xtatildi.\n\n" +
			"Telegram chatda <code>htop</code>, <code>vim</code>, <code>less</code>, <code>nano</code> kabi to'liq ekranli dasturlar ishlamaydi.\n\n" +
			"Snapshot uchun shortcut'lardan foydalaning: /htop /ps /disk /uptime /free"
		msg := tgbotapi.NewMessage(s.ChatID, text)
		msg.ParseMode = tgbotapi.ModeHTML
		_, _ = s.bot.Send(msg)
		return
	}

	clean := strings.TrimRight(stripANSI(string(out)), "\n")
	if clean == "" {
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

// runOnceLocked cmdMu ostida bitta komandani bajarib outputni qaytaradi (live ticker uchun).
func (s *Session) runOnceLocked(shellCmd string) ([]byte, bool) {
	s.cmdMu.Lock()
	defer s.cmdMu.Unlock()

	if !s.markActive() {
		return nil, false
	}

	s.drainAvailable()
	if _, err := io.WriteString(s.Conn.Stdin, shellCmd+"\n"); err != nil {
		return nil, false
	}
	return s.collectUntilQuiet(cmdQuietWindow, 5*time.Second), true
}

// StartLive berilgan shellCmd'ni davriy ravishda bajarib, bitta xabarni tahrirlab turadi.
// Eski live (agar bor bo'lsa) avtomatik to'xtatiladi.
func (s *Session) StartLive(chatID int64, shellCmd string) {
	s.StopLive()

	out, ok := s.runOnceLocked(shellCmd)
	if !ok {
		return
	}

	stopCh := make(chan struct{})
	text := formatLiveOutput(out)
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	kb := liveStopKeyboard()
	msg.ReplyMarkup = kb
	sent, err := s.bot.Send(msg)
	if err != nil {
		log.Printf("live initial send (tg=%d): %v", s.TelegramID, err)
		return
	}

	s.mu.Lock()
	s.liveStop = stopCh
	s.liveMsg = sent.MessageID
	s.mu.Unlock()

	go s.liveLoop(chatID, sent.MessageID, shellCmd, stopCh)
}

// StopLive aktiv live task'ni to'xtatadi.
func (s *Session) StopLive() {
	s.mu.Lock()
	stop := s.liveStop
	s.liveStop = nil
	s.liveMsg = 0
	s.mu.Unlock()
	if stop != nil {
		close(stop)
	}
}

func (s *Session) liveLoop(chatID int64, msgID int, shellCmd string, stopCh chan struct{}) {
	ticker := time.NewTicker(liveInterval)
	defer ticker.Stop()

	var lastText string
	stoppedReason := "Avto-to'xtash (3 daq)"

	for tick := 0; tick < liveMaxTicks; tick++ {
		select {
		case <-stopCh:
			stoppedReason = "Foydalanuvchi to'xtatdi"
			goto finalize
		case <-s.closeSig:
			return
		case <-ticker.C:
		}

		out, ok := s.runOnceLocked(shellCmd)
		if !ok {
			return
		}
		lastText = formatLiveOutput(out)

		edit := tgbotapi.NewEditMessageText(chatID, msgID, lastText)
		edit.ParseMode = tgbotapi.ModeHTML
		kb := liveStopKeyboard()
		edit.ReplyMarkup = &kb
		if _, err := s.bot.Send(edit); err != nil {
			// "message is not modified" — normal, e'tibor bermaymiz
			if !strings.Contains(err.Error(), "not modified") {
				log.Printf("live edit (tg=%d): %v", s.TelegramID, err)
			}
		}
	}

finalize:
	// Oxirgi tahrir: LIVE markerni "Stopped" bilan almashtirib, tugmani olib tashlaymiz
	final := strings.Replace(lastText, "🔴 LIVE", "⏹ "+stoppedReason, 1)
	if final == "" {
		final = "<i>⏹ " + htmlEscape(stoppedReason) + "</i>"
	}
	finalEdit := tgbotapi.NewEditMessageText(chatID, msgID, final)
	finalEdit.ParseMode = tgbotapi.ModeHTML
	_, _ = s.bot.Send(finalEdit)
}

// formatLiveOutput live xabar matnini tayyorlaydi (LIVE marker + timestamp + <pre> output).
func formatLiveOutput(out []byte) string {
	clean := strings.TrimRight(stripANSI(string(out)), "\n")
	if len(clean) > maxMsgChars {
		clean = "…\n" + clean[len(clean)-maxMsgChars:]
	}
	ts := time.Now().Format("15:04:05")
	return fmt.Sprintf("🔴 LIVE • %s\n<pre>%s</pre>", ts, htmlEscape(clean))
}

func liveStopKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🛑 Stop", "live:stop"),
		),
	)
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
