package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"remofy-bot/internal/db"
	"remofy-bot/internal/models"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

var googleOauth *oauth2.Config

// LinkedCallback bog'lash muvaffaqiyatli bo'lganda chaqiriladi (bot foydalanuvchini xabardor qilish uchun).
type LinkedCallback func(telegramID int64, user models.User)

var onLinked LinkedCallback

func Init(onLinkedCb LinkedCallback) {
	googleOauth = &oauth2.Config{
		RedirectURL:  os.Getenv("GOOGLE_REDIRECT_URL"),
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
		Scopes: []string{
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
		},
		Endpoint: google.Endpoint,
	}
	onLinked = onLinkedCb
}

// HandleLogin foydalanuvchini Google'ga yo'naltiradi. State avval bot tomonidan generatsiya qilingan.
func HandleLogin(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	if state == "" || !peekLinkToken(state) {
		http.Error(w, "Invalid or expired link. Botga qaytib /start yozing.", http.StatusBadRequest)
		return
	}
	url := googleOauth.AuthCodeURL(state, oauth2.SetAuthURLParam("prompt", "select_account"))
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

type googleUserInfo struct {
	ID      string `json:"id"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// HandleCallback Google dan code oladi, userni FirstOrCreate qiladi va TelegramUser yozadi.
func HandleCallback(w http.ResponseWriter, r *http.Request) {
	state := r.FormValue("state")
	tgID, tgUsername, ok := ConsumeLinkToken(state)
	if !ok {
		http.Error(w, "Link expired. /start ni qaytadan yozing.", http.StatusBadRequest)
		return
	}

	code := r.FormValue("code")
	tok, err := googleOauth.Exchange(context.Background(), code)
	if err != nil {
		http.Error(w, "OAuth exchange failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := http.Get("https://www.googleapis.com/oauth2/v2/userinfo?access_token=" + tok.AccessToken)
	if err != nil {
		http.Error(w, "Userinfo fetch failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	var info googleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		http.Error(w, "Userinfo decode failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	user := models.User{
		GoogleID:  info.ID,
		Email:     info.Email,
		Name:      info.Name,
		AvatarURL: info.Picture,
	}
	if err := db.DB.Where(models.User{GoogleID: user.GoogleID}).FirstOrCreate(&user).Error; err != nil {
		http.Error(w, "DB error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	link := models.TelegramUser{
		TelegramID: tgID,
		UserID:     user.ID,
		Username:   tgUsername,
		LinkedAt:   time.Now(),
	}
	// Upsert: TelegramID bo'yicha, mavjud bo'lsa UserID/Username yangilanadi
	if err := db.DB.Where(models.TelegramUser{TelegramID: tgID}).
		Assign(map[string]any{"user_id": user.ID, "username": tgUsername, "linked_at": time.Now()}).
		FirstOrCreate(&link).Error; err != nil {
		http.Error(w, "Link save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if onLinked != nil {
		go onLinked(tgID, user)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><body style="font-family:sans-serif;background:#1e1e1e;color:#fff;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;text-align:center"><div><h1>✅ Bog'lash muvaffaqiyatli</h1><p>Salom, %s!</p><p>Endi Telegram botiga qayting va <code>/servers</code> yozing.</p></div></body></html>`, info.Email)
}
