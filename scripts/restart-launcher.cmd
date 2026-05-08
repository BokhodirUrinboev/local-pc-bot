@echo off
REM Wrapper to launch restart.ps1 with safe quoting (path has spaces).
powershell.exe -NoProfile -NoLogo -NonInteractive -WindowStyle Hidden -ExecutionPolicy Bypass -File "C:\Users\nbkab\OneDrive\Ishchi stol\remofy-bot\scripts\restart.ps1" 1> "C:\Users\nbkab\OneDrive\Ishchi stol\remofy-bot\scripts\launcher.out.log" 2> "C:\Users\nbkab\OneDrive\Ishchi stol\remofy-bot\scripts\launcher.err.log"
