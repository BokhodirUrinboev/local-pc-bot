# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

A Go Telegram bot that runs **on the local Windows machine** as a thin wrapper around the `claude` CLI in agent mode. Whitelisted users (private chats and pre-approved groups) message the bot; every message becomes a `claude -p --dangerously-skip-permissions ...` invocation with full tool access — Read/Edit/Write/Bash plus any MCP servers configured in `BOT_WORKDIR/.mcp.json` (e.g. GitHub MCP).

There is **no** database, no SSH, no PowerShell free-text mode — earlier history had those (see git log) but they were ripped out. The bot's only job now is: receive Telegram message → run Claude in the workspace → stream the answer back, edit-throttled in a single message.

Sessions are **per-chat** (private chat = one session, group chat = one shared session). `claude --resume <id>` keeps conversational memory across messages until `/reset`.

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

Entry point `cmd/bot/main.go` loads `.env`, builds a `bot.Manager` + `bot.Bot`, registers the slash-command menu, then long-polls Telegram and dispatches each update to `Bot.HandleUpdate` in a goroutine.

`internal/bot/` is the only package with logic:

- **handler.go** — two-tier whitelist (user IDs + chat IDs), group/private routing, slash-command dispatch. In **groups**, the bot only responds to (a) slash commands, (b) messages mentioning `@botusername`, or (c) replies to the bot's own messages. The `@mention` substring is stripped before the prompt is sent to Claude. The `⏹ Stop` reply-keyboard button is treated as `/stop`.
- **session.go** — `Manager` lazily creates a `*Session` per Telegram **chat ID** (not user ID — groups share one session). Sessions hold the active `claudeCancel`, the captured `claudeSessionID` for `--resume`, and a `cmdMu` that serializes prompts per chat. Workspace is global (`BOT_WORKDIR`).
- **claude.go** — probes `claude` / `claude.cmd` / `claude.exe` / `claude.bat` on PATH (lazy, cached on the session), then runs:
  ```
  claude -p <prompt>
    --output-format stream-json --include-partial-messages --verbose
    --dangerously-skip-permissions
    [--mcp-config <BOT_WORKDIR>/.mcp.json]   (only if file exists)
    [--resume <session-id>]
  ```
  with `cmd.Dir = BOT_WORKDIR`. Parses the JSONL stream line-by-line (`stream_event` text deltas, `assistant`, `system/init`, `result`) and edit-throttles the placeholder Telegram message every 1.5s. Captured `session_id` is stashed on the session for the next `--resume`.
- **output.go** — `stripANSI` regex (CSI/OSC + control chars, keep `\n`/`\t`), `htmlEscape` for Telegram HTML mode, and a `fileExists` helper. Telegram message cap is `maxMsgChars = 3800`; longer output is tail-truncated with a `…` prefix.

### Why PowerShell wraps `claude.exe`

`claude.exe` on Windows does not get a usable OAuth/keychain handle when invoked via Go's `os/exec` directly. The bot wraps the call in `powershell.exe -Command "& 'C:\path\claude.exe' -p '...' --resume '...' ..."` so PowerShell attaches the right console/session/token. `psQuote` does single-quote escaping (`'` → `''`); never string-concatenate raw paths into the wrapper.

### Concurrency model

- Per-chat `cmdMu` — only one Claude prompt runs at a time per chat; new messages queue behind it.
- `SendInterrupt` deliberately **does not** take `cmdMu` — it grabs the cancel func under the short `mu` and fires it, so a stuck prompt can be unstuck.
- `claudeCancel` is the cancellation path; `claudeMaxWait = 30 min`.
- All session state is in-memory — restarting the bot forgets every chat's `--resume` ID. There is no persistent transcript on the bot side; Claude itself stores session state in its own dir.

### Live message editing

`RunClaude` sends a placeholder message immediately, then `editMessageText`s it on a 1.5s ticker as `stream_event` deltas arrive. Telegram's "message is not modified" error is filtered out of logs. On final edit, the icon flips (🤖✍️→🤖) and cancel/timeout reasons are prepended.

## Configuration

`.env` (or process env) — loaded by `godotenv` from cwd:

| Var | Effect |
|-----|--------|
| `TELEGRAM_BOT_TOKEN` | Required. Bot dies on startup if missing. |
| `ALLOWED_TELEGRAM_IDS` | User-level whitelist (private + inside groups). Comma/space/`;` separated int64s. Empty → open to all in private chats (logged loudly). |
| `ALLOWED_CHAT_IDS` | Group/supergroup chat-ID whitelist. Empty → bot is silent in all groups (safe default). Group IDs are negative; supergroups start with `-100`. |
| `BOT_WORKDIR` | Workspace where `claude` is invoked. `.mcp.json` and `.claude/settings.json` are read from here. Empty → hardcoded `C:\Users\nbkab\OneDrive\Ishchi stol`. |
| `GITHUB_PERSONAL_ACCESS_TOKEN` | Read by `.mcp.json` for the GitHub MCP server (see below). |

Slash commands are registered via `setMyCommands` on startup and listed in `BotCommands()` — keep the two in sync when adding a command. Current set: `/start /help /stop /reset /workdir`.

### Group setup notes

- Telegram bots in groups have **privacy mode** on by default — they only see commands, mentions, and replies. That matches what `HandleUpdate` reacts to, so no BotFather toggle is needed.
- To wire a group: add the bot to the group, get the chat ID (denied attempts log `chat=<id>`), then add it to `ALLOWED_CHAT_IDS` and restart the bot.
- The user-level whitelist still applies inside groups: a user not in `ALLOWED_TELEGRAM_IDS` is silently ignored even in a whitelisted group.

### MCP servers

The bot passes `--mcp-config <BOT_WORKDIR>/.mcp.json` only if the file exists. Create that file in `BOT_WORKDIR` (not in this project) — see `.mcp.json.example` for the template. Default template wires the GitHub MCP server via `npx -y @modelcontextprotocol/server-github` (requires Node + a Personal Access Token in env).

For other MCP servers, follow the same `mcpServers` schema — Claude Code will load them all on every prompt and inject their tools as `mcp__<server>__<tool>` names. Because `--dangerously-skip-permissions` is set, the agent gets all of them with no per-call approval.

### Skills and `.claude/settings.json`

Anything Claude Code skills/hooks-related goes in `<BOT_WORKDIR>/.claude/settings.json`. The bot doesn't manage that file — it's just picked up by `claude` because we run with `cmd.Dir = BOT_WORKDIR`. Use it for project-level skills, allowed-tool overrides, hooks, etc.

## Known divergence: CI vs runtime

`.github/workflows/ci.yml` builds a Docker image and pushes a tag bump to a `k8s-gitops` repo. The `Dockerfile` was deleted in the current working tree (see `git status`), so that workflow currently won't pass on `main` until either the Dockerfile is restored or the workflow is removed/rewritten for the Windows-binary install model. Don't trust CI as a source of truth about how this bot is meant to be deployed — the install path is `scripts/install.ps1` on Windows.

## Conventions worth keeping

- All user-facing strings are Uzbek (Latin script). Match that voice when adding messages.
- Telegram messages use `ParseMode=HTML`; always pass user content through `htmlEscape` and Claude output through `stripANSI` before formatting.
- PowerShell paths from user input get `psQuote` (single-quote with `'` → `''` escape) — never string-concatenate raw paths into the wrapper script.
- Logging goes through stdlib `log`; in production the wrapper redirects stderr to `bot-YYYY-MM-DD.err.log`. Don't add a logging framework.
