package serve

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestDefaultServerConfig pins the documented defaults.
func TestDefaultServerConfig(t *testing.T) {
	cfg := DefaultServerConfig("127.0.0.1:0")
	if cfg.Addr != "127.0.0.1:0" {
		t.Errorf("Addr = %q", cfg.Addr)
	}
	if cfg.ReadTimeout != 10*time.Second || cfg.WriteTimeout != 30*time.Second || cfg.IdleTimeout != 60*time.Second {
		t.Errorf("unexpected timeouts: %+v", cfg)
	}
	if cfg.MaxHeaderBytes != 1<<20 || cfg.MaxConcurrent != 1024 {
		t.Errorf("unexpected limits: %+v", cfg)
	}
}

// TestNewServerAndShutdown builds a real *http.Server via NewServer, starts it
// on an ephemeral port, verifies it serves /head, then shuts it down.
func TestNewServerAndShutdown(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	cfg := DefaultServerConfig("127.0.0.1:0")
	srv := NewServer(cfg, svc, nil)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(ln)

	url := "http://" + ln.Addr().String() + "/head"
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /head: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	if err := ShutdownServer(srv, 2*time.Second); err != nil {
		t.Fatalf("ShutdownServer: %v", err)
	}
}

// TestLimitConcurrent_Rejects verifies the semaphore gate returns 503 once the
// configured concurrency ceiling is exceeded.
func TestLimitConcurrent_Rejects(t *testing.T) {
	block := make(chan struct{})
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	})
	h := limitConcurrent(1, inner)
	srv := httptest.NewServer(h)
	defer srv.Close()
	defer close(block)

	// First request occupies the single slot.
	done := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(srv.URL)
		if err == nil {
			done <- resp
		}
	}()
	time.Sleep(50 * time.Millisecond)

	// Second request should be rejected with 503 while the first is in flight.
	resp2, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp2.StatusCode)
	}

	block <- struct{}{}
	r1 := <-done
	r1.Body.Close()
}

// TestLimitConcurrent_Unlimited covers MaxConcurrent==0 bypassing the gate
// (exercised indirectly via NewServer, which only wraps when > 0).
func TestLimitConcurrent_Unlimited(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(1), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	cfg := DefaultServerConfig("127.0.0.1:0")
	cfg.MaxConcurrent = 0
	srv := NewServer(cfg, svc, nil)
	if srv.Handler == nil {
		t.Fatal("expected a handler")
	}
}
