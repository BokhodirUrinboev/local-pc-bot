package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

const linkTTL = 10 * time.Minute

type pendingLink struct {
	telegramID int64
	username   string
	createdAt  time.Time
}

var (
	pendingMu sync.Mutex
	pending   = map[string]pendingLink{}
)

// NewLinkToken yangi state token yaratadi va Telegram ID ga bog'laydi.
func NewLinkToken(telegramID int64, username string) string {
	b := make([]byte, 18)
	rand.Read(b)
	state := base64.URLEncoding.EncodeToString(b)

	pendingMu.Lock()
	pending[state] = pendingLink{telegramID: telegramID, username: username, createdAt: time.Now()}
	pendingMu.Unlock()
	return state
}

// ConsumeLinkToken state ni topib oladi va o'chiradi. TTL tekshiriladi.
func ConsumeLinkToken(state string) (telegramID int64, username string, ok bool) {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	p, found := pending[state]
	if !found {
		return 0, "", false
	}
	delete(pending, state)
	if time.Since(p.createdAt) > linkTTL {
		return 0, "", false
	}
	return p.telegramID, p.username, true
}

// peekLinkToken — state mavjud (va TTL ichida) ekanini tekshiradi, lekin o'chirmaydi.
func peekLinkToken(state string) bool {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	p, ok := pending[state]
	if !ok {
		return false
	}
	if time.Since(p.createdAt) > linkTTL {
		delete(pending, state)
		return false
	}
	return true
}

// StartGC eskirgan state'larni vaqt-vaqti bilan tozalaydi.
func StartGC() {
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for range t.C {
			pendingMu.Lock()
			for k, v := range pending {
				if time.Since(v.createdAt) > linkTTL {
					delete(pending, k)
				}
			}
			pendingMu.Unlock()
		}
	}()
}
