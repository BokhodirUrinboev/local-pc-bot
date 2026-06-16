# Remofy local bot

Shu kompyuterda ishlovchi Telegram bot. Whitelist'dagi foydalanuvchilarga
shu kompyuterda to'g'ridan-to'g'ri shell komandalari yoki Claude AI agentini
ishga tushirishga ruxsat beradi. **Cross-platform:** Windows va Linux (Ubuntu).

## Xususiyatlar

- **Ikki rejim:** har bir chat default `shell` rejimida boshlanadi; `/claude` orqali AI rejimiga, `/powershell` (yoki `/shell`) orqali qaytariladi
- **Local shell:** shell rejimida har qanday matn bajariladi — Windows'da `powershell.exe`, Linux'da `bash` (workspace papkada)
- **Claude rejimi:** local `claude` CLI bilan stream qilinadigan suhbat (`--resume` orqali kontekst saqlanadi, to'liq tool/MCP access)
- **Live output:** uzoq ishlaydigan komandalar Telegram xabarini real vaqtda yangilaydi (~1.5s edit interval)
- **Whitelist:** faqat ruxsat etilgan Telegram ID'lar foydalana oladi (`ALLOWED_TELEGRAM_IDS`), gruppalar uchun alohida (`ALLOWED_CHAT_IDS`)
- **Navbat:** har bir chatda bir vaqtda faqat bitta komanda ishlaydi, qolganlari kutadi
- **Stop tugmasi:** aktiv komandani Telegramdan to'xtatish (`taskkill /F /T` butun process daraxti)
- **Server sifatida:** Windows'da Task Scheduler (joriy foydalanuvchi ostida — Claude OAuth tokeni uchun) + `RemofyBotWatchdog`; Linux'da systemd service (`Restart=always`)

## Sozlash (lokal)

### Windows

```powershell
# 1. .env yarating
Copy-Item .env.example .env
notepad .env
# TELEGRAM_BOT_TOKEN va ALLOWED_TELEGRAM_IDS to'ldiring

# 2. Build
$env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -ldflags='-s -w' -o '.dist\remofy-bot.exe' .\cmd\bot

# 3. Sinov uchun ishga tushirish
.\.dist\remofy-bot.exe
```

### Linux (Ubuntu)

```bash
# 1. .env yarating
cp .env.example .env
nano .env   # TELEGRAM_BOT_TOKEN va ALLOWED_TELEGRAM_IDS to'ldiring

# 2. Build
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-s -w' -o .dist/remofy-bot ./cmd/bot

# 3. Sinov uchun ishga tushirish
./.dist/remofy-bot
```

> **Claude rejimi uchun:** botni ishga tushiruvchi user ostida avval `claude login`
> bajarilgan bo'lishi shart (token `~/.claude/` ichida saqlanadi).

## Server sifatida o'rnatish (boot'da auto-start)

### Windows

`scripts/install.ps1` Administrator PowerShell ostida ishlatiladi. Batafsil: [scripts/README.md](scripts/README.md).

```powershell
# Administrator PowerShell:
.\scripts\install.ps1
```

Bu quyidagilarni qiladi:
- `C:\ProgramData\remofy-bot\` ga fayllarni o'rnatadi
- AC quvvatda hech qachon uxlamasin deb power planni o'zgartiradi
- Joriy foydalanuvchi (Interactive) ostida `RemofyBot` taskini yaratadi (login'da +30s auto-start). SYSTEM emas, chunki Claude OAuth tokeni shu profil ichida (`%USERPROFILE%\.claude\`)
- `RemofyBotWatchdog` taskini yaratadi (SYSTEM, har 1 daqiqada: `RemofyBot` Running bo'lmasa qayta ishga tushiradi)
- Botni darhol ishga tushiradi

### Linux (systemd)

```bash
sudo ./scripts/install-linux.sh
```

Bu quyidagilarni qiladi:
- Build qiladi va `/opt/remofy-bot/` ga o'rnatadi (`.env` ham shu yerga)
- `remofy-bot.service` systemd unitini yaratadi (`User=` — sizning user, `Restart=always`)
- Botni darhol ishga tushiradi (`systemctl enable --now`)

Boshqaruv: `systemctl status remofy-bot`, jonli loglar: `journalctl -u remofy-bot -f`.

## Komandalar

| Komanda | Tavsif |
|---------|--------|
| `/start`, `/help` | Yordam + joriy rejim va workdir |
| `/powershell` | PowerShell rejimini yoqish (default) |
| `/powershell <komanda>` | Bir martalik PS komanda (rejim o'zgarmaydi) |
| `/claude` | Claude AI rejimini yoqish |
| `/claude <savol>` | Bir martalik prompt yuborish (rejim o'zgarmaydi) |
| `/stop` | Aktiv komandani uzish (Ctrl+C analog) |
| `/reset` | Claude suhbat tarixini tozalash (yangi sessiya) |
| `/workdir` | Ishlayotgan papka |
| (boshqa matn) | Joriy rejimga ko'ra: PS komanda yoki Claude prompt |

## Konfiguratsiya

| Env var | Tavsif |
|---------|--------|
| `TELEGRAM_BOT_TOKEN` | [@BotFather](https://t.me/BotFather)'dan olingan token (majburiy) |
| `ALLOWED_TELEGRAM_IDS` | Ruxsat etilgan Telegram ID'lar (vergul bilan). Bo'sh — barcha private chatlar uchun ochiq (xavfli) |
| `ALLOWED_CHAT_IDS` | Gruppa/supergroup chat ID whitelisti. Bo'sh — bot gruppalarda jim turadi (xavfsiz default) |
| `BOT_WORKDIR` | shell va Claude ishlayotgan papka. Bo'sh — foydalanuvchi home papkasi (`%USERPROFILE%` / `$HOME`) |
| `BOT_SYSTEM_PROMPT` | Claude uchun `--append-system-prompt` (persona) |
| `GITHUB_PERSONAL_ACCESS_TOKEN` | `.mcp.json` ichidagi GitHub MCP server uchun |

## Cheklovlar

- **Interaktiv komandalar yo'q:** `python` REPL, `vim`, `nano` kabi stdin kutadigan dasturlar 30 daqiqada timeout bo'ladi
- **Bitta komanda bir vaqtda:** har bir chat uchun komandalar ketma-ket bajariladi (oldingisi tugamaguncha keyingisi navbatda kutadi). `/stop` orqali aktiv'ini uzish mumkin
- **In-memory state:** bot qayta ishga tushganda har bir chat'ning rejimi (default PS) va Claude `--resume` ID'si yo'qoladi
- **CWD persist bo'lmaydi:** har bir shell komanda yangi processda ishlaydi (`cd` keyingi komandaga ta'sir qilmaydi). Workdir global

## Arxitektura

```
remofy-bot/
├── cmd/bot/main.go           # Entry: env + Telegram polling
├── internal/bot/
│   ├── handler.go            # Update dispatcher, whitelist, slash komandalar, mode routing
│   ├── session.go            # Per-chat sessiya: mode, cmdSlot navbati, SendInterrupt
│   ├── claude.go             # claude -p stream-json parser + edit-throttle (OS-agnostik)
│   ├── shell.go              # shell komanda exec + stdout/stderr stream + edit-throttle (OS-agnostik)
│   ├── platform_windows.go   # Windows exec: powershell.exe wrapper, taskkill
│   ├── platform_unix.go      # Linux exec: bash/claude to'g'ridan, setpgid + SIGKILL
│   ├── poll.go               # Long-polling + message_thread_id (forum/topic) qo'llab-quvvatlash
│   └── output.go             # ANSI strip, HTML escape
└── scripts/
    ├── install.ps1           # Windows: Task Scheduler install
    ├── uninstall.ps1
    ├── run-bot.ps1
    ├── install-linux.sh      # Linux: systemd install
    ├── remofy-bot.service    # Linux: systemd unit template
    └── README.md
```
