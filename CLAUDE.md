# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this project is

A **cross-platform** (Windows + Linux) Go Telegram bot that runs **on the local machine** as a thin remote terminal. Whitelisted users (private chats and pre-approved groups) message the bot; each chat runs in one of two modes:

- **Shell mode (default)** — free-text messages are executed as shell commands in `BOT_WORKDIR`. The shell is platform-specific: **PowerShell** (`powershell.exe`) on Windows, **bash** on Linux. stdout+stderr are streamed back, edit-throttled in a single Telegram message.
- **Claude mode** — free-text messages become `claude -p --dangerously-skip-permissions ...` invocations with full tool access (Read/Edit/Write/Bash plus any MCP servers configured in `BOT_WORKDIR/.mcp.json`).

Mode is **per-chat** and switched explicitly via `/powershell` (alias `/shell`) and `/claude` (or with an inline argument: `/claude <prompt>` / `/powershell <cmd>` runs one-shot without flipping the mode). Sessions are **per-chat** (private chat = one session, group chat = one shared session). `claude --resume <id>` keeps Claude conversational memory across messages until `/reset`.

There is **no** database, no SSH, no per-user CWD persistence — every shell command runs in the global workdir.

### Cross-platform layout (build tags)

All OS-specific behavior lives behind build tags in two files; everything else is shared:

- `internal/bot/platform_windows.go` (`//go:build windows`) and `internal/bot/platform_unix.go` (`//go:build !windows`) each implement the same small surface: `claudeBinaryCandidates()`, `shellLabel()`, `buildClaudeCmd()`, `buildShellCmd()`, `setProcessGroup()`, `killTree()`.
- **Windows**: wraps both `claude` and the shell in `powershell.exe` (OAuth/console handle + UTF-8 + temp-file/`Invoke-Expression`); process-tree kill via `taskkill /F /T`.
- **Linux**: execs `claude` directly (no PS wrapper needed — see below) with the prompt on stdin; shell mode is `bash` reading the command from stdin; process-tree kill via `setpgid` + `SIGKILL` to the negative PID (process group).

When adding execution behavior, change **both** platform files or you'll break one OS's build.

## Common commands

**Windows** — build (from project root, PowerShell):

```powershell
$env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
go build -ldflags='-s -w' -o '.dist\remofy-bot.exe' .\cmd\bot
.\.dist\remofy-bot.exe        # run; reads .env from cwd
.\scripts\install.ps1         # install as service: C:\ProgramData\remofy-bot + RemofyBot Task Scheduler task
Get-Content C:\ProgramData\remofy-bot\logs\bot-*.err.log -Tail 50 -Wait   # tail logs
```

