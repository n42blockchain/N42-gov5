package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
)

func TestValidateCID(t *testing.T) {
	tests := []struct {
		cid   string
		valid bool
	}{
		{"QmYwAPJzv5CZsnN625s3XfXYmPXyMeLoZFyRzhLqCVPvP8", true},
		{"bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi", true},
		{"", false},
		{"invalid", false},
		{"foo&arg=evil", false},
		{"../../../etc/passwd", false},
		{"Qm", false},
	}
	for _, tt := range tests {
		err := validateCID(tt.cid)
		if tt.valid && err != nil {
			t.Errorf("validateCID(%q) should be valid, got: %v", tt.cid, err)
		}
		if !tt.valid && err == nil {
			t.Errorf("validateCID(%q) should be invalid", tt.cid)
		}
	}
}

func TestIPFSClientPin(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/pin/add" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"Pins":["QmTest"]}`))
	}))
	defer srv.Close()

	client := NewIPFSClient(srv.URL, 5e9, 5e9, 1<<20)
	err := client.Pin(context.Background(), "QmYwAPJzv5CZsnN625s3XfXYmPXyMeLoZFyRzhLqCVPvP8")
	if err != nil {
		t.Fatalf("Pin: %v", err)
	}
}

func TestIPFSClientPinInvalidCID(t *testing.T) {
	client := NewIPFSClient("http://localhost:5001", 5e9, 5e9, 1<<20)
	err := client.Pin(context.Background(), "evil&arg=foo")
	if err == nil {
		t.Fatal("expected error for invalid CID")
	}
}

func TestIPFSClientGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	client := NewIPFSClient(srv.URL, 5e9, 5e9, 1<<20)
	data, err := client.Get(context.Background(), "QmYwAPJzv5CZsnN625s3XfXYmPXyMeLoZFyRzhLqCVPvP8")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("data = %s, want hello world", data)
	}
}

func TestIPFSClientStat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Hash":"QmTest","NumLinks":0,"BlockSize":100,"DataSize":80,"CumulativeSize":100}`))
	}))
	defer srv.Close()

	client := NewIPFSClient(srv.URL, 5e9, 5e9, 1<<20)
	stat, err := client.Stat(context.Background(), "QmYwAPJzv5CZsnN625s3XfXYmPXyMeLoZFyRzhLqCVPvP8")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.DataSize != 80 {
		t.Fatalf("DataSize = %d, want 80", stat.DataSize)
	}
}

func TestBridgeLifecycle(t *testing.T) {
	cfg := conf.DefaultStorageCfg()
	cfg.Enabled = true
	b := NewBridge(&cfg)
	b.Start()
	b.Stop()
	b.Stop() // double stop safe
}

func TestIPFSClientTimeout(t *testing.T) {
	// Server that blocks long enough to trigger timeout.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Client with a very short timeout.
	client := NewIPFSClient(srv.URL, 0, 0, 1<<20)
	client.httpClient.Timeout = 50 * time.Millisecond

	err := client.Pin(context.Background(), "QmYwAPJzv5CZsnN625s3XfXYmPXyMeLoZFyRzhLqCVPvP8")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "Client.Timeout") && !strings.Contains(err.Error(), "context deadline") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
}

func TestIPFSClientRetry(t *testing.T) {
	var attempt int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempt, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("retry success"))
	}))
	defer srv.Close()

	client := NewIPFSClient(srv.URL, 5e9, 5e9, 1<<20)

	// Build a request manually and use retryDo.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/api/v0/cat?arg=test", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.retryDo(req, 3)
	if err != nil {
		t.Fatalf("retryDo should succeed after retries: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempt) != 3 {
		t.Fatalf("expected 3 attempts, got %d", atomic.LoadInt32(&attempt))
	}
}

func TestIPFSClientRetryExhausted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := NewIPFSClient(srv.URL, 5e9, 5e9, 1<<20)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/test", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.retryDo(req, 2)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if !strings.Contains(err.Error(), "max retries exceeded") {
		t.Fatalf("expected max retries error, got: %v", err)
	}
}

func TestIPFSClientInvalidResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not json at all {{{"))
	}))
	defer srv.Close()

	client := NewIPFSClient(srv.URL, 5e9, 5e9, 1<<20)
	_, err := client.Stat(context.Background(), "QmYwAPJzv5CZsnN625s3XfXYmPXyMeLoZFyRzhLqCVPvP8")
	if err == nil {
		t.Fatal("expected JSON decode error")
	}
}

func TestCIDValidationEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		cid   string
		valid bool
	}{
		{"empty", "", false},
		{"very long", strings.Repeat("Q", 500), false},
		{"special chars", "Qm!@#$%^&*()", false},
		{"null bytes", "Qm\x00\x00\x00", false},
		{"spaces", "Qm test test", false},
		{"tab chars", "Qm\ttab", false},
		{"valid v0 min", "QmYwAPJzv5CZsnN625s3XfXYmPXyMeLoZFyRzhLqCVPvP8", true},
		{"valid v1 min", "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCID(tt.cid)
			if tt.valid && err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
			if !tt.valid && err == nil {
				t.Error("expected invalid")
			}
		})
	}
}
