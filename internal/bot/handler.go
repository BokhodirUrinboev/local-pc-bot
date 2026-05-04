package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"remofy-bot/internal/auth"
	"remofy-bot/internal/db"
	"remofy-bot/internal/models"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"gorm.io/gorm"
)

type Bot struct {
	API         *tgbotapi.BotAPI
	Mgr         *Manager
	PublicURL   string
	rootContext context.Context
}

func New(api *tgbotapi.BotAPI, mgr *Manager, publicURL string, ctx context.Context) *Bot {
	return &Bot{
		API:         api,
		Mgr:         mgr,
		PublicURL:   strings.TrimRight(publicURL, "/"),
		rootContext: ctx,
	}
}

// shellShortcuts — slash komanda → aktiv sessiyada ishlatiladigan shell buyrug'i.
// Hammasi NON-INTERACTIVE (TUI emas) — Telegram chat sharoitida snapshot beradi.
var shellShortcuts = map[string]string{
	"uptime": "uptime",
	"disk":   "df -h",
	"df":     "df -h",
	"free":   "free -h",
	"mem":    "free -h",
	"ps":     "ps auxf | head -30",
	"htop":   "top -bn1 -o %MEM | head -30",
	"top":    "top -bn1 -o %MEM | head -30",
	"who":    "who",
	"ip":     "ip -br a",
	"date":   "date",
}

// BotCommands Telegram'ning slash menyusiga registratsiya qilinadigan komandalar.
func BotCommands() []tgbotapi.BotCommand {
	return []tgbotapi.BotCommand{
		{Command: "start", Description: "Botni ishga tushirish / bog'lanish"},
		{Command: "servers", Description: "Server ro'yxati"},
		{Command: "connect", Description: "Serverga ulanish (id bilan)"},
		{Command: "disconnect", Description: "Sessiyani yopish"},
		{Command: "uptime", Description: "Server uptime"},
		{Command: "disk", Description: "Disk: df -h"},
		{Command: "free", Description: "Xotira: free -h"},
		{Command: "ps", Description: "Jarayonlar (top 30)"},
		{Command: "htop", Description: "Top snapshot (top -bn1)"},
		{Command: "who", Description: "Tizimga kirgan foydalanuvchilar"},
		{Command: "ip", Description: "IP manzillar (ip -br a)"},
		{Command: "raw", Description: "Xom baytlar yuborish (hex)"},
		{Command: "help", Description: "Yordam"},
	}
}

// keyButtons — reply keyboard'dagi tugma label'lari → SSH stdin'ga yoziladigan raw kodlar.
var keyButtons = map[string]string{
	"Tab":   "\t",
	"Enter": "\n",
	"↑":     "\x1b[A",
	"↓":     "\x1b[B",
	"Esc":   "\x1b",
}

const (
	btnInterrupt  = "Ctrl+C"
	btnDisconnect = "🔌 Disconnect"
)

// OnLinked — auth paketi tomonidan OAuth callback muvaffaqiyatli bo'lganda chaqiriladi.
func (b *Bot) OnLinked(telegramID int64, user models.User) {
	msg := tgbotapi.NewMessage(telegramID, fmt.Sprintf("✅ Bog'landi: <b>%s</b>\n\n/servers — server ro'yxati", htmlEscape(user.Email)))
	msg.ParseMode = tgbotapi.ModeHTML
	if _, err := b.API.Send(msg); err != nil {
		log.Printf("OnLinked send: %v", err)
	}
}

// HandleUpdate har bir Telegram yangilanishini qabul qiladi.
func (b *Bot) HandleUpdate(u tgbotapi.Update) {
	switch {
	case u.CallbackQuery != nil:
		b.handleCallback(u.CallbackQuery)
	case u.Message != nil:
		b.handleMessage(u.Message)
	}
}