**Linux** — build & install (from project root, bash):

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags='-s -w' -o .dist/remofy-bot ./cmd/bot
./.dist/remofy-bot            # run; reads .env from cwd
sudo ./scripts/install-linux.sh   # install as systemd service (remofy-bot.service)
journalctl -u remofy-bot -f       # tail logs
```

**Cross-compile from Windows for the Ubuntu box:** set `$env:GOOS='linux'` before `go build` (pure-Go, `CGO_ENABLED=0`, so no toolchain needed). Copy `.dist/remofy-bot` to the server and run `install-linux.sh` there.

Quick lint/vet (no test suite exists). Build/vet **each** target so both platform files compile:

```
go vet ./...
gofmt -l .
GOOS=linux  go build ./...     # verify the Unix platform file
GOOS=windows go build ./...    # verify the Windows platform file
```

## Architecture

Entry point `cmd/bot/main.go` loads `.env`, builds a `bot.Manager` + `bot.Bot`, registers the slash-command menu, then long-polls Telegram and dispatches each update to `Bot.HandleUpdate` in a goroutine.

`internal/bot/` is the only package with logic:

- **handler.go** — two-tier whitelist (user IDs + chat IDs), group/private routing, slash-command dispatch, and free-text mode routing (`sess.Mode()` → `RunShell` or `RunClaude`). User-facing shell labels come from `shellLabel()` so the same UI reads "PowerShell" on Windows and "Bash" on Linux. In **groups**, the bot only responds to (a) slash commands, (b) messages mentioning `@botusername`, or (c) replies to the bot's own messages. The `@mention` substring is stripped before the text is sent on. The `⏹ Stop` reply-keyboard button is treated as `/stop`. If the incoming message is a **reply** to another message, `buildReplyContext` prepends that message's text/caption — and the *content of any attached Document* (downloaded via Telegram's file API, capped at `maxReplyDocBytes = 256KB`) — to the prompt/command, wrapped in `<reply_xabar>`/`<reply_fayl>` tags. This works in both modes (e.g. reply to a `.log` file with "why did this crash?").
- **session.go** — `Manager` lazily creates a `*Session` per Telegram **chat ID** (not user ID — groups share one session). Sessions hold `mode` (default `ModePowerShell` — the internal constant name is historical; it means "shell mode"), the active `claudeCancel`/`claudePID` for any in-flight exec (Claude or shell), the captured `claudeSessionID` for `--resume`, and a `cmdSlot` chan that serializes exec per chat. `SendInterrupt` calls `killTree(pid)` (platform-specific). Workspace is global (`BOT_WORKDIR`). If `BOT_WORKDIR` is empty, `NewManager` falls back to the user's home dir on every platform.
- **claude.go** — probes the platform's `claudeBinaryCandidates()` on PATH (lazy, cached on the session), builds the common arg list, then calls `buildClaudeCmd()` to construct the OS-specific command:
  ```
  claude -p                                  (NO inline prompt — see below)
    --output-format stream-json --include-partial-messages --verbose
    --dangerously-skip-permissions
    [--append-system-prompt <BOT_SYSTEM_PROMPT>]  (only if set)
    [--mcp-config <BOT_WORKDIR>/.mcp.json]        (only if file exists)
    [--resume <session-id>]
  ```
  with `cmd.Dir = BOT_WORKDIR`. The prompt is **not** an argument — it's delivered on `claude`'s **stdin** (Windows: read from a temp UTF-8 file inside the PowerShell wrapper; Linux: `strings.NewReader(prompt)`), dodging the Windows CreateProcess ~32K command-line cap and keeping Cyrillic/emoji intact. Parses the JSONL stream line-by-line (`stream_event` text deltas, `assistant`, `system/init`, `result`) and edit-throttles the placeholder Telegram message every 1.5s. Captured `session_id` is stashed on the session for the next `--resume`.
- **shell.go** — `RunShell(command, threadID)` calls `buildShellCmd()` for the OS-specific shell (Windows: temp `.ps1` + `Invoke-Expression`; Linux: `bash` reading the command on stdin) with `cmd.Dir = BOT_WORKDIR`. stdout and stderr are merged via `io.Pipe` and scanner-streamed to the placeholder message with the same 1.5s edit-throttle. Same queue, cancel, and `killTree` shutdown path as the Claude runner.
- **poll.go** — long-polling helper that also extracts `message_thread_id` (tgbotapi v5 doesn't expose it natively) so forum/topic groups work. `SendInThread` is the shared send helper.
- **output.go** — `stripANSI` regex (CSI/OSC + control chars, keep `\n`/`\t`), `htmlEscape` for Telegram HTML mode, and a `fileExists` helper. Telegram message cap is `maxMsgChars = 3800`; longer output is tail-truncated with a `…` prefix.

### Why PowerShell wraps `claude.exe` (Windows only)

`claude.exe` on Windows does not get a usable OAuth/keychain handle when invoked via Go's `os/exec` directly. So on Windows (`platform_windows.go`) the bot wraps the call in `powershell.exe -Command "...; [IO.File]::ReadAllText(tmp,UTF8) | & 'C:\path\claude.exe' -p --resume '...' ..."` so PowerShell attaches the right console/session/token. Both Windows wrappers (Claude and shell) prepend `$OutputEncoding=[Console]::InputEncoding=[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new();` — without it Win-PS 5.1 pipes in default ASCII and Cyrillic/emoji become `?`. `psQuote` (single-quote escaping `'` → `''`) lives in `platform_windows.go`; never string-concatenate raw paths into the wrapper.

**On Linux this whole problem disappears** (`platform_unix.go`): Claude Code reads its credentials from `~/.claude/`, so the bot execs `claude` directly with no shell wrapper. The only requirement is that the process runs as the **same user that ran `claude login`** (the `User=` in the systemd unit), since the token lives in that user's home.

### Concurrency model

- Per-chat `cmdSlot` (buffered channel size 1) — only one exec runs at a time per chat; new messages queue behind it. Same slot for Claude and shell.
- `SendInterrupt` deliberately **does not** acquire `cmdSlot` — it grabs `claudeCancel` + `claudePID` under the short `mu`, fires both (context cancel + `killTree`), and rolls the `queueGen` so anything already queued in `cmdSlot` bails on its generation `ctx.Done()`. `killTree` is process-tree on both OSes (`taskkill /F /T` on Windows; `SIGKILL` to the process group on Linux, which is why `setProcessGroup`/`setpgid` is set before `Start`).
- `claudeMaxWait = 30 min` (both runners); `claudeQueueWait = claudeMaxWait + 1 min` queue cap.
- All session state is in-memory — restarting the bot forgets every chat's mode (resets to shell), `--resume` ID, and queue. There is no persistent transcript on the bot side; Claude itself stores session state in its own dir.

### Live message editing

Both `RunClaude` and `RunShell` send a placeholder message immediately, then `editMessageText` it on a 1.5s ticker as new output arrives. Telegram's "message is not modified" error is filtered out of logs. On final edit the icon flips (🤖✍️→🤖 for Claude, 🟦✍️→🟦 for shell) and cancel/timeout reasons are prepended.

## Configuration

`.env` (or process env) — loaded by `godotenv` from cwd:

| Var | Effect |
|-----|--------|
| `TELEGRAM_BOT_TOKEN` | Required. Bot dies on startup if missing. |
| `ALLOWED_TELEGRAM_IDS` | User-level whitelist (private + inside groups). Comma/space/`;` separated int64s. Empty → open to all in private chats (logged loudly). |
| `ALLOWED_CHAT_IDS` | Group/supergroup chat-ID whitelist. Empty → bot is silent in all groups (safe default). Group IDs are negative; supergroups start with `-100`. |
| `BOT_WORKDIR` | Workspace where both shell commands and `claude` are invoked. `.mcp.json` and `.claude/settings.json` are read from here for Claude. Empty → the user's home dir (`%USERPROFILE%` / `$HOME`). |
| `BOT_SYSTEM_PROMPT` | Passed as `--append-system-prompt` to Claude (persona). Shell mode ignores it. |
| `GITHUB_PERSONAL_ACCESS_TOKEN` | Read by `.mcp.json` for the GitHub MCP server (see below). |

Slash commands are registered via `setMyCommands` on startup and listed in `BotCommands()` — keep the two in sync when adding a command. Current set: `/start /help /powershell /claude /stop /reset /workdir`. `/shell` is accepted as an alias of `/powershell` but is not in the menu.

### Group setup notes

- Telegram bots in groups have **privacy mode** on by default — they only see commands, mentions, and replies. That matches what `HandleUpdate` reacts to, so no BotFather toggle is needed.
- To wire a group: add the bot to the group, get the chat ID (denied attempts log `chat=<id>`), then add it to `ALLOWED_CHAT_IDS` and restart the bot.
- The user-level whitelist still applies inside groups: a user not in `ALLOWED_TELEGRAM_IDS` is silently ignored even in a whitelisted group.

### MCP servers (Claude mode only)

The bot passes `--mcp-config <BOT_WORKDIR>/.mcp.json` only if the file exists. Create that file in `BOT_WORKDIR` (not in this project) — see `.mcp.json.example` for the template. Default template wires the GitHub MCP server via `npx -y @modelcontextprotocol/server-github` (requires Node + a Personal Access Token in env).

For other MCP servers, follow the same `mcpServers` schema — Claude Code will load them all on every prompt and inject their tools as `mcp__<server>__<tool>` names. Because `--dangerously-skip-permissions` is set, the agent gets all of them with no per-call approval.

### Skills and `.claude/settings.json`

Anything Claude Code skills/hooks-related goes in `<BOT_WORKDIR>/.claude/settings.json`. The bot doesn't manage that file — it's just picked up by `claude` because we run with `cmd.Dir = BOT_WORKDIR`. Use it for project-level skills, allowed-tool overrides, hooks, etc.

## Conventions worth keeping

- All user-facing strings are Uzbek (Latin script). Match that voice when adding messages. Use `shellLabel()` rather than hardcoding "PowerShell" so messages read correctly on both OSes.
- Telegram messages use `ParseMode=HTML`; always pass user content through `htmlEscape` and exec output through `stripANSI` before formatting.
- OS-specific exec belongs in `platform_windows.go` / `platform_unix.go` — never put a `powershell.exe`/`taskkill`/`bash`/`syscall` reference in the shared runner code. Keep both files' function signatures identical.
- On Windows, PowerShell paths from user input get `psQuote` (single-quote with `'` → `''` escape) — never string-concatenate raw paths into the wrapper script. User commands/prompts reach the child via temp file or stdin, never inlined into the `-Command` arg (CreateProcess 32K limit + encoding issues).
- Logging goes through stdlib `log`; on Windows the wrapper redirects stderr to `bot-YYYY-MM-DD.err.log`, on Linux systemd captures it (`journalctl -u remofy-bot`). Don't add a logging framework.
