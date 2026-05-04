package bot

import (
	"regexp"
	"strings"
	"sync"
)

// ANSI escape sequences (CSI, OSC, va boshqalar) — Telegram'da ko'rsatilmaydi.
var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]|\x1b\][^\x07]*\x07|\x1b[()][AB012]|\x1b[=>]|[\x00-\x08\x0b-\x0c\x0e-\x1f\x7f]`)

// Screen-clear / alt-screen-exit sequences — bularni ko'rganda buffer'ni reset qilamiz,
// shunda foydalanuvchining `clear` (yoki htop'dan chiqishi) ishlaganday tuyuladi.
//   \x1b[2J / \x1b[3J — ekranni tozalash (clear, reset)
//   \x1bc            — full reset (RIS)
//   \x1b[?1049h/l    — alt screen buffer kirish/chiqish (htop, vim, less, nano)
var clearScreenRe = regexp.MustCompile(`\x1b\[[23]J|\x1bc|\x1b\[\?1049[hl]`)

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
// Agar oqimda screen-clear sequence bo'lsa, undan oldingi hamma narsa tashlab yuboriladi.
func (b *Buffer) Append(p []byte) {
	if len(p) == 0 {
		return
	}
	raw := string(p)

	b.mu.Lock()
	defer b.mu.Unlock()

	// Clear sequence aniqlansa — buffer reset, faqat oxirgi clear'dan KEYINGI matn qoladi.
	if locs := clearScreenRe.FindAllStringIndex(raw, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		b.lines = nil
		b.partial = ""
		raw = raw[last[1]:]
	}

	clean := stripANSI(raw)
	combined := b.partial + clean
	parts := strings.Split(combined, "\n")
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
