package coldresolve

import (
	"testing"
	"time"
)

// TestNewTorrentFetcher pins the constructor's field wiring. nil bridge is
// fine here: construction does not dial anything.
func TestNewTorrentFetcher(t *testing.T) {
	f := NewTorrentFetcher(nil, "/tmp/cache", 5*time.Second)
	if f.CacheDir != "/tmp/cache" {
		t.Errorf("CacheDir = %q, want /tmp/cache", f.CacheDir)
	}
	if f.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", f.Timeout)
	}
}
