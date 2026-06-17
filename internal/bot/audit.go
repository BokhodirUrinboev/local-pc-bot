package bot

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// auditMaxRunes — audit qatorida saqlanadigan komanda/prompt matnining maksimal uzunligi.
const auditMaxRunes = 1000

// audit har bir bajariladigan komanda/promptni yozib qo'yadi: kim, qaysi chat,
// qaysi rejim va matn (qisqartirilgan). Har doim stdlib log orqali (journald/err.log
// tomonidan ushlanadi), va BOT_AUDIT_LOG o'rnatilgan bo'lsa — alohida faylga ham.
func (b *Bot) audit(m *tgbotapi.Message, mode, text string) {
	var uid int64
	var user string
	if m.From != nil {
		uid = m.From.ID
		user = m.From.UserName
	}

	oneLine := strings.ReplaceAll(text, "\r", " ")
	oneLine = strings.ReplaceAll(oneLine, "\n", " ")
	if r := []rune(oneLine); len(r) > auditMaxRunes {
		oneLine = string(r[:auditMaxRunes]) + "…"
	}

	line := fmt.Sprintf("%s user=%d(@%s) chat=%d mode=%s | %s",
		time.Now().Format(time.RFC3339), uid, user, m.Chat.ID, mode, oneLine)
	log.Printf("AUDIT %s", line)

	if b.auditPath == "" {
		return
	}
	b.auditMu.Lock()
	defer b.auditMu.Unlock()
	f, err := os.OpenFile(b.auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("audit open (%s): %v", b.auditPath, err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		log.Printf("audit write: %v", err)
	}
}
