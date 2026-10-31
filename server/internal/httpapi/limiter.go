package httpapi

import (
	"sync"
	"time"
)

type bucket struct {
	tokens  float64
	touched time.Time
}
type limiter struct {
	mu        sync.Mutex
	items     map[string]bucket
	capacity  int
	nextSweep time.Time
}

func newLimiter(capacity int) *limiter {
	return &limiter{items: make(map[string]bucket), capacity: capacity}
}

// Unknown keys fail closed when full; never evict an active attacker's depleted bucket.
func (l *limiter) allow(key string, rate, burst float64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if now.After(l.nextSweep) {
		for key, value := range l.items {
			if now.Sub(value.touched) > 30*time.Minute {
				delete(l.items, key)
			}
		}
		l.nextSweep = now.Add(time.Minute)
	}
	b, ok := l.items[key]
	if !ok {
		if len(l.items) >= l.capacity {
			return false
		}
		b = bucket{tokens: burst, touched: now}
	}
	b.tokens = min(burst, b.tokens+now.Sub(b.touched).Seconds()*rate)
	b.touched = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	l.items[key] = b
	return allowed
}

type connectionBudget struct {
	mu              sync.Mutex
	count, max      int
	byIP, byVisitor map[string]int
}

func (b *connectionBudget) acquire(ip, visitor string) (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.count >= b.max || b.byIP[ip] >= 8 || (visitor != "" && b.byVisitor[visitor] >= 2) {
		return nil, false
	}
	b.count++
	b.byIP[ip]++
	if visitor != "" {
		b.byVisitor[visitor]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.count--
			b.byIP[ip]--
			if b.byIP[ip] == 0 {
				delete(b.byIP, ip)
			}
			if visitor != "" {
				b.byVisitor[visitor]--
				if b.byVisitor[visitor] == 0 {
					delete(b.byVisitor, visitor)
				}
			}
		})
	}, true
}