func (b *Bot) handleMessage(m *tgbotapi.Message) {
	if m.From == nil {
		return
	}
	tgID := m.From.ID

	if m.IsCommand() {
		cmd := m.Command()
		// Shell shortcut'lar — aktiv sessiyada mos shell buyrug'ini bajaradi
		if shellCmd, ok := shellShortcuts[cmd]; ok {
			b.runShortcut(m, shellCmd)
			return
		}
		switch cmd {
		case "start":
			b.cmdStart(m)
		case "help":
			b.cmdHelp(m)
		case "servers":
			b.cmdServers(m)
		case "connect":
			b.cmdConnect(m)
		case "disconnect":
			b.cmdDisconnect(m)
		case "raw":
			b.cmdRaw(m)
		default:
			b.reply(m.Chat.ID, "Noma'lum komanda. /help")
		}
		return
	}

	sess := b.Mgr.Get(tgID)
	if sess == nil {
		b.reply(m.Chat.ID, "Aktiv sessiya yo'q. /servers ro'yxatdan tanlang yoki /connect <id>")
		return
	}

	text := m.Text

	// Reply keyboard tugmalari
	switch text {
	case btnDisconnect:
		sess.Close("foydalanuvchi tomonidan uzildi")
		b.Mgr.Remove(tgID)
		return
	case btnInterrupt:
		sess.SendInterrupt()
		return
	}
	if raw, ok := keyButtons[text]; ok {
		go sess.SendKey(raw)
		return
	}

	// Oddiy komanda
	go sess.RunCommand(text)
}

func (b *Bot) handleCallback(cb *tgbotapi.CallbackQuery) {
	defer func() {
		_, _ = b.API.Request(tgbotapi.NewCallback(cb.ID, ""))
	}()

	if cb.From == nil || cb.Message == nil {
		return
	}
	data := cb.Data

	if strings.HasPrefix(data, "connect:") {
		idStr := strings.TrimPrefix(data, "connect:")
		serverID, err := strconv.ParseUint(idStr, 10, 64)
		if err != nil {
			return
		}
		b.connectByID(cb.From.ID, cb.Message.Chat.ID, uint(serverID))
	}
}

// --- Komandalar ---

func (b *Bot) cmdStart(m *tgbotapi.Message) {
	tgID := m.From.ID
	user, ok := b.lookupUser(tgID)
	if ok {
		text := fmt.Sprintf("Salom, <b>%s</b>!\n\n/servers — serverlar ro'yxati\n/help — yordam", htmlEscape(user.Email))
		b.reply(m.Chat.ID, text)
		return
	}

	state := auth.NewLinkToken(tgID, m.From.UserName)
	url := fmt.Sprintf("%s/auth/google/login?state=%s", b.PublicURL, state)
	text := "👋 Remofy botiga xush kelibsiz!\n\nBoshlash uchun Google akkaunti bilan bog'laning. Link 10 daqiqa amal qiladi:"
	msg := tgbotapi.NewMessage(m.Chat.ID, text)
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonURL("🔐 Google bilan kirish", url),
		),
	)
	if _, err := b.API.Send(msg); err != nil {
		log.Printf("cmdStart send: %v", err)
	}
}

func (b *Bot) cmdHelp(m *tgbotapi.Message) {
	text := `<b>Remofy bot — komandalar</b>

<b>Sessiya:</b>
/start — bog'lanish (Google)
/servers — server ro'yxati
/connect &lt;id&gt; — serverga ulanish
/disconnect — sessiyani yopish

<b>Tezkor shell snapshotlar (aktiv sessiya kerak):</b>
/uptime — uptime
/disk yoki /df — df -h
/free yoki /mem — free -h
/ps — ps auxf | head -30
/htop yoki /top — top -bn1 | head -30
/who — kirgan foydalanuvchilar
/ip — IP manzillar

<b>Boshqa:</b>
/raw &lt;hex&gt; — xom baytlar (masalan "1b5b41" = ↑)
/help — shu yordam

<b>Sessiya ichida:</b>
• Har qanday matn — komanda sifatida yuboriladi
• Har komanda uchun alohida javob keladi
• Pastdagi tugmalar: Ctrl+C, Tab, Enter, ↑↓, Esc, Disconnect

<b>Cheklov:</b> vim/htop/nano kabi to'liq ekranli (TUI) dasturlar avtomatik to'xtatiladi — snapshot uchun yuqoridagi shortcut'lardan foydalaning.`
	b.reply(m.Chat.ID, text)
}

