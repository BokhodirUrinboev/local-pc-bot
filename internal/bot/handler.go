package bot

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Reply qilingan xabardagi fayl mazmunini cheklaymiz — Telegram messageda 4096 cap,
// lekin Claude promptiga ko'proq sig'adi. Mantiqiy chegara — 256KB.
const maxReplyDocBytes = 256 * 1024

type Bot struct {
	API     *tgbotapi.BotAPI
	Mgr     *Manager
	rootCtx context.Context

	allowedUsers  map[int64]struct{} // user-level whitelist (private + group ichida)
	allowedChats  map[int64]struct{} // group/supergroup chat ID whitelist
	allowAllUsers bool               // BOT_ALLOW_ALL_USERS — whitelist'ni butunlay ochadi (xavfli)

	auditPath string // BOT_AUDIT_LOG — bo'sh bo'lmasa, har bir exec shu faylga yoziladi
	auditMu   sync.Mutex

	selfID       int64
	selfUsername string
}

func New(api *tgbotapi.BotAPI, mgr *Manager, allowedUsers, allowedChats []int64, allowAllUsers bool, auditPath string, ctx context.Context) *Bot {
	users := make(map[int64]struct{}, len(allowedUsers))
	for _, id := range allowedUsers {
		users[id] = struct{}{}
	}
	chats := make(map[int64]struct{}, len(allowedChats))
	for _, id := range allowedChats {
		chats[id] = struct{}{}
	}
	return &Bot{
		API:           api,
		Mgr:           mgr,
		rootCtx:       ctx,
		allowedUsers:  users,
		allowedChats:  chats,
		allowAllUsers: allowAllUsers,
		auditPath:     auditPath,
		selfID:        api.Self.ID,
		selfUsername:  api.Self.UserName,
	}
}

const btnStop = "⏹ Stop"

// BotCommands — Telegram menyusi uchun.
func BotCommands() []tgbotapi.BotCommand {
	return []tgbotapi.BotCommand{
		{Command: "start", Description: "Boshlash / yordam"},
		{Command: "help", Description: "Yordam"},
		{Command: "powershell", Description: shellLabel() + " rejimi (default)"},
		{Command: "claude", Description: "Claude AI rejimi"},
		{Command: "stop", Description: "Aktiv komandani to'xtatish"},
		{Command: "reset", Description: "Suhbat tarixini tozalash"},
		{Command: "workdir", Description: "Joriy ish papkasi"},
		{Command: "cd", Description: "Ish papkasini o'zgartirish (/cd <yo'l>)"},
		{Command: "get", Description: "Fayl yuborish (/get <yo'l>)"},
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
// threadID — agar xabar forum (topic) ichida bo'lsa, message_thread_id; aks holda 0.
func (b *Bot) HandleUpdate(p Polled) {
	u := p.Update
	threadID := p.MessageThreadID
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
			_, _ = SendInThread(b.API, chatID, threadID, text, tgbotapi.ModeHTML, nil)
		}
		return
	}

	sess := b.Mgr.Get(chatID)

	// Slash komandalar har doim ishlaydi (gruppada ham)
	if m.IsCommand() {
		b.handleCommand(sess, m, threadID)
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

	// Reply qilingan xabar bo'lsa — uning matni/captioni va biriktirilgan
	// fayli (masalan .log) prompt/komandaga kontekst sifatida qo'shiladi.
	if rc := b.buildReplyContext(m.ReplyToMessage); rc != "" {
		text = rc + "\n\n" + text
	}

	switch sess.Mode() {
	case ModeClaude:
		b.audit(m, "claude", text)
		go sess.RunClaude(text, threadID)
	default:
		b.audit(m, "shell", text)
		go sess.RunShell(text, threadID)
	}
}

// buildReplyContext reply qilingan xabardan kontekst yig'adi:
// matn/caption + agar Document biriktirilgan bo'lsa — uning mazmuni.
// Hech narsa bo'lmasa "" qaytaradi.
func (b *Bot) buildReplyContext(r *tgbotapi.Message) string {
	if r == nil {
		return ""
	}
	body := r.Text
	if body == "" {
		body = r.Caption
	}

	var doc string
	if r.Document != nil {
		content, err := b.downloadFile(r.Document.FileID, maxReplyDocBytes)
		if err != nil {
			log.Printf("reply doc download (file=%s): %v", r.Document.FileName, err)
		} else if content != "" {
			doc = fmt.Sprintf("<reply_fayl nomi=%q>\n%s\n</reply_fayl>", r.Document.FileName, content)
		}
	}

	if body == "" && doc == "" {
		return ""
	}

	var parts []string
	parts = append(parts, "<reply_xabar>")
	if body != "" {
		parts = append(parts, body)
	}
	if doc != "" {
		parts = append(parts, doc)
	}
	parts = append(parts, "</reply_xabar>")
	return strings.Join(parts, "\n")
}

// downloadFile Telegram file_id orqali faylni yuklab oladi (maxBytes bilan cheklangan).
func (b *Bot) downloadFile(fileID string, maxBytes int) (string, error) {
	f, err := b.API.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", err
	}
	url := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.API.Token, f.FilePath)
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxBytes {
		return string(data[:maxBytes]) + "\n…(qisqartirildi)", nil
	}
	return string(data), nil
}

