# Remofy local bot

Shu kompyuterda ishlovchi Telegram bot. Whitelist'dagi foydalanuvchilarga
Telegram orqali to'g'ridan-to'g'ri PowerShell komandalari va Claude'ni ishlatishga ruxsat beradi.

## Xususiyatlar

- **Local PowerShell:** har qanday matn → `powershell.exe -Command ...` orqali bajariladi
- **Persistent CWD:** `cd` komandasi keyingi komandalar uchun ham saqlanadi
- **Live output:** uzoq ishlaydigan komandalar Telegram xabarini real vaqtda yangilaydi
- **Claude rejimi:** local `claude` CLI bilan strim qilinadigan suhbat (`--resume` orqali kontekst saqlanadi)
- **Whitelist:** faqat ruxsat etilgan Telegram ID'lar foydalana oladi (`ALLOWED_TELEGRAM_IDS`)
- **Stop tugmasi:** aktiv komanda yoki Claude promptini Telegramdan to'xtatish
- **Server sifatida:** Task Scheduler orqali boot'da avtomatik ishga tushadi, crash bo'lsa qayta ishga tushadi

## Sozlash (lokal)

```powershell
# 1. .env yarating
Copy-Item .env.example .env
notepad .env
# TELEGRAM_BOT_TOKEN va ALLOWED_TELEGRAM_IDS to'ldiring

# 2. Build
$env:GOOS='windows'; $env:GOARCH='amd64'
go build -ldflags='-s -w' -o '.dist\remofy-bot.exe' .\cmd\bot

# 3. Sinov uchun ishga tushirish
.\.dist\remofy-bot.exe
```

## Server sifatida o'rnatish (boot'da auto-start)

`scripts/install.ps1` Administrator PowerShell ostida ishlatiladi. Batafsil: [scripts/README.md](scripts/README.md).

```powershell
# Administrator PowerShell:
.\scripts\install.ps1
```

Bu quyidagilarni qiladi:
- `C:\ProgramData\remofy-bot\` ga fayllarni o'rnatadi
- AC quvvatda hech qachon uxlamasin deb power planni o'zgartiradi
- LocalSystem ostida `RemofyBot` Task Scheduler taskini yaratadi (boot'da auto-start, crash → restart)
- Botni darhol ishga tushiradi

## Komandalar

| Komanda | Tavsif |
|---------|--------|
| `/start`, `/help` | Yordam + joriy CWD |
| `/pwd` | Joriy ish papkasi |
| `/cd <path>` | Papkani o'zgartirish (state saqlanadi) |
| `/stop` | Aktiv komandani uzish (Ctrl+C analog) |
| `/claude` | Claude rejimini yoqish |
| `/claude <savol>` | Bir martalik prompt yuborish |
| `/endclaude` | Claude rejimidan chiqish |
| (boshqa matn) | PowerShell komandasi sifatida bajariladi |

## Konfiguratsiya

| Env var | Tavsif |
|---------|--------|
| `TELEGRAM_BOT_TOKEN` | [@BotFather](https://t.me/BotFather)'dan olingan token (majburiy) |
| `ALLOWED_TELEGRAM_IDS` | Ruxsat etilgan Telegram ID'lar (vergul bilan). Bo'sh — hamma uchun ochiq (xavfli) |
| `BOT_WORKDIR` | Default ish papkasi. Bo'sh — `C:\Users\nbkab\OneDrive\Ishchi stol` |

## Cheklovlar

- **Interaktiv komandalar yo'q:** `python` REPL, `vim`, `nano` kabi stdin kutadigan dasturlar 30 daqiqada timeout bo'ladi
- **Bitta komanda bir vaqtda:** har bir foydalanuvchi uchun komandalar ketma-ket bajariladi (oldingisi tugamaguncha keyingisi kutadi). `/stop` orqali aktiv'ini uzish mumkin
- **In-memory state:** bot qayta ishga tushganda har bir foydalanuvchining CWD'si default'ga qaytadi (Claude session ID ham yo'qoladi)

## Arxitektura

```
remofy-bot/
├── cmd/bot/main.go           # Entry: env + Telegram polling
├── internal/bot/
│   ├── handler.go            # Update dispatcher, whitelist, komandalar
│   ├── session.go            # Per-user CWD + RunCommand (PowerShell exec)
│   ├── claude.go             # Local claude -p stream-json parser
│   └── output.go             # ANSI strip, HTML escape
└── scripts/                  # Windows install (Task Scheduler)
    ├── install.ps1
    ├── uninstall.ps1
    ├── run-bot.ps1
    └── README.md
```
