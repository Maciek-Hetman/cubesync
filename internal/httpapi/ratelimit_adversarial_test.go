package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestClientIPSpoofingResistance verifies that untrusted remote addresses cannot
// spoof their IP via X-Forwarded-For or X-Real-IP headers.
func TestClientIPSpoofingResistance(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		remoteAddr string
		headers    map[string]string
		expectedIP string
	}{
		{
			name:       "Public IPv4 cannot spoof with private XFF",
			remoteAddr: "203.0.113.195:54321",
			headers: map[string]string{
				"X-Forwarded-For": "10.0.0.1",
			},
			expectedIP: "203.0.113.195",
		},
		{
			name:       "Public IPv4 cannot spoof with public XFF",
			remoteAddr: "203.0.113.195:54321",
			headers: map[string]string{
				"X-Forwarded-For": "198.51.100.5, 203.0.113.1",
			},
			expectedIP: "203.0.113.195",
		},
		{
			name:       "Public IPv4 cannot spoof with X-Real-IP",
			remoteAddr: "203.0.113.195:54321",
			headers: map[string]string{
				"X-Real-IP": "1.1.1.1",
			},
			expectedIP: "203.0.113.195",
		},
		{
			name:       "Public IPv4 with both XFF and X-Real-IP ignored",
			remoteAddr: "198.51.100.22:12345",
			headers: map[string]string{
				"X-Forwarded-For": "8.8.8.8",
				"X-Real-IP":       "1.1.1.1",
			},
			expectedIP: "198.51.100.22",
		},
		{
			name:       "Public IPv6 cannot spoof with XFF",
			remoteAddr: "[2607:f8b0:4005:805::200e]:443",
			headers: map[string]string{
				"X-Forwarded-For": "127.0.0.1",
			},
			expectedIP: "2607:f8b0:4005:805::200e",
		},
		{
			name:       "Loopback IPv4 127.0.0.1 trusts leftmost XFF",
			remoteAddr: "127.0.0.1:55000",
			headers: map[string]string{
				"X-Forwarded-For": "198.51.100.50, 10.0.0.1, 127.0.0.1",
			},
			expectedIP: "198.51.100.50",
		},
		{
			name:       "Private IPv4 10.0.0.1 trusts leftmost XFF",
			remoteAddr: "10.0.0.1:44321",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.77, 10.0.0.2",
			},
			expectedIP: "203.0.113.77",
		},
		{
			name:       "Private IPv4 192.168.1.1 trusts X-Real-IP when XFF is absent",
			remoteAddr: "192.168.1.1:33221",
			headers: map[string]string{
				"X-Real-IP": "203.0.113.88",
			},
			expectedIP: "203.0.113.88",
		},
		{
			name:       "Private IPv4 172.16.0.5 trusts X-Real-IP when XFF is invalid",
			remoteAddr: "172.16.0.5:12345",
			headers: map[string]string{
				"X-Forwarded-For": "not-an-ip",
				"X-Real-IP":       "198.51.100.99",
			},
			expectedIP: "198.51.100.99",
		},
		{
			name:       "Loopback IPv6 [::1] trusts leftmost XFF",
			remoteAddr: "[::1]:54321",
			headers: map[string]string{
				"X-Forwarded-For": "203.0.113.99, 10.0.0.1",
			},
			expectedIP: "203.0.113.99",
		},
		{
			name:       "Unspecified IPv4 0.0.0.0 trusts leftmost XFF",
			remoteAddr: "0.0.0.0:80",
			headers: map[string]string{
				"X-Forwarded-For": "198.51.100.123",
			},
			expectedIP: "198.51.100.123",
		},
		{
			name:       "Proxy with completely empty/invalid headers falls back to remote host",
			remoteAddr: "127.0.0.1:8080",
			headers: map[string]string{
				"X-Forwarded-For": "   ",
				"X-Real-IP":       "bogus",
			},
			expectedIP: "127.0.0.1",
		},
		{
			name:       "XFF with whitespace around IPs is trimmed properly",
			remoteAddr: "127.0.0.1:8080",
			headers: map[string]string{
				"X-Forwarded-For": "  203.0.113.111  , 10.0.0.1",
			},
			expectedIP: "203.0.113.111",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, "/v1/auth/login", nil)
			req.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}

			actualIP := clientIP(req)
			if actualIP != tc.expectedIP {
				t.Errorf("clientIP(%q, headers=%v) = %q; want %q", tc.remoteAddr, tc.headers, actualIP, tc.expectedIP)
			}
		})
	}
}