func (b *Bot) handleCommand(sess *Session, m *tgbotapi.Message, threadID int) {
	switch m.Command() {
	case "start", "help":
		b.cmdHelp(sess, threadID)
	case "stop":
		sess.SendInterrupt()
		b.reply(sess, threadID, "⏹ Stop signal yuborildi.")
	case "reset":
		sess.Reset()
		b.reply(sess, threadID, "🧹 Suhbat tarixi tozalandi. Yangi sessiya boshlanadi.")
	case "workdir":
		b.reply(sess, threadID, "📂 <code>"+htmlEscape(sess.Cwd())+"</code>")
	case "cd":
		b.cmdCd(sess, m, threadID)
	case "get":
		b.cmdGet(sess, m, threadID)
	case "claude":
		arg := strings.TrimSpace(m.CommandArguments())
		if arg != "" {
			// Bir martalik — mode o'zgarmaydi.
			b.audit(m, "claude", arg)
			go sess.RunClaude(arg, threadID)
			return
		}
		sess.SetMode(ModeClaude)
		b.reply(sess, threadID, "🤖 Claude AI rejimi yoqildi. Shell ga qaytish: /powershell")
	case "powershell", "shell":
		arg := strings.TrimSpace(m.CommandArguments())
		if arg != "" {
			b.audit(m, "shell", arg)
			go sess.RunShell(arg, threadID)
			return
		}
		sess.SetMode(ModePowerShell)
		b.reply(sess, threadID, "🟦 "+shellLabel()+" rejimi yoqildi. Claude ga o'tish: /claude")
	default:
		// Gruppada noma'lum komandaga javob bermaymiz (boshqa botniki bo'lishi mumkin).
		if !(m.Chat.IsGroup() || m.Chat.IsSuperGroup()) {
			b.reply(sess, threadID, "Noma'lum komanda. /help")
		}
	}
}

