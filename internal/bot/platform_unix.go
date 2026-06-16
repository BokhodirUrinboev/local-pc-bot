//go:build !windows

package bot

import (
	"context"
	"os/exec"
	"strings"
	"syscall"
)

// claudeBinaryCandidates — Unix'da oddiy `claude`.
func claudeBinaryCandidates() []string {
	return []string{"claude"}
}

// shellLabel — shell rejimining foydalanuvchiga ko'rsatiladigan nomi.
func shellLabel() string { return "Bash" }

// buildClaudeCmd Unix'da claude'ni to'g'ridan-to'g'ri exec qiladi — Windows'dagi
// PowerShell OAuth-wrapper hack kerak emas (creds `~/.claude/` dan o'qiladi).
// Prompt stdin orqali beriladi (`claude -p` argumentsiz stdin'ni o'qiydi).
func buildClaudeCmd(ctx context.Context, binary, prompt string, args []string) (*exec.Cmd, func(), error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Stdin = strings.NewReader(prompt)
	return cmd, func() {}, nil
}

// buildShellCmd foydalanuvchi matnini bash'ga stdin orqali script sifatida beradi
// (ARG_MAX cheklovidan xoli; non-interactive, stdin tty emas).
func buildShellCmd(ctx context.Context, command string) (*exec.Cmd, func(), error) {
	cmd := exec.CommandContext(ctx, "bash")
	cmd.Stdin = strings.NewReader(command)
	return cmd, func() {}, nil
}

// setProcessGroup bolani yangi process group leader qiladi — shunda killTree
// butun guruhga (bola + uning child'lari, masalan node MCP) signal yubora oladi.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree process guruhini butunlay o'ldiradi. Manfiy PID — guruh (setProcessGroup
// orqali pgid == pid).
func killTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
