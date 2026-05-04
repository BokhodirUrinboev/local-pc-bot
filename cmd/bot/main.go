package main

import (
	"context"
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
	b := bot.New(api, mgr, publicURL, rootCtx)

	auth.Init(b.OnLinked)
	auth.StartGC()

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

	// Graceful shutdown
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

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Fatalf("%s is required", key)
	}
	return v
}
