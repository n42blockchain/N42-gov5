package torrent

import (
	"context"
	"testing"
)

// TestNewBridge covers the constructor: it must wire an empty mapping table
// and accept a nil client (the mapping-only operations never dereference it).
func TestNewBridge(t *testing.T) {
	b := NewBridge(nil)
	if b == nil {
		t.Fatal("NewBridge() = nil")
	}
	if b.mappings == nil {
		t.Fatal("NewBridge() did not initialize the mappings map")
	}
	if b.client != nil {
		t.Fatal("NewBridge(nil).client should be nil")
	}
}

// TestFetchByContentHash_NoMapping covers the not-found branch of
// FetchByContentHash, which must fail before ever touching the (nil)
// client, since no real torrent client / network is available in this
// deterministic test environment.
func TestFetchByContentHash_NoMapping(t *testing.T) {
	b := NewBridge(nil)
	_, err := b.FetchByContentHash(context.Background(), [32]byte{0x01})
	if err == nil {
		t.Fatal("FetchByContentHash() error = nil, want error for unmapped content hash")
	}
}
