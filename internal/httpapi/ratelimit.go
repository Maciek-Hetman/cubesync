package httpapi

import (
	"net"
	"net/http"
	"strings"
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

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			client := strings.TrimSpace(parts[0])
			if client != "" {
				if parsed := net.ParseIP(client); parsed != nil {
					return client
				}
			}
		}
		if xrip := strings.TrimSpace(r.Header.Get("X-Real-IP")); xrip != "" {
			if parsed := net.ParseIP(xrip); parsed != nil {
				return xrip
			}
		}
	}
	return host
}

func (l *ipRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := clientIP(r)
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
			writeJSON(w, http.StatusTooManyRequests, errorBody{Error: errorDetail{Code: "rate_limited", Message: "too many requests"}})
			return
		}
		next.ServeHTTP(w, r)
	})
}
