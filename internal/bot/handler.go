package bot

import (
	"context"
	"fmt"
	"log"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Bot struct {
	API     *tgbotapi.BotAPI
	Mgr     *Manager
	rootCtx context.Context

	allowedUsers map[int64]struct{} // user-level whitelist (private + group ichida)
	allowedChats map[int64]struct{} // group/supergroup chat ID whitelist

	selfID       int64
	selfUsername string
}

func New(api *tgbotapi.BotAPI, mgr *Manager, allowedUsers, allowedChats []int64, ctx context.Context) *Bot {
	users := make(map[int64]struct{}, len(allowedUsers))
	for _, id := range allowedUsers {
		users[id] = struct{}{}
	}
	chats := make(map[int64]struct{}, len(allowedChats))
	for _, id := range allowedChats {
		chats[id] = struct{}{}
	}
	return &Bot{
		API:          api,
		Mgr:          mgr,
		rootCtx:      ctx,
		allowedUsers: users,
		allowedChats: chats,
		selfID:       api.Self.ID,
		selfUsername: api.Self.UserName,
	}
}

const btnStop = "⏹ Stop"

// BotCommands — Telegram menyusi uchun.
func BotCommands() []tgbotapi.BotCommand {
	return []tgbotapi.BotCommand{
		{Command: "start", Description: "Boshlash / yordam"},
		{Command: "help", Description: "Yordam"},
		{Command: "stop", Description: "Aktiv promptni to'xtatish"},
		{Command: "reset", Description: "Suhbat tarixini tozalash"},
		{Command: "workdir", Description: "Joriy ish papkasi"},
	}
}

func mainKeyboard() tgbotapi.ReplyKeyboardMarkup {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton(btnStop),
		),
	)
	kb.ResizeKeyboard = true
	return kb
}

