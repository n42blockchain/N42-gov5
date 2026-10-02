package jsonrpc

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDefaultRateLimitConfig(t *testing.T) {
	cfg := DefaultRateLimitConfig()
	if cfg.RequestsPerSecond <= 0 || cfg.BurstSize <= 0 {
		t.Fatalf("expected positive defaults, got %+v", cfg)
	}
}

// TestNewRateLimiter_FillsZeroDefaults covers NewRateLimiter's
// defaulting branches for a partially-specified config, plus Stop
// terminating the cleanup goroutine cleanly.
func TestNewRateLimiter_FillsZeroDefaults(t *testing.T) {
	rl := NewRateLimiter(&RateLimitConfig{RequestsPerSecond: 10})
	defer rl.Stop()

	if rl.config.CleanupInterval != time.Minute {
		t.Errorf("CleanupInterval default: got %v, want 1m", rl.config.CleanupInterval)
	}
	if rl.config.EntryTTL != 5*time.Minute {
		t.Errorf("EntryTTL default: got %v, want 5m", rl.config.EntryTTL)
	}
	if rl.config.BurstSize != 20 {
		t.Errorf("BurstSize default: got %d, want 20 (2x RequestsPerSecond)", rl.config.BurstSize)
	}

	// nil config falls back entirely to DefaultRateLimitConfig.
	rl2 := NewRateLimiter(nil)
	defer rl2.Stop()
	if rl2.config.RequestsPerSecond != 100 {
		t.Errorf("nil-config RequestsPerSecond: got %d, want 100", rl2.config.RequestsPerSecond)
	}
}

// TestRateLimiter_AllowBurstThenThrottles drives Allow() through its
// three branches: first-seen IP (grant + seed bucket), within burst
// (grant, decrement), and exhausted (deny).
func TestRateLimiter_AllowBurstThenThrottles(t *testing.T) {
	rl := NewRateLimiter(&RateLimitConfig{RequestsPerSecond: 1, BurstSize: 2, CleanupInterval: time.Hour, EntryTTL: time.Hour})
	defer rl.Stop()

	ip := "203.0.113.5"
	if !rl.Allow(ip) {
		t.Fatal("first request for a new IP should be allowed")
	}
	if !rl.Allow(ip) {
		t.Fatal("second request within burst should be allowed")
	}
	if rl.Allow(ip) {
		t.Fatal("third immediate request should exceed burst and be denied")
	}
}

func TestRateLimiter_CleanupExpiresOldEntries(t *testing.T) {
	rl := NewRateLimiter(&RateLimitConfig{RequestsPerSecond: 5, BurstSize: 5, CleanupInterval: 10 * time.Millisecond, EntryTTL: 20 * time.Millisecond})
	defer rl.Stop()

	rl.Allow("198.51.100.9")
	rl.mu.Lock()
	n := len(rl.entries)
	rl.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 entry after Allow, got %d", n)
	}

	time.Sleep(100 * time.Millisecond)

	rl.mu.Lock()
	n = len(rl.entries)
	rl.mu.Unlock()
	if n != 0 {
		t.Errorf("expected the cleanup goroutine to evict the expired entry, got %d entries left", n)
	}
}

func TestParseCIDRs_AcceptsBareIPsAndDropsInvalid(t *testing.T) {
	nets := ParseCIDRs([]string{"10.0.0.0/8", "192.168.1.1", "not-an-ip", "", "  "})
	if len(nets) != 2 {
		t.Fatalf("expected 2 valid nets (CIDR + bare IP as /32), got %d: %v", len(nets), nets)
	}
}

func TestClientIP_UntrustedPeerIgnoresForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")

	got := ClientIP(req, nil)
	if got != "203.0.113.9" {
		t.Errorf("got %q, want the untrusted peer's own address", got)
	}
}

func TestClientIP_TrustedPeerHonorsForwardedFor(t *testing.T) {
	trustedNets := ParseCIDRs([]string{"203.0.113.0/24"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:1234" // the peer itself is inside the trusted range
	req.Header.Set("X-Forwarded-For", "9.9.9.9, 203.0.113.50, 8.8.8.8")

	// Walks right-to-left, skipping entries that are themselves trusted
	// proxies: 8.8.8.8 is the first (right-most) non-trusted entry.
	got := ClientIP(req, trustedNets)
	if got != "8.8.8.8" {
		t.Errorf("got %q, want the right-most non-trusted XFF entry (8.8.8.8)", got)
	}
}

// TestClientIP_TrustedPeerFallsBackToXRealIP covers the X-Real-IP
// branch when X-Forwarded-For is absent.
func TestClientIP_TrustedPeerFallsBackToXRealIP(t *testing.T) {
	trustedNets := ParseCIDRs([]string{"203.0.113.0/24"})
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("X-Real-IP", "7.7.7.7")

	got := ClientIP(req, trustedNets)
	if got != "7.7.7.7" {
		t.Errorf("got %q, want X-Real-IP value (7.7.7.7)", got)
	}
}

// TestRateLimitMiddleware_BlocksOverQuotaRequests drives the real HTTP
// middleware over httptest: first request passes through, a saturated
// limiter returns 429 for the next one.
func TestRateLimitMiddleware_BlocksOverQuotaRequests(t *testing.T) {
	rl := NewRateLimiter(&RateLimitConfig{RequestsPerSecond: 1, BurstSize: 1, CleanupInterval: time.Hour, EntryTTL: time.Hour})
	defer rl.Stop()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := RateLimitMiddleware(rl, inner)

	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	req1.RemoteAddr = "192.0.2.1:55555"
	w1 := httptest.NewRecorder()
	wrapped.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", w1.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.RemoteAddr = "192.0.2.1:55556" // same host, different port -> same rate-limit key
	w2 := httptest.NewRecorder()
	wrapped.ServeHTTP(w2, req2)
	if w2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", w2.Code)
	}
}

// TestRateLimitHandlerFunc_BlocksOverQuotaRequests is the HandlerFunc
// variant of the same gate.
func TestRateLimitHandlerFunc_BlocksOverQuotaRequests(t *testing.T) {
	rl := NewRateLimiter(&RateLimitConfig{RequestsPerSecond: 1, BurstSize: 1, CleanupInterval: time.Hour, EntryTTL: time.Hour})
	defer rl.Stop()

	calls := 0
	wrapped := RateLimitHandlerFunc(rl, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.0.2.2:5555" + string(rune('0'+i))
		w := httptest.NewRecorder()
		wrapped(w, req)
	}
	if calls != 1 {
		t.Errorf("expected the inner handler to run exactly once (second blocked), ran %d times", calls)
	}
}
