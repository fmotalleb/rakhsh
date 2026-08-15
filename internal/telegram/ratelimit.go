package telegram

import (
	"sync"
	"time"
)

// chatEditLimiter throttles editMessageText calls per chat so that concurrent
// downloads sharing one chat do not trip Telegram flood control (HTTP 429).
type chatEditLimiter struct {
	mu      sync.Mutex
	lastAt  map[int64]time.Time
	minimum time.Duration
}

func newChatEditLimiter(minimum time.Duration) *chatEditLimiter {
	return &chatEditLimiter{
		lastAt:  make(map[int64]time.Time),
		minimum: minimum,
	}
}

// allow reports whether an edit for chatID may proceed now. When it returns
// false the caller should skip the edit and retry on the next progress tick.
func (l *chatEditLimiter) allow(chatID int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if next := l.lastAt[chatID].Add(l.minimum); now.Before(next) {
		return false
	}
	l.lastAt[chatID] = now
	return true
}