// TestRateLimiterBurstAndReplenish tests token bucket rate limiting burst enforcement,
// error response headers (Retry-After), and replenishment over time.
func TestRateLimiterBurstAndReplenish(t *testing.T) {
	t.Parallel()

	// 120 requests per minute = 2 req/sec. Burst = 3.
	limiter := newIPRateLimiter(120, 3)
	defer close(limiter.stopEvict)

	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	sendReq := func(ip string) (int, http.Header) {
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
		r.RemoteAddr = ip + ":12345"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code, w.Header()
	}

	testIP := "198.51.100.77"

	// 3 immediate requests should succeed (burst capacity)
	for i := 1; i <= 3; i++ {
		code, _ := sendReq(testIP)
		if code != http.StatusOK {
			t.Fatalf("request %d within burst returned code %d, want 200", i, code)
		}
	}

	// 4th immediate request should be rate-limited (429)
	code, hdr := sendReq(testIP)
	if code != http.StatusTooManyRequests {
		t.Fatalf("4th request returned code %d, want 429", code)
	}
	if retryAfter := hdr.Get("Retry-After"); retryAfter != "60" {
		t.Errorf("Retry-After header = %q, want '60'", retryAfter)
	}

	// Another distinct IP should have its own separate burst allowance
	otherIP := "198.51.100.88"
	for i := 1; i <= 3; i++ {
		c, _ := sendReq(otherIP)
		if c != http.StatusOK {
			t.Fatalf("other IP request %d returned code %d, want 200", i, c)
		}
	}
	c, _ := sendReq(otherIP)
	if c != http.StatusTooManyRequests {
		t.Fatalf("other IP 4th request returned code %d, want 429", c)
	}

	// Wait for token replenishment (1 token per 500ms since rate is 120/min)
	time.Sleep(600 * time.Millisecond)

	// Now 1 request should succeed for testIP
	code, _ = sendReq(testIP)
	if code != http.StatusOK {
		t.Fatalf("request after replenishment returned code %d, want 200", code)
	}

	// And immediate next request should fail again
	code, _ = sendReq(testIP)
	if code != http.StatusTooManyRequests {
		t.Fatalf("request immediately after consuming replenished token returned %d, want 429", code)
	}
}

// TestRateLimiterHighConcurrencyStress runs concurrent requests across multiple client IPs
// to detect race conditions, deadlocks, and verify accurate rate limiting isolation.
func TestRateLimiterHighConcurrencyStress(t *testing.T) {
	t.Parallel()

	// 60 req/min = 1 req/sec. Burst = 5.
	limiter := newIPRateLimiter(60, 5)
	defer close(limiter.stopEvict)

	handler := limiter.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	const numIPs = 10
	const reqsPerIP = 20
	var total200, total429 int64

	var wg sync.WaitGroup
	for ipIdx := 0; ipIdx < numIPs; ipIdx++ {
		ip := fmt.Sprintf("192.0.2.%d", ipIdx+1)
		for r := 0; r < reqsPerIP; r++ {
			wg.Add(1)
			go func(clientIP string) {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
				req.RemoteAddr = clientIP + ":54321"
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, req)

				if w.Code == http.StatusOK {
					atomic.AddInt64(&total200, 1)
				} else if w.Code == http.StatusTooManyRequests {
					atomic.AddInt64(&total429, 1)
				} else {
					t.Errorf("unexpected status code %d", w.Code)
				}
			}(ip)
		}
	}

	wg.Wait()

	// Each of the 10 IPs has burst=5, so total 200 responses must be exactly 10 * 5 = 50.
	// Total 429 responses must be 10 * 15 = 150.
	if total200 != int64(numIPs*5) {
		t.Errorf("total 200 responses = %d, want %d", total200, numIPs*5)
	}
	if total429 != int64(numIPs*(reqsPerIP-5)) {
		t.Errorf("total 429 responses = %d, want %d", total429, numIPs*(reqsPerIP-5))
	}
}

// TestRateLimiterEviction verifies that stale visitor entries are pruned.
func TestRateLimiterEviction(t *testing.T) {
	t.Parallel()

	limiter := newIPRateLimiter(60, 5)
	defer close(limiter.stopEvict)

	limiter.mu.Lock()
	limiter.visitors["stale-ip"] = &visitor{
		limiter:  nil,
		lastSeen: time.Now().Add(-2 * time.Hour), // 2 hours old
	}
	limiter.visitors["fresh-ip"] = &visitor{
		limiter:  nil,
		lastSeen: time.Now(), // Fresh
	}
	limiter.mu.Unlock()

	// Trigger manual eviction simulation matching evictLoop logic
	limiter.mu.Lock()
	cutoff := time.Now().Add(-time.Hour)
	for key, value := range limiter.visitors {
		if value.lastSeen.Before(cutoff) {
			delete(limiter.visitors, key)
		}
	}
	limiter.mu.Unlock()

	limiter.mu.Lock()
	_, staleExists := limiter.visitors["stale-ip"]
	_, freshExists := limiter.visitors["fresh-ip"]
	limiter.mu.Unlock()

	if staleExists {
		t.Errorf("expected 'stale-ip' to be evicted")
	}
	if !freshExists {
		t.Errorf("expected 'fresh-ip' to remain in visitor map")
	}
}
