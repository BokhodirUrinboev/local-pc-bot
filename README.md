# Remofy Telegram Bot

[Remofy web-ssh](../web-ssh-backend) ning Telegram bot versiyasi. Foydalanuvchi brauzersiz, to'g'ridan-to'g'ri Telegram orqali o'z serverlariga ulanib komandalar bajara oladi.

Bot mavjud Remofy ma'lumotlar bazasini (PostgreSQL) o'qiydi — foydalanuvchi web-ssh saytida qo'shgan serverlar avtomatik ko'rinadi. O'zining `telegram_users` jadvalini qo'shadi (Telegram ↔ web-ssh foydalanuvchi bog'lanishi).

## Xususiyatlar

- **Google OAuth bog'lanish:** `/start` → link bosib Google'da kirish → Telegram ID web-ssh foydalanuvchisi bilan bog'lanadi
- **Davomiy SSH sessiyasi:** har bir komanda bir xil shellda bajariladi (`cd`, env, history saqlanadi)
- **Live output:** SSH chiqishi bitta xabarda yangilanib turadi (anchor message + edit-throttle)
- **Maxsus tugmalar:** Ctrl+C, Tab, Enter, ↑↓, Esc, Disconnect — inline keyboard orqali
- **Xom baytlar:** `/raw <hex>` — istalgan kontrol kodlarni yuborish
- **Auto-disconnect:** 30 daqiqa bo'sh tursa sessiya yopiladi
- **Keepalive:** SSH `keepalive@openssh.com` har 30 sek (uzoq sessiyalar uzilib qolmasligi uchun)

## Sozlash

### 1. `.env`

```bash
cp .env.example .env
```

To'ldirilishi kerak:

| Kalit | Tavsif |
|-------|--------|
| `DB_PATH` | Web-ssh-backend ishlatadigan **bir xil** Postgres DSN |
| `ENCRYPTION_KEY` | Web-ssh dagi **bir xil** 32-baytli kalit (server parollarini decrypt qilish uchun) |
| `TELEGRAM_BOT_TOKEN` | [@BotFather](https://t.me/BotFather) dan olingan token |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` | Google OAuth Client (yangi yoki mavjudini ishlatish mumkin) |
| `GOOGLE_REDIRECT_URL` | `https://<bot-host>/auth/google/callback` — Google Cloud konsolida ham ro'yxatdan o'tgan bo'lishi shart |
| `PUBLIC_BASE_URL` | Botning ommaviy URL'i (bot foydalanuvchiga login linkini shu asosda yuboradi) |
| `BOT_HTTP_PORT` | OAuth callback uchun port (default `8090`) |
| `SESSION_IDLE_MINUTES` | Sessiya idle timeout (default `30`) |

> ⚠️ `ENCRYPTION_KEY` web-ssh-backend dagi qiymat bilan **aynan bir xil** bo'lishi shart. Aks holda mavjud serverlarning parollari decrypt qilinmaydi.

### 2. Google OAuth

Google Cloud Console'da OAuth Client'ga **Authorized redirect URI** sifatida `${PUBLIC_BASE_URL}/auth/google/callback` qo'shing.

Web-ssh bilan bir xil clientdan foydalanish mumkin — faqat ikkita redirect URI bo'ladi (web-ssh va bot uchun).

## Ishga tushirish

### Lokal

```bash
go mod download
go run ./cmd/bot
```

`PUBLIC_BASE_URL` lokalda — Telegramdan callback uchun publik URL kerak. [ngrok](https://ngrok.com) yoki shunga o'xshash tunnel ishlating:

```bash
ngrok http 8090
# ngrok URL ni .env dagi PUBLIC_BASE_URL va GOOGLE_REDIRECT_URL ga yozing
```

### Docker

```bash
docker build -t remofy-bot .
docker run -d --env-file .env -p 8090:8090 remofy-bot
```

## Foydalanish

1. Telegramda botga `/start` yozing
2. "🔐 Google bilan kirish" tugmasini bosing → Google'da kirib chiqing
3. Botga qaytib `/servers` yozing — web-ssh dagi serverlar tugmalar shaklida ko'rinadi
4. Tugma bosing yoki `/connect <id>` — ulanish ochiladi
5. Komanda yozing — output anchor xabarda yangilanib turadi
6. `/disconnect` — sessiyani yopish

## Cheklovlar

- **Interaktiv TUI:** `vim`, `nano`, `htop` — ANSI cursor manipulyatsiyasi Telegramda to'g'ri ko'rinmaydi. Oddiy `cat`, `tail -f`, `top -bn1` ishlaydi.
- **Edit rate limit:** juda tez chiqadigan output (masalan `yes`) — Telegram rate limiti tufayli to'liq ko'rinmasligi mumkin (debounce bilan tushib qoladi).
- **Bir foydalanuvchi = bir sessiya:** yangi `/connect` eskisini yopadi.
- **In-memory store:** bot qayta ishga tushganda barcha sessiyalar yo'qoladi — qayta ulanish kerak.

## Arxitektura

```
remofy-bot/
├── cmd/bot/main.go              # Entry: bot polling + OAuth HTTP server
├── internal/
│   ├── bot/
│   │   ├── handler.go           # Update dispatcher + komandalar
│   │   ├── session.go           # Per-user SSH sessiya managerи
│   │   └── output.go            # Ring buffer, ANSI strip, HTML escape
│   ├── auth/
│   │   ├── oauth.go             # Google OAuth handlers
│   │   └── link.go              # State token → TelegramID linking
│   ├── db/db.go                 # GORM + TelegramUser AutoMigrate
│   ├── models/                  # User, Server, Folder (web-ssh sxemasi) + TelegramUser
│   ├── crypto/crypto.go         # AES-GCM (web-ssh bilan bir xil)
│   └── sshconn/connect.go       # SSH dial + PTY + keepalive
```
