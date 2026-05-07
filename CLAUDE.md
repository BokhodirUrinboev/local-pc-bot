# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

A Go Telegram bot that runs **on the local Windows machine** and gives whitelisted Telegram users:
- A persistent PowerShell session (per Telegram user, with sticky CWD).
- A streaming `claude -p` mode that reuses session IDs via `--resume`.

There is **no** database, no SSH, no container runtime in the running bot — all of those existed in earlier history (see git log) but were ripped out. The bot is a single Windows binary that shells out to `powershell.exe` and `claude`.

## Common commands

Build (from project root, PowerShell):

```powershell
$env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -ldflags='-s -w' -o '.dist\remofy-bot.exe' .\cmd\bot
```

Run locally for testing:

```powershell
.\.dist\remofy-bot.exe        # reads .env from cwd
```

Install/update the long-running service (Administrator PowerShell required):

```powershell
.\scripts\install.ps1         # copies to C:\ProgramData\remofy-bot, registers RemofyBot Task Scheduler task
.\scripts\uninstall.ps1
```

Tail live logs of the installed service:

```powershell
Get-Content C:\ProgramData\remofy-bot\logs\bot-*.err.log -Tail 50 -Wait
```

Quick lint/vet (no test suite exists in this repo):

```powershell
go vet ./...
gofmt -l .
```

## Architecture

Entry point `cmd/bot/main.go` loads `.env`, builds a `bot.Manager` + `bot.Bot`, registers slash-command menu, then long-polls Telegram and dispatches each update to `Bot.HandleUpdate` in a goroutine.

`internal/bot/` is the only package with logic:

- **handler.go** — whitelist gate (`ALLOWED_TELEGRAM_IDS` map; empty = open to everyone, logged as a warning), slash-command router, free text dispatch. Free text goes to `RunClaude` if Claude mode is on, otherwise `RunCommand`. The `⏹ Stop` reply-keyboard button is treated as `/stop`.
- **session.go** — `Manager` lazily creates a `*Session` per Telegram user ID; sessions hold the sticky CWD, the active `runCancel` / `claudeCancel`, and a `cmdMu` that serializes commands per user. `RunCommand` is the core PowerShell exec — see "CWD marker trick" below.
- **claude.go** — probes `claude` / `claude.cmd` / `claude.exe` / `claude.bat` on PATH, then runs `claude -p <prompt> --output-format stream-json --include-partial-messages --verbose [--resume <id>]`. Parses the JSONL stream line-by-line (`stream_event` text deltas, `assistant`, `system/init`, `result`) and edit-throttles the placeholder Telegram message every 1.5s. Captured `session_id` is stashed on the session for the next `--resume`.
- **output.go** — `stripANSI` regex (CSI/OSC + control chars, keep `\n`/`\t`) and `htmlEscape` for Telegram HTML mode. Telegram message cap is `maxMsgChars = 3800`; longer output is tail-truncated with a `…` prefix.

### CWD marker trick (session.go)

PowerShell is invoked once per command with a wrapper script:

```
Set-Location -LiteralPath '<cwd>'
$ErrorActionPreference = 'Continue'
<user text verbatim>

Write-Output '<<<__REMOFY_CWD_MARKER__>>>'
Write-Output (Get-Location).Path
```

After the process exits, `splitCwd` finds the **last** marker in stdout+stderr and treats the next line as the new CWD. This is how `cd foo` persists across messages without a long-lived shell. If the user's command happens to print that exact marker line, CWD detection breaks — that's the documented assumption. `$ErrorActionPreference = 'Continue'` matches cmd-style behavior so non-terminating errors don't abort the marker.

### Concurrency model

- Per-user `cmdMu` — only one `RunCommand` or `RunClaude` runs at a time per user; new messages queue behind it.
- `SendInterrupt` deliberately **does not** take `cmdMu` — it grabs the cancel funcs under the short `mu` and fires them, so a stuck command can be unstuck.
- `runCancel` / `claudeCancel` are the only context cancellation paths; `cmdMaxWait = 30 min` for shell, `claudeMaxWait = 10 min` for Claude.
- All session state is in-memory — restarting the bot resets every user's CWD to `BOT_WORKDIR` and forgets every Claude session ID.

### Live message editing

Both `RunCommand` and `RunClaude` send a placeholder message immediately, then `editMessageText` it on a 1.5s ticker. Telegram's "message is not modified" error is filtered out of logs. On final edit, status icon flips (⏳→✅, 🤖✍️→🤖) and cancel/timeout reasons are prepended.

## Configuration

`.env` (or process env) — loaded by `godotenv` from cwd:

| Var | Effect |
|-----|--------|
| `TELEGRAM_BOT_TOKEN` | Required. Bot dies on startup if missing. |
| `ALLOWED_TELEGRAM_IDS` | Comma/space/semicolon-separated int64s. Empty → bot is open to anyone (logged loudly). |
| `BOT_WORKDIR` | Default per-session CWD. Empty → hardcoded `C:\Users\nbkab\OneDrive\Ishchi stol`. |

Slash commands are registered via `setMyCommands` on startup and listed in `BotCommands()` — keep the two in sync when adding a command.

## Known divergence: CI vs runtime

`.github/workflows/ci.yml` builds a Docker image and pushes a tag bump to a `k8s-gitops` repo. The `Dockerfile` was deleted in the current working tree (see `git status`), so that workflow currently won't pass on `main` until either the Dockerfile is restored or the workflow is removed/rewritten for the Windows-binary install model. Don't trust CI as a source of truth about how this bot is meant to be deployed — the install path is `scripts/install.ps1` on Windows.

## Conventions worth keeping

- All user-facing strings are Uzbek (Latin script). Match that voice when adding messages.
- Telegram messages use `ParseMode=HTML`; always pass user content through `htmlEscape` and shell output through `stripANSI` before formatting.
- PowerShell paths from user input get `psQuote` (single-quote with `'` → `''` escape) — never string-concatenate raw paths into the wrapper script.
- Logging goes through stdlib `log`; in production the wrapper redirects stderr to `bot-YYYY-MM-DD.err.log`. Don't add a logging framework.
