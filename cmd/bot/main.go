package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"remofy-bot/internal/bot"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file (process env'dan foydalanyapmiz)")
	}

	token := mustEnv("TELEGRAM_BOT_TOKEN")
	// BOT_WORKDIR bo'sh bo'lsa — NewManager har platformada foydalanuvchi home
	// papkasiga tushadi (Windows: %USERPROFILE%, Unix: $HOME).
	workdir := strings.TrimSpace(os.Getenv("BOT_WORKDIR"))

	// Konfiguratsiyani tarmoqqa chiqishdan OLDIN tekshiramiz — noto'g'ri sozlamada
	// (ayniqsa bo'sh whitelist) Telegram'ga ulanmasdan darrov to'xtaymiz.
	allowedUsers := parseIDList(os.Getenv("ALLOWED_TELEGRAM_IDS"), "ALLOWED_TELEGRAM_IDS")
	allowedChats := parseIDList(os.Getenv("ALLOWED_CHAT_IDS"), "ALLOWED_CHAT_IDS")
	allowAllUsers := envBool("BOT_ALLOW_ALL_USERS")
	auditPath := strings.TrimSpace(os.Getenv("BOT_AUDIT_LOG"))

	// Fail-closed: bo'sh whitelist bilan bu — ochiq masofaviy shell. Faqat aniq
	// BOT_ALLOW_ALL_USERS=yes bilan ruxsat beramiz; aks holda ishga tushmaymiz.
	switch {
	case len(allowedUsers) > 0:
		log.Printf("User whitelist (%d): %v", len(allowedUsers), allowedUsers)
	case allowAllUsers:
		log.Println("WARNING: ALLOWED_TELEGRAM_IDS bo'sh, BOT_ALLOW_ALL_USERS yoqilgan — bot HAMMA private foydalanuvchi uchun OCHIQ (xavfli)!")
	default:
		log.Fatal("ALLOWED_TELEGRAM_IDS bo'sh — xavfsizlik uchun bot ishga tushmaydi. " +
			"Ruxsat etilgan Telegram ID(lar)ni kiriting, yoki (xavfli) BOT_ALLOW_ALL_USERS=yes qo'ying.")
	}
	if auditPath != "" {
		log.Printf("Audit log: %s", auditPath)
	}
	if len(allowedChats) == 0 {
		log.Println("INFO: ALLOWED_CHAT_IDS bo'sh — bot gruppalarda umuman javob bermaydi.")
	} else {
		log.Printf("Chat whitelist (%d): %v", len(allowedChats), allowedChats)
	}

	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("Telegram init: %v", err)
	}
	// Default HTTP client'da timeout yo'q — agar Telegram javob bermay turib qolsa,
	// edit/send chaqiruvi abadiy bloklanadi va RunClaude'ning cmdMu'sini band qiladi.
	// 60s — long-polling timeout (30s) ustidan xavfsiz qopla.
	api.Client = &http.Client{Timeout: 60 * time.Second}
	log.Printf("Bot: @%s (id=%d)", api.Self.UserName, api.Self.ID)

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	systemPrompt := strings.TrimSpace(os.Getenv("BOT_SYSTEM_PROMPT"))
	if systemPrompt != "" {
		preview := systemPrompt
		if len(preview) > 80 {
			preview = preview[:80] + "..."
		}
		log.Printf("System prompt: %s", preview)
	}

	mgr := bot.NewManager(api, workdir, systemPrompt)
	log.Printf("Workdir: %s", mgr.Workdir())
	b := bot.New(api, mgr, allowedUsers, allowedChats, allowAllUsers, auditPath, rootCtx)

	if _, err := api.Request(tgbotapi.NewSetMyCommands(bot.BotCommands()...)); err != nil {
		log.Printf("setMyCommands: %v", err)
	}
	if _, err := api.MakeRequest("setChatMenuButton", tgbotapi.Params{
		"menu_button": `{"type":"commands"}`,
	}); err != nil {
		log.Printf("setChatMenuButton: %v", err)
	}

	updates := bot.PollUpdates(rootCtx, api, 30)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	log.Println("Bot ishga tushdi. Ctrl+C — to'xtatish.")
	for {
		select {
		case p, ok := <-updates:
			if !ok {
				return
			}
			go b.HandleUpdate(p)
		case <-stop:
			log.Println("Shutdown signal — to'xtatilmoqda...")
			cancel()
			return
		}
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s required", key)
	}
	return v
}

// envBool — "1", "true", "yes", "on" (case-insensitive) → true.
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// parseIDList vergul/probel/nuqta-vergul bilan ajratilgan int64 ID ro'yxatini parslaydi.
func parseIDList(raw, label string) []int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	sep := func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\t' }
	out := make([]int64, 0, 4)
	for _, p := range strings.FieldsFunc(raw, sep) {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		id, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			log.Printf("%s: yaroqsiz '%s'", label, p)
			continue
		}
		out = append(out, id)
	}
	return out
}
