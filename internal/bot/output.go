package bot

import (
	"regexp"
	"strings"
)

// ANSI escape sequences (CSI, OSC, va boshqalar) — Telegram'da ko'rsatilmaydi.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][AB012]|\x1b[=>]|[\x00-\x08\x0b-\x0c\x0e-\x1f\x7f]`)

// stripANSI ANSI/control kodlarni olib tashlaydi (\n va \t qoldiriladi).
func stripANSI(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// Telegram HTML uchun escape.
var htmlReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func htmlEscape(s string) string { return htmlReplacer.Replace(s) }
