//go:build windows

package bot

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// claudeBinaryCandidates — Windows'da `claude` bir nechta ko'rinishda bo'lishi mumkin.
func claudeBinaryCandidates() []string {
	return []string{"claude", "claude.cmd", "claude.exe", "claude.bat"}
}

// shellLabel — shell rejimining foydalanuvchiga ko'rsatiladigan nomi.
func shellLabel() string { return "PowerShell" }

// buildClaudeCmd Claude'ni PowerShell orqali wrap qiladi — Windows'da Go'ning
// to'g'ridan-to'g'ri exec'i claude.exe'ga OAuth/keychain handle'ni to'liq
// pass qilmas ekan. Promptni temp UTF-8 faylga yozib, claude.exe stdin'iga
// pipe qilamiz (CreateProcess ~32K command-line cheklovidan oshib ketmaslik
// va Cyrillic/emoji buzilmasligi uchun). $OutputEncoding/[Console]::*Encoding
// UTF-8'ga majburlanadi — aks holda Win-PS 5.1 default ASCII'da pipe qiladi.
func buildClaudeCmd(ctx context.Context, binary, prompt string, args []string) (*exec.Cmd, func(), error) {
	tmpPath, cleanup, err := writeTempFile(prompt, "remofy-prompt-*.txt")
	if err != nil {
		return nil, func() {}, err
	}
	parts := make([]string, 0, len(args)+5)
	parts = append(parts,
		"$OutputEncoding=[Console]::InputEncoding=[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new();",
		"[System.IO.File]::ReadAllText("+psQuote(tmpPath)+",[System.Text.Encoding]::UTF8)",
		"|", "&", psQuote(binary))
	for _, a := range args {
		parts = append(parts, psQuote(a))
	}
	psCmd := strings.Join(parts, " ")
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NoLogo", "-NonInteractive", "-Command", psCmd)
	return cmd, cleanup, nil
}

// buildShellCmd foydalanuvchi matnini temp .ps1 faylga yozib, Invoke-Expression
// orqali bajaradi (script-block — foydalanuvchi yozgan sintaksis saqlanadi).
func buildShellCmd(ctx context.Context, command string) (*exec.Cmd, func(), error) {
	tmpPath, cleanup, err := writeTempFile(command, "remofy-pscmd-*.ps1")
	if err != nil {
		return nil, func() {}, err
	}
	psCmd := "$OutputEncoding=[Console]::InputEncoding=[Console]::OutputEncoding=[System.Text.UTF8Encoding]::new(); " +
		"Invoke-Expression ([System.IO.File]::ReadAllText(" + psQuote(tmpPath) + ",[System.Text.Encoding]::UTF8))"
	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NoProfile", "-NoLogo", "-NonInteractive", "-Command", psCmd)
	return cmd, cleanup, nil
}

// setProcessGroup Windows'da no-op — process daraxti taskkill /T orqali o'ldiriladi.
func setProcessGroup(cmd *exec.Cmd) {}

// killTree butun process daraxtini (PowerShell + claude.exe + node MCP child'lari)
// o'ldiradi. /T — daraxt, /F — majburiy.
func killTree(pid int) {
	_ = exec.Command("taskkill.exe", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}

// writeTempFile matnni vaqtinchalik faylga yozadi va tozalovchi func qaytaradi.
func writeTempFile(content, pattern string) (string, func(), error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

// psQuote wraps s in PowerShell single quotes, doubling any embedded quote.
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
