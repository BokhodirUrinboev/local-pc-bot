#Requires -Version 5.1
#Requires -RunAsAdministrator
# install.ps1 -- Remofy bot: Windows Task Scheduler installer.
#
# What it does:
#   1. Copies .exe + wrapper to C:\ProgramData\remofy-bot
#   2. Copies .env (or creates from .env.example)
#   3. Disables sleep/hibernate on AC power
#   4. Creates "RemofyBot" scheduled task (LocalSystem, AtStartup, restart=999)
#   5. Starts the task
#
# Usage (in Administrator PowerShell):
#   .\install.ps1

$ErrorActionPreference = "Stop"

$RepoRoot     = Split-Path -Parent $PSScriptRoot
$InstallDir   = "C:\ProgramData\remofy-bot"
$LogDir       = Join-Path $InstallDir "logs"
$TaskName     = "RemofyBot"
$WatchdogTask = "RemofyBotWatchdog"

# Bot CURRENT USER nomidan ishlashi shart — Claude CLI OAuth tokeni shu
# foydalanuvchi profili ichida (`%USERPROFILE%\.claude\`). SYSTEM bo'lsa
# "Not logged in" xatosi chiqadi. Interactive logon token kerak.
$RunUser = "$env:USERDOMAIN\$env:USERNAME"
Write-Host "    RunAs:      $RunUser"

$ExeSrc     = Join-Path $RepoRoot ".dist\remofy-bot.exe"
$WrapperSrc = Join-Path $PSScriptRoot "run-bot.ps1"
$EnvSrc     = Join-Path $RepoRoot ".env"
$EnvExample = Join-Path $RepoRoot ".env.example"

Write-Host "==> Remofy Bot install" -ForegroundColor Cyan
Write-Host "    InstallDir: $InstallDir"
Write-Host "    Task:       $TaskName"
Write-Host ""

# --- Verify sources ---
if (-not (Test-Path $ExeSrc)) {
    Write-Host "ERROR: $ExeSrc not found." -ForegroundColor Red
    Write-Host "Build first:" -ForegroundColor Yellow
    Write-Host "  `$env:GOOS='windows'; `$env:GOARCH='amd64'; go build -ldflags='-s -w' -o '.dist\remofy-bot.exe' ./cmd/bot" -ForegroundColor Yellow
    exit 1
}
if (-not (Test-Path $WrapperSrc)) {
    Write-Host "ERROR: $WrapperSrc not found." -ForegroundColor Red
    exit 1
}

# --- Stop and remove existing tasks ---
Write-Host "==> Checking existing tasks..."
foreach ($t in @($TaskName, $WatchdogTask)) {
    $existing = Get-ScheduledTask -TaskName $t -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Host "    Found old task '$t' -- stopping and removing..."
        try { Stop-ScheduledTask -TaskName $t -ErrorAction SilentlyContinue } catch {}
        Unregister-ScheduledTask -TaskName $t -Confirm:$false
    }
}

# Stop existing bot process (so we can replace the .exe)
Get-Process -Name "remofy-bot" -ErrorAction SilentlyContinue | ForEach-Object {
    Write-Host ("    Stopping old process (PID={0})..." -f $_.Id)
    Stop-Process -Id $_.Id -Force
    Start-Sleep -Seconds 2
}

# --- Create directories ---
Write-Host "==> Creating directories..."
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
New-Item -ItemType Directory -Force -Path $LogDir     | Out-Null

# --- Copy files ---
Write-Host "==> Copying files..."
Copy-Item $ExeSrc     -Destination (Join-Path $InstallDir "remofy-bot.exe") -Force
Copy-Item $WrapperSrc -Destination (Join-Path $InstallDir "run-bot.ps1")    -Force

$EnvDest = Join-Path $InstallDir ".env"
if (Test-Path $EnvSrc) {
    Copy-Item $EnvSrc -Destination $EnvDest -Force
    Write-Host "    .env copied from project root" -ForegroundColor Green
} elseif (-not (Test-Path $EnvDest)) {
    if (Test-Path $EnvExample) {
        Copy-Item $EnvExample -Destination $EnvDest -Force
    }
    Write-Host ""
    Write-Host "    WARNING: Edit $EnvDest before running (TELEGRAM_BOT_TOKEN, ALLOWED_TELEGRAM_IDS, ...)" -ForegroundColor Yellow
    Write-Host ""
} else {
    Write-Host "    .env already exists -- keeping it" -ForegroundColor Green
}

