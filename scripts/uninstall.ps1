#Requires -Version 5.1
#Requires -RunAsAdministrator
# uninstall.ps1 -- removes the RemofyBot scheduled task.

$ErrorActionPreference = "Continue"

$InstallDir   = "C:\ProgramData\remofy-bot"
$TaskName     = "RemofyBot"
$WatchdogTask = "RemofyBotWatchdog"

Write-Host "==> Remofy Bot uninstall" -ForegroundColor Cyan

foreach ($t in @($WatchdogTask, $TaskName)) {
    $existing = Get-ScheduledTask -TaskName $t -ErrorAction SilentlyContinue
    if ($existing) {
        Write-Host "==> Stopping task '$t'..."
        try { Stop-ScheduledTask -TaskName $t -ErrorAction SilentlyContinue } catch {}
        Unregister-ScheduledTask -TaskName $t -Confirm:$false
        Write-Host "    Task '$t' removed" -ForegroundColor Green
    } else {
        Write-Host "    Task '$t' not found (already removed)"
    }
}

Get-Process -Name "remofy-bot" -ErrorAction SilentlyContinue | ForEach-Object {
    Write-Host ("==> Stopping process (PID={0})..." -f $_.Id)
    Stop-Process -Id $_.Id -Force
}

if (Test-Path $InstallDir) {
    $reply = Read-Host "Remove install directory [$InstallDir]? (y/N)"
    if ($reply -match '^(y|yes)$') {
        Remove-Item -Path $InstallDir -Recurse -Force
        Write-Host "    $InstallDir removed" -ForegroundColor Green
    } else {
        Write-Host "    $InstallDir kept (logs and .env preserved)"
    }
}

Write-Host ""
Write-Host "==> Done." -ForegroundColor Cyan