func (b *Bot) cmdServers(m *tgbotapi.Message) {
	user, ok := b.lookupUser(m.From.ID)
	if !ok {
		b.cmdStart(m)
		return
	}
	var servers []models.Server
	if err := db.DB.Where("user_id = ?", user.ID).Order("name").Find(&servers).Error; err != nil {
		b.reply(m.Chat.ID, "DB xato: "+err.Error())
		return
	}
	if len(servers) == 0 {
		b.reply(m.Chat.ID, "Sizda hali server yo'q. Web-ssh saytida server qo'shing.")
		return
	}

	rows := make([][]tgbotapi.InlineKeyboardButton, 0, len(servers))
	for _, s := range servers {
		label := fmt.Sprintf("%s (%s@%s)", s.Name, s.Username, s.Host)
		rows = append(rows, tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData(label, fmt.Sprintf("connect:%d", s.ID)),
		))
	}

	msg := tgbotapi.NewMessage(m.Chat.ID, fmt.Sprintf("Sizning serverlaringiz (%d):", len(servers)))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	if _, err := b.API.Send(msg); err != nil {
		log.Printf("cmdServers send: %v", err)
	}
}

func (b *Bot) cmdConnect(m *tgbotapi.Message) {
	user, ok := b.lookupUser(m.From.ID)
	if !ok {
		b.cmdStart(m)
		return
	}
	args := strings.TrimSpace(m.CommandArguments())
	if args == "" {
		b.reply(m.Chat.ID, "Ishlatish: /connect &lt;server_id&gt;\n\nServer IDsini /servers dan oling.")
		return
	}
	id, err := strconv.ParseUint(args, 10, 64)
	if err != nil {
		b.reply(m.Chat.ID, "ID raqam bo'lishi kerak.")
		return
	}
	_ = user
	b.connectByID(m.From.ID, m.Chat.ID, uint(id))
}

func (b *Bot) cmdDisconnect(m *tgbotapi.Message) {
	sess := b.Mgr.Get(m.From.ID)
	if sess == nil {
		b.reply(m.Chat.ID, "Aktiv sessiya yo'q.")
		return
	}
	sess.Close("foydalanuvchi tomonidan uzildi")
	b.Mgr.Remove(m.From.ID)
}

func (b *Bot) cmdRaw(m *tgbotapi.Message) {
	sess := b.Mgr.Get(m.From.ID)
	if sess == nil {
		b.reply(m.Chat.ID, "Aktiv sessiya yo'q.")
		return
	}
	hexStr := strings.ReplaceAll(strings.TrimSpace(m.CommandArguments()), " ", "")
	if hexStr == "" {
		b.reply(m.Chat.ID, "Ishlatish: /raw 1b5b41  (= Up arrow)")
		return
	}
	if len(hexStr)%2 != 0 {
		b.reply(m.Chat.ID, "Hex satr juft uzunlikda bo'lishi kerak.")
		return
	}
	bytes := make([]byte, len(hexStr)/2)
	for i := 0; i < len(bytes); i++ {
		v, err := strconv.ParseUint(hexStr[i*2:i*2+2], 16, 8)
		if err != nil {
			b.reply(m.Chat.ID, "Yaroqsiz hex: "+err.Error())
			return
		}
		bytes[i] = byte(v)
	}
	go sess.SendKey(string(bytes))
}

// runShortcut shortcut'ni aktiv sessiyada bajaradi.
func (b *Bot) runShortcut(m *tgbotapi.Message, shellCmd string) {
	sess := b.Mgr.Get(m.From.ID)
	if sess == nil {
		b.reply(m.Chat.ID, "Aktiv sessiya yo'q. /servers ro'yxatdan tanlang.")
		return
	}
	go sess.RunCommand(shellCmd)
}

// --- Yordamchi ---

func (b *Bot) connectByID(tgID, chatID int64, serverID uint) {
	user, ok := b.lookupUser(tgID)
	if !ok {
		b.reply(chatID, "Avval /start orqali bog'laning.")
		return
	}
	var server models.Server
	err := db.DB.Where("id = ? AND user_id = ?", serverID, user.ID).First(&server).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		b.reply(chatID, "Server topilmadi yoki sizga tegishli emas.")
		return
	}
	if err != nil {
		b.reply(chatID, "DB xato: "+err.Error())
		return
	}

	if _, err := b.Mgr.Open(b.rootContext, tgID, chatID, server); err != nil {
		b.reply(chatID, "Ulanish xato: "+err.Error())
	}
}

func (b *Bot) lookupUser(tgID int64) (models.User, bool) {
	var link models.TelegramUser
	if err := db.DB.Where("telegram_id = ?", tgID).First(&link).Error; err != nil {
		return models.User{}, false
	}
	var user models.User
	if err := db.DB.First(&user, link.UserID).Error; err != nil {
		return models.User{}, false
	}
	return user, true
}

func (b *Bot) reply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	if _, err := b.API.Send(msg); err != nil {
		log.Printf("reply: %v", err)
	}
}