// HandleUpdate har bir Telegram yangilanishini qabul qiladi.
func (b *Bot) HandleUpdate(u tgbotapi.Update) {
	if u.Message == nil || u.Message.From == nil {
		return
	}
	m := u.Message
	fromID := m.From.ID
	chatID := m.Chat.ID
	isGroup := m.Chat.IsGroup() || m.Chat.IsSuperGroup()

	// Gruppada — chat-level whitelist tekshiruvi
	if isGroup && !b.isChatAllowed(chatID) {
		// Jim — log qilamiz, lekin javob bermaymiz (begona gruppa).
		log.Printf("denied (chat not in whitelist): chat=%d user=%d", chatID, fromID)
		return
	}
	// User-level whitelist (private va gruppa ichida ham)
	if !b.isUserAllowed(fromID) {
		log.Printf("denied (user not in whitelist): chat=%d user=%d", chatID, fromID)
		// Private chatda ogohlantiramiz; gruppada esa jim turamiz (begona shovqin yo'q).
		if !isGroup {
			text := fmt.Sprintf("⛔ Sizga ushbu botdan foydalanishga ruxsat berilmagan.\n\nTelegram ID: <code>%d</code>", fromID)
			msg := tgbotapi.NewMessage(chatID, text)
			msg.ParseMode = tgbotapi.ModeHTML
			_, _ = b.API.Send(msg)
		}
		return
	}

	sess := b.Mgr.Get(chatID)

	// Slash komandalar har doim ishlaydi (gruppada ham)
	if m.IsCommand() {
		b.handleCommand(sess, m)
		return
	}

	text := m.Text
	if text == "" {
		// Caption bilan kelgan rasm/fayl yoki bo'sh xabar — hozircha o'tkazib yuboramiz.
		return
	}

	// Stop tugmasi
	if text == btnStop {
		sess.SendInterrupt()
		return
	}

	// Gruppada faqat mention yoki replyga javob beramiz
	if isGroup {
		mentioned := b.isMentioned(m)
		repliedToBot := m.ReplyToMessage != nil && m.ReplyToMessage.From != nil && m.ReplyToMessage.From.ID == b.selfID
		if !mentioned && !repliedToBot {
			return
		}
		// @mention substring'ini olib tashlaymiz
		if mentioned {
			text = stripBotMention(text, b.selfUsername)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
	}

	go sess.RunClaude(text)
}

func (b *Bot) handleCommand(sess *Session, m *tgbotapi.Message) {
	switch m.Command() {
	case "start", "help":
		b.cmdHelp(sess)
	case "stop":
		sess.SendInterrupt()
		b.reply(sess, "⏹ Stop signal yuborildi.")
	case "reset":
		sess.Reset()
		b.reply(sess, "🧹 Suhbat tarixi tozalandi. Yangi sessiya boshlanadi.")
	case "workdir":
		b.reply(sess, "📂 <code>"+htmlEscape(b.Mgr.Workdir())+"</code>")
	default:
		// Gruppada noma'lum komandaga javob bermaymiz (boshqa botniki bo'lishi mumkin).
		if !(m.Chat.IsGroup() || m.Chat.IsSuperGroup()) {
			b.reply(sess, "Noma'lum komanda. /help")
		}
	}
}

func (b *Bot) isUserAllowed(tgID int64) bool {
	if len(b.allowedUsers) == 0 {
		return true
	}
	_, ok := b.allowedUsers[tgID]
	return ok
}

// isChatAllowed gruppa chat ID'si whitelist'da ekanligini tekshiradi.
// Bo'sh ALLOWED_CHAT_IDS — bot gruppalarda umuman javob bermaydi (xavfsiz default).
func (b *Bot) isChatAllowed(chatID int64) bool {
	if len(b.allowedChats) == 0 {
		return false
	}
	_, ok := b.allowedChats[chatID]
	return ok
}

// isMentioned xabarda @botname yoki text_mention orqali bot tilga olinganmi.
func (b *Bot) isMentioned(m *tgbotapi.Message) bool {
	if b.selfUsername == "" && b.selfID == 0 {
		return false
	}
	mentionLower := "@" + strings.ToLower(b.selfUsername)
	if b.selfUsername != "" && strings.Contains(strings.ToLower(m.Text), mentionLower) {
		return true
	}
	for _, ent := range m.Entities {
		if ent.Type == "text_mention" && ent.User != nil && ent.User.ID == b.selfID {
			return true
		}
	}
	return false
}

// stripBotMention matndan @username substring'larini case-insensitive tarzda olib tashlaydi.
func stripBotMention(text, username string) string {
	if username == "" {
		return text
	}
	mention := "@" + username
	lower := strings.ToLower(text)
	lowerMention := strings.ToLower(mention)
	for {
		idx := strings.Index(lower, lowerMention)
		if idx < 0 {
			break
		}
		text = text[:idx] + text[idx+len(mention):]
		lower = lower[:idx] + lower[idx+len(mention):]
	}
	return text
}

func (b *Bot) cmdHelp(s *Session) {
	text := `<b>Remofy bot</b> — Claude AI agent shu kompyuterda.

Yozgan har qanday matn Claude'ga prompt sifatida yuboriladi va agent kerakli toollar (Read, Edit, Write, Bash, GitHub MCP) bilan ishlaydi.

<b>Foydalanish:</b>
• Private chat — har qanday matn → Claude
• Gruppa — botni @mention qiling yoki javobiga reply yozing
• Long-running promptni <b>⏹ Stop</b> tugmasi yoki /stop bilan to'xtatish

<b>Komandalar:</b>
/stop — aktiv promptni uzish
/reset — suhbat tarixini tozalash (yangi sessiya)
/workdir — agent ishlayotgan papka
/help — shu yordam

<b>Workspace:</b> <code>` + htmlEscape(b.Mgr.Workdir()) + `</code>`

	msg := tgbotapi.NewMessage(s.ChatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	// Reply keyboard faqat private chatda ko'rsatamiz — gruppada hammaga tushib ketmasin.
	if s.ChatID > 0 {
		msg.ReplyMarkup = mainKeyboard()
	}
	_, _ = b.API.Send(msg)
}

func (b *Bot) reply(s *Session, text string) {
	msg := tgbotapi.NewMessage(s.ChatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	if _, err := b.API.Send(msg); err != nil {
		log.Printf("reply (chat=%d): %v", s.ChatID, err)
	}
}
