# Windows server install

Bu skriptlar Remofy botini Windows kompyuteriga **server sifatida** o'rnatadi:
- kompyuter yoqilishi bilan avtomatik ishga tushadi (login shart emas);
- crash bo'lsa qayta ishga tushadi;
- AC quvvatda hech qachon sleep mode'ga ketmaydi;
- LocalSystem hisobi ostida ishlaydi (foydalanuvchi paroli kerak emas).

## Talablar

- Windows 10 / 11
- Administrator huquqi (UAC)
- Faqat **whitelist'dagi** Telegram ID'lar bot bilan ishlaydi
  (`.env` ichida `ALLOWED_TELEGRAM_IDS=...` to'ldirilishi shart)

## O'rnatish

```powershell
# 1. Loyiha papkasiga o'ting
cd "C:\Users\nbkab\OneDrive\Ishchi stol\remofy-bot"

# 2. .env ni yarating va to'ldiring (TELEGRAM_BOT_TOKEN, ALLOWED_TELEGRAM_IDS, va h.k.)
Copy-Item .env.example .env
notepad .env

# 3. Build (agar .dist\remofy-bot.exe hali yo'q bo'lsa)
$env:GOOS="windows"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go build -ldflags="-s -w" -o ".dist\remofy-bot.exe" .\cmd\bot

# 4. Administrator PowerShell ochib (Win+X → Terminal (Admin)):
cd "C:\Users\nbkab\OneDrive\Ishchi stol\remofy-bot"
.\scripts\install.ps1
```

`install.ps1` quyidagilarni qiladi:
1. `.exe`, `run-bot.ps1`, `.env` ni `C:\ProgramData\remofy-bot\` ga ko'chiradi.
2. Power planni "AC'da hech qachon uxlamasin" qiladi.
3. `RemofyBot` nomli Task Scheduler taskini yaratadi (LocalSystem, AtStartup, restart=999).
4. Botni darhol ishga tushiradi.

## Boshqaruv

```powershell
# Status
Get-ScheduledTask RemofyBot

# To'xtatish
Stop-ScheduledTask RemofyBot

# Qayta ishga tushirish
Start-ScheduledTask RemofyBot

# Loglarni ko'rish (jonli)
Get-Content C:\ProgramData\remofy-bot\logs\bot-*.err.log -Tail 50 -Wait

# Wrapper logi (qayta ishga tushishlar tarixi)
Get-Content C:\ProgramData\remofy-bot\logs\wrapper-*.log -Tail 50

# Whitelist'ni o'zgartirish
notepad C:\ProgramData\remofy-bot\.env
Stop-ScheduledTask RemofyBot ; Start-ScheduledTask RemofyBot
```

## O'chirish

```powershell
.\scripts\uninstall.ps1
```

## Sleep mode haqida

**AC quvvatda** (zaryadlovchi ulangan): bot 24/7 ishlaydi — install skripti
`standby-timeout-ac=0`, `hibernate-timeout-ac=0` qilib qo'yadi.

**Batareyada**: Windows uxlasa, jarayonlar to'xtaydi. Bot ham to'xtaydi.
Agar laptop bo'lsa va qopqoq yopilganda ham ishlasin desangiz:

```powershell
powercfg /SETACVALUEINDEX SCHEME_CURRENT SUB_BUTTONS LIDACTION 0
powercfg /SETACTIVE SCHEME_CURRENT
```

## Ichki tuzilma

```
C:\ProgramData\remofy-bot\
├── remofy-bot.exe        # bot binarisi
├── run-bot.ps1           # restart loop wrapper
├── .env                  # konfiguratsiya
└── logs\
    ├── wrapper-YYYY-MM-DD.log    # restart hodisalari
    ├── bot-YYYY-MM-DD.out.log    # bot stdout
    └── bot-YYYY-MM-DD.err.log    # bot stderr (loglar shu yerda)
```
