#Requires -Version 5.1
# run-bot.ps1 -- launched hidden by Task Scheduler.
# Runs remofy-bot.exe in a restart loop, redirects stdout/stderr to log files.

$ErrorActionPreference = "Continue"

$Dir    = "C:\ProgramData\remofy-bot"
$Exe    = Join-Path $Dir "remofy-bot.exe"
$LogDir = Join-Path $Dir "logs"

if (-not (Test-Path $LogDir)) {
    New-Item -ItemType Directory -Force -Path $LogDir | Out-Null
}

Set-Location $Dir

function Write-WrapperLog {
    param([string]$Msg)
    $ts = Get-Date -Format 'yyyy-MM-dd HH:mm:ss'
    $f  = Join-Path $LogDir ("wrapper-{0}.log" -f (Get-Date -Format 'yyyy-MM-dd'))
    Add-Content -Path $f -Value "[$ts] $Msg" -Encoding utf8
}

Write-WrapperLog "Wrapper started. Exe: $Exe"

if (-not (Test-Path $Exe)) {
    Write-WrapperLog "FATAL: exe not found: $Exe"
    exit 1
}

$delay = 5
while ($true) {
    $stamp  = Get-Date -Format 'yyyy-MM-dd'
    $stdout = Join-Path $LogDir "bot-$stamp.out.log"
    $stderr = Join-Path $LogDir "bot-$stamp.err.log"
    $start  = Get-Date

    Write-WrapperLog "Starting bot..."
    try {
        $proc = Start-Process -FilePath $Exe `
            -WorkingDirectory $Dir `
            -RedirectStandardOutput $stdout `
            -RedirectStandardError  $stderr `
            -NoNewWindow -PassThru -Wait
        $code = $proc.ExitCode
    } catch {
        Write-WrapperLog "Start-Process error: $_"
        $code = -1
    }

    $ran = ((Get-Date) - $start).TotalSeconds
    Write-WrapperLog ("Bot exited (code={0}, ran={1:N0}s)" -f $code, $ran)

    if ($ran -lt 30) {
        $delay = [Math]::Min($delay * 2, 60)
    } else {
        $delay = 5
    }

    Write-WrapperLog "Restarting in $delay seconds..."
    Start-Sleep -Seconds $delay
}
