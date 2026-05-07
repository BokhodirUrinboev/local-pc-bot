package bot

import (
	"context"
	"fmt"
	"log"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Bot struct {
	API        *tgbotapi.BotAPI
	Mgr        *Manager
	rootCtx    context.Context
	allowedIDs map[int64]struct{} // bo'sh bo'lsa — hamma uchun ochiq
}

func New(api *tgbotapi.BotAPI, mgr *Manager, allowedIDs []int64, ctx context.Context) *Bot {
	allow := make(map[int64]struct{}, len(allowedIDs))
	for _, id := range allowedIDs {
		allow[id] = struct{}{}
	}
	return &Bot{API: api, Mgr: mgr, rootCtx: ctx, allowedIDs: allow}
}

func (b *Bot) isAllowed(tgID int64) bool {
	if len(b.allowedIDs) == 0 {
		return true
	}
	_, ok := b.allowedIDs[tgID]
	return ok
}

const btnStop = "⏹ Stop"

// BotCommands — Telegram menyusi uchun.
func BotCommands() []tgbotapi.BotCommand {
	return []tgbotapi.BotCommand{
		{Command: "start", Description: "Boshlash / yordam"},
		{Command: "help", Description: "Yordam"},
		{Command: "pwd", Description: "Joriy papka"},
		{Command: "cd", Description: "Papkani o'zgartirish"},
		{Command: "stop", Description: "Aktiv komandani to'xtatish"},
		{Command: "claude", Description: "Claude rejimi"},
		{Command: "endclaude", Description: "Claude rejimidan chiqish"},
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
	// Faqat Message — callback'lar yo'q endi
	if u.Message == nil || u.Message.From == nil {
		return
	}
	m := u.Message
	fromID := m.From.ID
	chatID := m.Chat.ID

	// Whitelist gate
	if !b.isAllowed(fromID) {
		log.Printf("denied (not in whitelist): tg_id=%d", fromID)
		text := fmt.Sprintf("⛔ Sizga ushbu botdan foydalanishga ruxsat berilmagan.\n\nTelegram ID: <code>%d</code>", fromID)
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = tgbotapi.ModeHTML
		_, _ = b.API.Send(msg)
		return
	}

	sess := b.Mgr.Get(fromID, chatID)

	if m.IsCommand() {
		switch m.Command() {
		case "start", "help":
			b.cmdHelp(sess)
		case "pwd":
			b.reply(sess, "📂 <code>"+htmlEscape(sess.Cwd())+"</code>")
		case "cd":
			args := strings.TrimSpace(m.CommandArguments())
			if args == "" {
				b.reply(sess, "📂 <code>"+htmlEscape(sess.Cwd())+"</code>\n<i>Ishlatish: /cd &lt;path&gt;</i>")
				return
			}
			go sess.RunCommand("Set-Location -LiteralPath " + psQuote(args))
		case "stop":
			sess.SendInterrupt()
			b.reply(sess, "⏹ Stop signal yuborildi.")
		case "claude":
			args := strings.TrimSpace(m.CommandArguments())
			if !sess.IsClaudeMode() {
				sess.EnterClaudeMode()
			}
			if args != "" {
				go sess.RunClaude(args)
			}
		case "endclaude":
			sess.ExitClaudeMode()
		default:
			b.reply(sess, "Noma'lum komanda. /help")
		}
		return
	}

	text := m.Text
	if text == btnStop {
		sess.SendInterrupt()
		return
	}

	// Claude rejimi: matn → prompt
	if sess.IsClaudeMode() {
		go sess.RunClaude(text)
		return
	}

	// Oddiy PowerShell komandasi
	go sess.RunCommand(text)
}

func (b *Bot) cmdHelp(s *Session) {
	text := `<b>Remofy bot</b> — shu kompyuterda terminal va Claude.

<b>Foydalanish:</b>
• Har qanday matn → PowerShell komandasi
• <code>cd ...</code> ishlatsangiz — papka holati saqlanadi (keyingi komandalar shu papkadan)
• Long-running komandani <b>⏹ Stop</b> tugma orqali to'xtatish mumkin

<b>Komandalar:</b>
/pwd — joriy papka
/cd &lt;path&gt; — papkani o'zgartirish
/stop — aktiv komandani uzish
/claude — Claude rejimini yoqish (matn → prompt)
/claude &lt;savol&gt; — bitta promptni darhol yuborish
/endclaude — Claude rejimidan chiqish

<b>Joriy papka:</b> <code>` + htmlEscape(s.Cwd()) + `</code>`

	msg := tgbotapi.NewMessage(s.ChatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	msg.ReplyMarkup = mainKeyboard()
	_, _ = b.API.Send(msg)
}

func (b *Bot) reply(s *Session, text string) {
	msg := tgbotapi.NewMessage(s.ChatID, text)
	msg.ParseMode = tgbotapi.ModeHTML
	if _, err := b.API.Send(msg); err != nil {
		log.Printf("reply (tg=%d): %v", s.TelegramID, err)
	}
}
