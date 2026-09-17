package httpapi

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		remoteAddr     string
		headers        map[string]string
		trustedProxies []string
		wantIP         string
	}{
		{
			name:       "direct public client ignores XFF",
			remoteAddr: "203.0.113.195:4321",
			headers: map[string]string{
				"X-Forwarded-For": "198.51.100.1",
			},
			trustedProxies: nil,
			wantIP:         "203.0.113.195",
		},
		{
			name:       "trusted proxy with X-Forwarded-For",
			remoteAddr: "172.30.0.10:43781",
			headers: map[string]string{
				"X-Forwarded-For": "198.51.100.42, 172.30.0.10",
			},
			trustedProxies: []string{"172.30.0.10/32"},
			wantIP:         "198.51.100.42",
		},
		{
			name:       "trusted proxy with X-Real-IP",
			remoteAddr: "172.30.0.10:43781",
			headers: map[string]string{
				"X-Real-IP": "198.51.100.99",
			},
			trustedProxies: []string{"172.30.0.10/32"},
			wantIP:         "198.51.100.99",
		},
		{
			name:       "untrusted peer ignores XFF",
			remoteAddr: "10.0.0.1:43781",
			headers: map[string]string{
				"X-Forwarded-For": "198.51.100.50",
			},
			trustedProxies: []string{"172.30.0.10/32"},
			wantIP:         "10.0.0.1",
		},
		{
			name:       "multi-hop XFF uses rightmost untrusted hop",
			remoteAddr: "172.30.0.10:43781",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.5, 203.0.113.6, 172.30.0.10",
			},
			trustedProxies: []string{"172.30.0.10/32"},
			wantIP:         "203.0.113.6",
		},
		{
			name:       "malformed XFF falls back to peer",
			remoteAddr: "172.30.0.10:43781",
			headers: map[string]string{
				"X-Forwarded-For": "invalid-ip",
				"X-Real-IP":       "198.51.100.88",
			},
			trustedProxies: []string{"172.30.0.10/32"},
			wantIP:         "172.30.0.10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/v1/auth/login", nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			var trusted []netip.Prefix
			for _, p := range tt.trustedProxies {
				prefix, _ := netip.ParsePrefix(p)
				trusted = append(trusted, prefix)
			}
			got := clientIP(req, trusted)
			if got != tt.wantIP {
				t.Fatalf("clientIP() = %q, want %q", got, tt.wantIP)
			}
		})
	}
}

func TestRateLimiterBlocksExcessiveRequests(t *testing.T) {
	t.Parallel()

	limiter := newIPRateLimiter(60, 2)
	defer close(limiter.stopEvict)

	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := func() int {
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
		r.RemoteAddr = "198.51.100.5:1234"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}

	// Burst is 2
	if code := req(); code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", code)
	}
	if code := req(); code != http.StatusOK {
		t.Fatalf("second request status = %d, want 200", code)
	}
	// 3rd request exceeds burst limit
	if code := req(); code != http.StatusTooManyRequests {
		t.Fatalf("third request status = %d, want 429", code)
	}
}
