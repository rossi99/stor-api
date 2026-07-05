package server

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"stor.app/api/internal/httpx"
)

// ipRateLimiter is a per-IP token bucket used on the unauthenticated abuse
// surfaces (credential guessing on /auth/*, invite-code guessing on join).
// In-memory is the right scope while the API runs as a single instance; swap
// for a shared store if it ever scales out.
type ipRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     rate.Limit
	burst    int
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPRateLimiter(perMinute int) *ipRateLimiter {
	l := &ipRateLimiter{
		visitors: make(map[string]*visitor),
		rate:     rate.Limit(float64(perMinute) / 60.0),
		burst:    perMinute,
	}
	go l.janitor()
	return l
}

func (l *ipRateLimiter) janitor() {
	for range time.Tick(5 * time.Minute) {
		l.mu.Lock()
		for ip, v := range l.visitors {
			if time.Since(v.lastSeen) > 10*time.Minute {
				delete(l.visitors, ip)
			}
		}
		l.mu.Unlock()
	}
}

func (l *ipRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	v, ok := l.visitors[ip]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(l.rate, l.burst)}
		l.visitors[ip] = v
	}
	v.lastSeen = time.Now()
	return v.limiter.Allow()
}

func (l *ipRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		if !l.allow(ip) {
			w.Header().Set("Retry-After", "60")
			httpx.Error(w, http.StatusTooManyRequests, "rate_limited", "too many requests, slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}