# --- Power plan: never sleep on AC ---
Write-Host "==> Power settings (AC: never sleep)..."
& powercfg /change standby-timeout-ac 0    | Out-Null
& powercfg /change hibernate-timeout-ac 0  | Out-Null
& powercfg /change disk-timeout-ac 0       | Out-Null

# --- Create scheduled task ---
Write-Host "==> Creating scheduled task..."

$psArgs = "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$InstallDir\run-bot.ps1`""

$action = New-ScheduledTaskAction `
    -Execute "powershell.exe" `
    -Argument $psArgs `
    -WorkingDirectory $InstallDir

$trigger = New-ScheduledTaskTrigger -AtLogOn -User $RunUser
$trigger.Delay = "PT30S"

$settings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -RestartCount 999 `
    -RestartInterval (New-TimeSpan -Minutes 1) `
    -ExecutionTimeLimit (New-TimeSpan -Seconds 0) `
    -MultipleInstances IgnoreNew

$principal = New-ScheduledTaskPrincipal `
    -UserId $RunUser `
    -LogonType Interactive `
    -RunLevel Highest

Register-ScheduledTask `
    -TaskName    $TaskName `
    -Description "Remofy Telegram bot - auto-start on boot" `
    -Action      $action `
    -Trigger     $trigger `
    -Settings    $settings `
    -Principal   $principal | Out-Null

Write-Host "    Task '$TaskName' registered (LocalSystem)" -ForegroundColor Green

# --- Watchdog task: every 1 minute, restart RemofyBot if not Running ---
Write-Host "==> Creating watchdog task..."

$watchdogCmd = "if ((Get-ScheduledTask -TaskName '$TaskName' -ErrorAction SilentlyContinue).State -ne 'Running') { Start-ScheduledTask -TaskName '$TaskName' }"
$watchdogArgs = "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -Command `"$watchdogCmd`""

$wAction = New-ScheduledTaskAction `
    -Execute "powershell.exe" `
    -Argument $watchdogArgs

$wTrigger = New-ScheduledTaskTrigger -Once -At (Get-Date) `
    -RepetitionInterval (New-TimeSpan -Minutes 1) `
    -RepetitionDuration (New-TimeSpan -Days 9999)

$wSettings = New-ScheduledTaskSettingsSet `
    -AllowStartIfOnBatteries `
    -DontStopIfGoingOnBatteries `
    -StartWhenAvailable `
    -ExecutionTimeLimit (New-TimeSpan -Minutes 1) `
    -MultipleInstances IgnoreNew

# Watchdog SYSTEM nomidan ishlaydi — bu shart edi, aks holda
# har daqiqada Interactive user'ning desktop'ida PS oynasi miltillaydi.
# Watchdog Claude OAuth'iga muhtoj emas, faqat Start-ScheduledTask qiladi
# (SYSTEM hamma task'ni boshqara oladi).
$wPrincipal = New-ScheduledTaskPrincipal `
    -UserId "SYSTEM" `
    -LogonType ServiceAccount `
    -RunLevel Highest

Register-ScheduledTask `
    -TaskName    $WatchdogTask `
    -Description "Restarts RemofyBot if it's not Running (every 1 min)" `
    -Action      $wAction `
    -Trigger     $wTrigger `
    -Settings    $wSettings `
    -Principal   $wPrincipal | Out-Null

Write-Host "    Task '$WatchdogTask' registered (1-min watchdog)" -ForegroundColor Green

# --- Start now ---
Write-Host "==> Starting bot..."
Start-ScheduledTask -TaskName $TaskName
Start-ScheduledTask -TaskName $WatchdogTask
Start-Sleep -Seconds 3

$state         = (Get-ScheduledTask -TaskName $TaskName).State
$watchdogState = (Get-ScheduledTask -TaskName $WatchdogTask).State
Write-Host "    ${TaskName}: $state"
Write-Host "    ${WatchdogTask}: $watchdogState"

Write-Host ""
Write-Host "==> DONE!" -ForegroundColor Cyan
Write-Host ""
Write-Host "Management:" -ForegroundColor Yellow
Write-Host "  Status:  Get-ScheduledTask RemofyBot"
Write-Host "  Start:   Start-ScheduledTask RemofyBot"
Write-Host "  Stop:    Stop-ScheduledTask RemofyBot"
Write-Host "  Logs:    Get-Content $LogDir\bot-*.err.log -Tail 50 -Wait"
Write-Host "  Remove:  .\uninstall.ps1"
