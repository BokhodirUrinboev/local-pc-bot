package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"remofy-bot/internal/bot"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file (process env'dan foydalanyapmiz)")
	}

	token := mustEnv("TELEGRAM_BOT_TOKEN")
	workdir := strings.TrimSpace(os.Getenv("BOT_WORKDIR"))
	if workdir == "" {
		workdir = `C:\Users\nbkab\OneDrive\Ishchi stol`
	}

	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("Telegram init: %v", err)
	}
	log.Printf("Bot: @%s (id=%d)", api.Self.UserName, api.Self.ID)
	log.Printf("Workdir: %s", workdir)

	allowedUsers := parseIDList(os.Getenv("ALLOWED_TELEGRAM_IDS"), "ALLOWED_TELEGRAM_IDS")
	allowedChats := parseIDList(os.Getenv("ALLOWED_CHAT_IDS"), "ALLOWED_CHAT_IDS")

	if len(allowedUsers) == 0 {
		log.Println("WARNING: ALLOWED_TELEGRAM_IDS bo'sh — bot HAMMA private foydalanuvchi uchun ochiq!")
	} else {
		log.Printf("User whitelist (%d): %v", len(allowedUsers), allowedUsers)
	}
	if len(allowedChats) == 0 {
		log.Println("INFO: ALLOWED_CHAT_IDS bo'sh — bot gruppalarda umuman javob bermaydi.")
	} else {
		log.Printf("Chat whitelist (%d): %v", len(allowedChats), allowedChats)
	}

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
	b := bot.New(api, mgr, allowedUsers, allowedChats, rootCtx)

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
