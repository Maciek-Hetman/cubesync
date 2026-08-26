package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ipRateLimiter struct {
	mu        sync.Mutex
	visitors  map[string]*visitor
	rate      rate.Limit
	burst     int
	stopEvict chan struct{}
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPRateLimiter(eventsPerMinute, burst int) *ipRateLimiter {
	l := &ipRateLimiter{
		visitors:  make(map[string]*visitor),
		rate:      rate.Every(time.Minute / time.Duration(eventsPerMinute)),
		burst:     burst,
		stopEvict: make(chan struct{}),
	}
	go l.evictLoop()
	return l
}

func (l *ipRateLimiter) evictLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			l.mu.Lock()
			cutoff := time.Now().Add(-time.Hour)
			for key, value := range l.visitors {
				if value.lastSeen.Before(cutoff) {
					delete(l.visitors, key)
				}
			}
			l.mu.Unlock()
		case <-l.stopEvict:
			return
		}
	}
}

func (l *ipRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		l.mu.Lock()
		entry, ok := l.visitors[host]
		if !ok {
			entry = &visitor{limiter: rate.NewLimiter(l.rate, l.burst)}
			l.visitors[host] = entry
		}
		entry.lastSeen = time.Now()
		allowed := entry.limiter.Allow()
		l.mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
