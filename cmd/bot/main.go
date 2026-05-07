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
	log.Printf("Bot: @%s", api.Self.UserName)

	allowedIDs := parseAllowedIDs(os.Getenv("ALLOWED_TELEGRAM_IDS"))
	if len(allowedIDs) == 0 {
		log.Println("WARNING: ALLOWED_TELEGRAM_IDS bo'sh — bot HAMMA uchun ochiq!")
	} else {
		log.Printf("Whitelist (%d): %v", len(allowedIDs), allowedIDs)
	}

	rootCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr := bot.NewManager(api, workdir)
	b := bot.New(api, mgr, allowedIDs, rootCtx)

	if _, err := api.Request(tgbotapi.NewSetMyCommands(bot.BotCommands()...)); err != nil {
		log.Printf("setMyCommands: %v", err)
	}
	if _, err := api.MakeRequest("setChatMenuButton", tgbotapi.Params{
		"menu_button": `{"type":"commands"}`,
	}); err != nil {
		log.Printf("setChatMenuButton: %v", err)
	}

	uCfg := tgbotapi.NewUpdate(0)
	uCfg.Timeout = 30
	updates := api.GetUpdatesChan(uCfg)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	log.Println("Bot ishga tushdi. Ctrl+C — to'xtatish.")
	for {
		select {
		case u := <-updates:
			go b.HandleUpdate(u)
		case <-stop:
			log.Println("Shutdown signal — to'xtatilmoqda...")
			api.StopReceivingUpdates()
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

// parseAllowedIDs vergul/probel bilan ajratilgan Telegram ID ro'yxatini parslaydi.
func parseAllowedIDs(raw string) []int64 {
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
			log.Printf("ALLOWED_TELEGRAM_IDS: yaroqsiz '%s'", p)
			continue
		}
		out = append(out, id)
	}
	return out
}
