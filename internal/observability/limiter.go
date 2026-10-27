package observability

import (
	"sync"
	"time"
)

// maxKeys bounds the limiter's memory when log messages are built dynamically.
const maxKeys = 1024

type limiter struct {
	mu          sync.Mutex
	last        map[string]time.Time
	suppressed  map[string]int
	windowStart time.Time
	windowCount int
}

func newLimiter() *limiter {
	return &limiter{last: map[string]time.Time{}, suppressed: map[string]int{}}
}

// allow reports whether an event of key may be sent now, and how many of its repeats were held
// back since the last one sent. budget > 0 also counts the event against the hourly budget.
func (l *limiter) allow(key string, every time.Duration, budget int, now time.Time) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.last[key]; ok && now.Sub(last) < every {
		l.suppressed[key]++
		return false, 0
	}
	if budget > 0 {
		if now.Sub(l.windowStart) >= time.Hour {
			l.windowStart, l.windowCount = now, 0
		}
		if l.windowCount >= budget {
			l.suppressed[key]++
			return false, 0
		}
		l.windowCount++
	}
	if len(l.last) >= maxKeys {
		l.last, l.suppressed = map[string]time.Time{}, map[string]int{}
	}
	n := l.suppressed[key]
	delete(l.suppressed, key)
	l.last[key] = now
	return true, n
}
