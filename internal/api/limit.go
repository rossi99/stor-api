package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// limiter is a token bucket per key: burst requests at once, then one more every interval.
// shortcut: in memory per process, so limits reset on restart and aren't shared; move to Postgres or Redis when running more than one instance.
type limiter struct {
	mu      sync.Mutex
	burst   float64
	every   time.Duration
	now     func() time.Time
	buckets map[string]bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(burst int, every time.Duration) *limiter {
	return &limiter{burst: float64(burst), every: every, now: time.Now, buckets: map[string]bucket{}}
}

func (l *limiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	refill := func(b bucket) float64 { return min(l.burst, b.tokens+float64(now.Sub(b.at))/float64(l.every)) }
	if len(l.buckets) > 10_000 {
		// Full buckets carry no state worth keeping.
		for k, b := range l.buckets {
			if refill(b) >= l.burst {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[key]
	tokens := l.burst
	if ok {
		tokens = refill(b)
	}
	if tokens < 1 {
		l.buckets[key] = bucket{tokens: tokens, at: now}
		return false
	}
	l.buckets[key] = bucket{tokens: tokens - 1, at: now}
	return true
}

// limited caps password-checking endpoints per client address, which also bounds the PBKDF2 work
// one client can cause. Keying by address rather than email means nobody can lock someone else out.
// shortcut: uses the connection's address; behind a proxy, read X-Forwarded-For from the trusted proxy instead.
func (s *Server) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		if !s.attempts.allow(host) {
			fail(w, http.StatusTooManyRequests, "too many attempts, try again shortly")
			return
		}
		next(w, r)
	}
}
