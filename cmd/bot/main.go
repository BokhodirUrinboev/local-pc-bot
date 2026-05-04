package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"remofy-bot/internal/auth"
	"remofy-bot/internal/bot"
	"remofy-bot/internal/crypto"
	"remofy-bot/internal/db"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found (using process env)")
	}

	token := mustEnv("TELEGRAM_BOT_TOKEN")
	publicURL := mustEnv("PUBLIC_BASE_URL")
	webAppURL := os.Getenv("WEB_APP_URL") // bo'sh bo'lsa Mini App o'chiriladi
	httpPort := os.Getenv("BOT_HTTP_PORT")
	if httpPort == "" {
		httpPort = "8090"
	}

	idleMin, _ := strconv.Atoi(os.Getenv("SESSION_IDLE_MINUTES"))
	idleTimeout := time.Duration(idleMin) * time.Minute

	db.Init()
	crypto.Init()

	api, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Fatalf("Failed to init Telegram bot: %v", err)
	}
	log.Printf("Authorized on bot: @%s", api.Self.UserName)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	mgr := bot.NewManager(api, idleTimeout)
	b := bot.New(api, mgr, publicURL, webAppURL, rootCtx)

	auth.Init(b.OnLinked)
	auth.StartGC()

	// Mini App (web_app) menu button — har bir foydalanuvchi uchun input maydoni yonida ko'rinadi.
	// v5.5.1 da WebApp tipi yo'q, shuning uchun raw API call.
	if webAppURL != "" {
		if err := setWebAppMenuButton(api, webAppURL, "Remofy"); err != nil {
			log.Printf("setChatMenuButton failed: %v", err)
		} else {
			log.Printf("Menu button set to web_app: %s", webAppURL)
		}
	}

	// HTTP server (OAuth callback)
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/google/login", auth.HandleLogin)
	mux.HandleFunc("/auth/google/callback", auth.HandleCallback)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	httpSrv := &http.Server{
		Addr:              ":" + httpPort,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("HTTP server listening on :%s", httpPort)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Bot long polling
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
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = httpSrv.Shutdown(shutdownCtx)
			cancel()
			rootCancel()
			return
		}
	}
}

// setWebAppMenuButton bot uchun default menu button'ni "web_app" turiga o'rnatadi.
// Bu Telegram Bot API 6.0+ method'i — kutubxona uni helper sifatida bermagani uchun
// MakeRequest orqali to'g'ridan-to'g'ri chaqiramiz.
func setWebAppMenuButton(api *tgbotapi.BotAPI, url, text string) error {
	menu, err := json.Marshal(map[string]any{
		"type":    "web_app",
		"text":    text,
		"web_app": map[string]string{"url": url},
	})
	if err != nil {
		return err
	}
	params := tgbotapi.Params{"menu_button": string(menu)}
	_, err = api.MakeRequest("setChatMenuButton", params)
	return err
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}
