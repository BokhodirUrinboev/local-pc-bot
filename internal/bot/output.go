package bot

import (
	"regexp"
	"strings"
	"sync"
)

// ANSI escape sequences (CSI, OSC, va boshqalar) — Telegram'da ko'rsatilmaydi.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][AB012]|\x1b[=>]|[\x00-\x08\x0b-\x0c\x0e-\x1f\x7f]`)

// stripANSI ANSI/control kodlarni olib tashlaydi (CR, TAB, LF qoldiriladi).
func stripANSI(s string) string {
	// \r ko'pincha "satrni qaytadan yozish" uchun ishlatiladi (progress barlar).
	// Telegram uchun \r ni o'chirib, faqat oxirgi versiya qoldirilsa yaxshi bo'ladi —
	// lekin hozir oddiy strip qilamiz.
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// Telegram HTML uchun escape.
var htmlReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func htmlEscape(s string) string { return htmlReplacer.Replace(s) }

// Buffer — sessiya stdout'idan kelayotgan satrlarni saqlaydigan ring buffer.
// Snapshot() Telegram xabariga mos keladigan formatlangan matn qaytaradi.
type Buffer struct {
	mu       sync.Mutex
	lines    []string
	maxLines int
	partial  string // hali \n bilan tugamagan oxirgi satr
}

func NewBuffer(maxLines int) *Buffer {
	return &Buffer{maxLines: maxLines}
}

// Append SSH stdout dan kelgan baytlarni qo'shadi. Strip qilinadi va satrlarga bo'linadi.
func (b *Buffer) Append(p []byte) {
	if len(p) == 0 {
		return
	}
	clean := stripANSI(string(p))
	b.mu.Lock()
	defer b.mu.Unlock()
	combined := b.partial + clean
	parts := strings.Split(combined, "\n")
	// Oxirgi qism — hali tugallanmagan satr
	b.partial = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		b.lines = append(b.lines, l)
	}
	if len(b.lines) > b.maxLines {
		b.lines = b.lines[len(b.lines)-b.maxLines:]
	}
}

// Snapshot HTML escape qilingan matnni qaytaradi (so'nggi maxLines + tugallanmagan satr).
// Telegram 4096 char limitiga sig'maslik uchun boshidan kesiladi.
func (b *Buffer) Snapshot() string {
	const maxChars = 3800 // <pre>...</pre> tag uchun joy qoldiramiz
	b.mu.Lock()
	defer b.mu.Unlock()
	all := strings.Join(b.lines, "\n")
	if b.partial != "" {
		if all != "" {
			all += "\n"
		}
		all += b.partial
	}
	if len(all) > maxChars {
		all = "…" + all[len(all)-maxChars:]
	}
	return htmlEscape(all)
}

// Reset bufferni bo'shatadi (yangi anchor xabar boshlanganda).
func (b *Buffer) Reset() {
	b.mu.Lock()
	b.lines = nil
	b.partial = ""
	b.mu.Unlock()
}

// IsEmpty hech narsa yozilmaganmi?
func (b *Buffer) IsEmpty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.lines) == 0 && b.partial == ""
}
