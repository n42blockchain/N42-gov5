// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hyperlane_cov_test.go covers HyperlaneMailboxBinding/HyperlaneReceiver
// construction and handleProcessEvent. DialHTTP only parses the endpoint
// URL and builds a client object — it performs no real network I/O until a
// CallContext is made, so constructing these types here stays fake-only.

package bridge

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestNewHyperlaneMailboxBinding(t *testing.T) {
	addr := types.HexToAddress("0x1000000000000000000000000000000000000009")
	from := types.HexToAddress("0x2000000000000000000000000000000000000009")

	b, err := NewHyperlaneMailboxBinding("http://127.0.0.1:0", addr, DomainN42, from)
	if err != nil {
		t.Fatalf("NewHyperlaneMailboxBinding: %v", err)
	}
	defer b.Close()

	if b.mailboxAddr != addr {
		t.Fatalf("mailboxAddr = %s, want %s", b.mailboxAddr, addr)
	}
	if b.n42Domain != DomainN42 {
		t.Fatalf("n42Domain = %d, want %d", b.n42Domain, DomainN42)
	}
}

func TestNewHyperlaneMailboxBinding_InvalidEndpoint(t *testing.T) {
	addr := types.HexToAddress("0x1000000000000000000000000000000000000009")
	from := types.HexToAddress("0x2000000000000000000000000000000000000009")

	if _, err := NewHyperlaneMailboxBinding("://bad-url", addr, DomainN42, from); err == nil {
		t.Fatal("expected error for invalid endpoint URL")
	}
}

func TestNewHyperlaneReceiver(t *testing.T) {
	addr := types.HexToAddress("0x3000000000000000000000000000000000000009")

	r, err := NewHyperlaneReceiver("http://127.0.0.1:0", addr, 0)
	if err != nil {
		t.Fatalf("NewHyperlaneReceiver: %v", err)
	}
	if r.pollInterval != 12*time.Second {
		t.Fatalf("pollInterval = %v, want default 12s", r.pollInterval)
	}

	r2, err := NewHyperlaneReceiver("http://127.0.0.1:0", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("NewHyperlaneReceiver: %v", err)
	}
	if r2.pollInterval != 5*time.Second {
		t.Fatalf("pollInterval = %v, want 5s", r2.pollInterval)
	}
}

func TestNewHyperlaneReceiver_InvalidEndpoint(t *testing.T) {
	addr := types.HexToAddress("0x3000000000000000000000000000000000000009")
	if _, err := NewHyperlaneReceiver("://bad-url", addr, 0); err == nil {
		t.Fatal("expected error for invalid endpoint URL")
	}
}

func TestHyperlaneReceiver_HandleProcessEvent(t *testing.T) {
	addr := types.HexToAddress("0x3000000000000000000000000000000000000009")
	r, err := NewHyperlaneReceiver("http://127.0.0.1:0", addr, 0)
	if err != nil {
		t.Fatalf("NewHyperlaneReceiver: %v", err)
	}

	// Malformed log entry (no topics) must not panic.
	r.handleProcessEvent(map[string]interface{}{})

	// Too few topics must not panic.
	r.handleProcessEvent(map[string]interface{}{
		"topics": []interface{}{"0x1"},
	})

	// Well-formed Process event log.
	r.handleProcessEvent(map[string]interface{}{
		"topics":          []interface{}{"0xabc", "0x1", "0x2", "0x3"},
		"transactionHash": "0xdeadbeef",
	})
}
