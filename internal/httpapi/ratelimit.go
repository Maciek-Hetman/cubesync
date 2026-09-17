package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ipRateLimiter struct {
	mu             sync.Mutex
	visitors       map[string]*visitor
	rate           rate.Limit
	burst          int
	stopEvict      chan struct{}
	trustedProxies []netip.Prefix
}

type visitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPRateLimiter(eventsPerMinute, burst int) *ipRateLimiter {
	return newIPRateLimiterWithProxies(eventsPerMinute, burst, nil)
}

func newIPRateLimiterWithProxies(eventsPerMinute, burst int, trustedProxies []netip.Prefix) *ipRateLimiter {
	l := &ipRateLimiter{
		visitors:       make(map[string]*visitor),
		rate:           rate.Every(time.Minute / time.Duration(eventsPerMinute)),
		burst:          burst,
		stopEvict:      make(chan struct{}),
		trustedProxies: trustedProxies,
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

func clientIP(r *http.Request, trustedProxies []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !isTrustedProxy(peer, trustedProxies) {
		return peer.String()
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			addr, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
			if err != nil {
				// Entries left of a malformed hop cannot be attributed to a trusted proxy.
				return peer.String()
			}
			addr = addr.Unmap()
			if !isTrustedProxy(addr, trustedProxies) {
				return addr.String()
			}
		}
		return peer.String()
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return addr.Unmap().String()
	}
	return peer.String()
}

func isTrustedProxy(addr netip.Addr, trustedProxies []netip.Prefix) bool {
	for _, prefix := range trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func (l *ipRateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := clientIP(r, l.trustedProxies)
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
