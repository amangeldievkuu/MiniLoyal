package auth

import (
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/amangeldievkuu/miniloyal/internal/platform/clock"
)

// Limiter is a set of token buckets keyed by a string (client IP, email).
// Idle buckets are pruned so the map stays bounded.
type Limiter struct {
	clock clock.Clock
	limit rate.Limit
	burst int
	idle  time.Duration

	mu        sync.Mutex
	entries   map[string]*bucket
	lastPrune time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewLimiter allows perMinute events per key with the given burst.
func NewLimiter(clk clock.Clock, perMinute, burst int) *Limiter {
	if clk == nil {
		clk = clock.Real{}
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		clock:     clk,
		limit:     rate.Limit(float64(perMinute) / 60),
		burst:     burst,
		idle:      10 * time.Minute,
		entries:   make(map[string]*bucket),
		lastPrune: clk.Now(),
	}
}

// Allow consumes one event for key. When the bucket is empty it returns
// false and how long the caller should wait.
func (l *Limiter) Allow(key string) (time.Duration, bool) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.entries[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(l.limit, l.burst)}
		l.entries[key] = b
	}
	b.seen = now
	l.pruneLocked(now)

	res := b.lim.ReserveN(now, 1)
	if !res.OK() {
		return time.Minute, false
	}
	if delay := res.DelayFrom(now); delay > 0 {
		res.CancelAt(now)
		return delay, false
	}
	return 0, true
}

// pruneLocked drops buckets idle longer than l.idle, at most once a minute.
func (l *Limiter) pruneLocked(now time.Time) {
	if now.Sub(l.lastPrune) < time.Minute {
		return
	}
	l.lastPrune = now
	for k, b := range l.entries {
		if now.Sub(b.seen) > l.idle {
			delete(l.entries, k)
		}
	}
}

// Len reports how many keys are tracked (for tests).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}