func (b *Bot) isUserAllowed(tgID int64) bool {
	// Fail-closed: bo'sh whitelist'da bot ishga tushmaydi (main.go), shuning uchun
	// bu yerda len==0 → ochiq mantig'i YO'Q. Faqat aniq BOT_ALLOW_ALL_USERS bilan ochiladi.
	if b.allowAllUsers {
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

func (b *Bot) cmdHelp(s *Session, threadID int) {
	shell := shellLabel()
	modeLine := "🟦 <b>" + shell + "</b> (default)"
	if s.Mode() == ModeClaude {
		modeLine = "🤖 <b>Claude AI</b>"
	}
	text := `<b>Remofy bot</b> — shu kompyuter uchun masofaviy terminal.

<b>Joriy rejim:</b> ` + modeLine + `

Free-text xabarlar joriy rejimga ko'ra ishlatiladi:
• 🟦 ` + shell + ` — matn shu kompyuterda ` + shell + ` komanda sifatida bajariladi
• 🤖 Claude — matn Claude AI agentga prompt sifatida yuboriladi (Read, Edit, Write, Bash, GitHub MCP)

<b>Rejim almashtirish:</b>
/powershell — ` + shell + ` rejimiga o'tish
/claude — Claude rejimiga o'tish
/powershell <i>komanda</i> — bir martalik ` + shell + ` (rejim o'zgarmaydi)
/claude <i>savol</i> — bir martalik prompt (rejim o'zgarmaydi)

<b>Boshqa komandalar:</b>
/stop — aktiv komandani uzish
/reset — Claude suhbat tarixini tozalash
/workdir — joriy ish papkasi
/cd <i>yo'l</i> — ish papkasini o'zgartirish (cd komandalar orasida saqlanadi)
/get <i>yo'l</i> — kompyuterdan fayl yuborish
/help — shu yordam

<b>Gruppada:</b> botni @mention qiling yoki javobiga reply yozing.

<b>Ish papkasi:</b> <code>` + htmlEscape(s.Cwd()) + `</code>`

	// Reply keyboard faqat private chatda ko'rsatamiz — gruppada hammaga tushib ketmasin.
	var rm interface{}
	if s.ChatID > 0 {
		rm = mainKeyboard()
	}
	if _, err := SendInThread(b.API, s.ChatID, threadID, text, tgbotapi.ModeHTML, rm); err != nil {
		log.Printf("cmdHelp (chat=%d): %v", s.ChatID, err)
	}
}

// maxGetBytes — Telegram sendDocument (bot API) limiti.
const maxGetBytes = 50 * 1024 * 1024

// cmdCd shell rejimining ish papkasini o'zgartiradi (yo'l joriy cwd'ga nisbiy bo'lishi mumkin).
// Argumentsiz — joriy papkani ko'rsatadi.
func (b *Bot) cmdCd(sess *Session, m *tgbotapi.Message, threadID int) {
	arg := strings.TrimSpace(m.CommandArguments())
	if arg == "" {
		b.reply(sess, threadID, "📂 <code>"+htmlEscape(sess.Cwd())+"</code>")
		return
	}
	target := arg
	if !filepath.IsAbs(target) {
		target = filepath.Join(sess.Cwd(), target)
	}
	target = filepath.Clean(target)
	if st, err := os.Stat(target); err != nil || !st.IsDir() {
		b.reply(sess, threadID, "⚠️ Papka topilmadi: <code>"+htmlEscape(arg)+"</code>")
		return
	}
	sess.SetCwd(target)
	b.reply(sess, threadID, "📂 <code>"+htmlEscape(target)+"</code>")
}

// cmdGet kompyuterdagi faylni Telegram document sifatida yuboradi (yo'l cwd'ga nisbiy bo'lishi mumkin).
func (b *Bot) cmdGet(sess *Session, m *tgbotapi.Message, threadID int) {
	arg := strings.TrimSpace(m.CommandArguments())
	if arg == "" {
		b.reply(sess, threadID, "Foydalanish: <code>/get &lt;fayl yo'li&gt;</code>")
		return
	}
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(sess.Cwd(), path)
	}
	path = filepath.Clean(path)
	st, err := os.Stat(path)
	if err != nil {
		b.reply(sess, threadID, "⚠️ Fayl topilmadi: <code>"+htmlEscape(arg)+"</code>")
		return
	}
	if st.IsDir() {
		b.reply(sess, threadID, "⚠️ Bu papka, fayl emas: <code>"+htmlEscape(arg)+"</code>")
		return
	}
	if st.Size() > maxGetBytes {
		b.reply(sess, threadID, fmt.Sprintf("⚠️ Fayl juda katta (%d MB). Telegram limiti — 50 MB.", st.Size()/1024/1024))
		return
	}
	doc := tgbotapi.NewDocument(sess.ChatID, tgbotapi.FilePath(path))
	doc.ReplyToMessageID = m.MessageID // topic/forum ichida to'g'ri joyga tushishi uchun
	if _, err := b.API.Send(doc); err != nil {
		log.Printf("get send (chat=%d, path=%s): %v", sess.ChatID, path, err)
		b.reply(sess, threadID, "⚠️ Yuborib bo'lmadi: "+htmlEscape(err.Error()))
	}
}

func (b *Bot) reply(s *Session, threadID int, text string) {
	if _, err := SendInThread(b.API, s.ChatID, threadID, text, tgbotapi.ModeHTML, nil); err != nil {
		log.Printf("reply (chat=%d): %v", s.ChatID, err)
	}
}
