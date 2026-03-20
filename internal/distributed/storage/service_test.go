package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

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
