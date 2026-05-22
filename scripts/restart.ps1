# restart.ps1 - bot'ni xavfsiz qayta ishga tushiradi (binary swap + restart)
# Detached holda WMI orqali ishga tushiriladi, shuning uchun parent bot
# o'lganda ham bu script ishlashda davom etadi.

$ErrorActionPreference = 'Continue'
$root = 'C:\Users\nbkab\OneDrive\Ishchi stol\remofy-bot'
$logDir = Join-Path $root 'logs'
New-Item -ItemType Directory -Force -Path $logDir | Out-Null

$ts = Get-Date -Format 'yyyy-MM-dd-HHmmss'
$logFile = Join-Path $logDir "restart-$ts.log"

function Log([string]$m) {
  "$([DateTime]::Now.ToString('HH:mm:ss')) $m" | Add-Content -Path $logFile -Encoding utf8
}

Log "=== Restart boshlandi ==="

# 1) Mening response Telegram'ga yetib borishi uchun biroz kutamiz
Start-Sleep -Seconds 20
Log "Sleep tugadi, eski bot'ni to'xtatamiz"

# 2) Eski remofy-bot.exe jarayonlarini to'xtatamiz
Get-Process -Name 'remofy-bot' -ErrorAction SilentlyContinue | ForEach-Object {
  Log "Stop-Process PID=$($_.Id)"
  Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
}
Start-Sleep -Seconds 3

# 3) Binary'larni swap qilamiz
$old = Join-Path $root '.dist\remofy-bot.exe'
$bak = Join-Path $root '.dist\remofy-bot.exe~'
$new = Join-Path $root '.dist\remofy-bot-new.exe'

if (-not (Test-Path $new)) {
  Log "ERROR: yangi binary topilmadi: $new"
  exit 1
}

if (Test-Path $bak) {
  Remove-Item $bak -Force -ErrorAction SilentlyContinue
  Log "Eski backup o'chirildi"
}
if (Test-Path $old) {
  Move-Item $old $bak -ErrorAction SilentlyContinue
  Log "Eski binary -> .exe~ ga ko'chirildi"
}
Move-Item $new $old
Log "Yangi binary -> remofy-bot.exe ga qo'yildi"

# 4) Yangi bot'ni detached holda ishga tushiramiz (WMI Create)
$errLog = Join-Path $logDir "bot-$(Get-Date -Format 'yyyy-MM-dd').err.log"
$outLog = Join-Path $logDir "bot-$(Get-Date -Format 'yyyy-MM-dd').out.log"

$psArgs = @(
  '-NoProfile', '-NoLogo', '-NonInteractive', '-WindowStyle', 'Hidden',
  '-Command',
  "Set-Location '$root'; & '$old' 2>>'$errLog' >>'$outLog'"
) -join ' '

$cmdLine = "powershell.exe $psArgs"
Log "Yangi bot ishga tushirilmoqda: $cmdLine"

$res = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{
  CommandLine      = $cmdLine
  CurrentDirectory = $root
}
Log "WMI Create natija: PID=$($res.ProcessId), ReturnValue=$($res.ReturnValue)"

# 5) Tekshiruv
Start-Sleep -Seconds 3
$running = Get-Process -Name 'remofy-bot' -ErrorAction SilentlyContinue
if ($running) {
  Log "OK: remofy-bot ishlamoqda, PID=$($running.Id)"
} else {
  Log "WARN: remofy-bot jarayoni topilmadi — err logni tekshiring: $errLog"
}
Log "=== Restart tugadi ==="
