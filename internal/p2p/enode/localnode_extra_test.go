package enode

import (
	"context"
	"net"
	"testing"

	"github.com/n42blockchain/N42/internal/p2p/enr"
)

func newTestLocalNode(t *testing.T) *LocalNode {
	t.Helper()
	db, err := OpenDB(context.Background(), "", t.TempDir())
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(db.Close)
	key := testKey(t)
	return NewLocalNode(db, key)
}

func TestLocalNode_IDAndDatabase(t *testing.T) {
	ln := newTestLocalNode(t)
	if ln.ID() == (ID{}) {
		t.Fatal("expected non-zero local node ID")
	}
	if ln.Database() == nil {
		t.Fatal("expected non-nil Database()")
	}
}

func TestLocalNode_SetAndNode(t *testing.T) {
	ln := newTestLocalNode(t)
	seqBefore := ln.Seq()

	ln.Set(enr.UDP(30303))
	n := ln.Node()
	if n == nil {
		t.Fatal("expected non-nil Node()")
	}
	if n.UDP() != 30303 {
		t.Fatalf("UDP() = %d, want 30303", n.UDP())
	}
	if ln.Seq() <= seqBefore {
		t.Fatal("expected Seq() to increase after Set + Node")
	}

	// Setting the same value again must not bump Seq (reflect.DeepEqual short
	// circuit in ln.set).
	n1 := ln.Node()
	ln.Set(enr.UDP(30303))
	n2 := ln.Node()
	if n1 != n2 {
		t.Fatal("expected Node() to return the cached record when nothing changed")
	}
}

func TestLocalNode_Delete(t *testing.T) {
	ln := newTestLocalNode(t)
	ln.Set(enr.UDP(1234))
	_ = ln.Node()

	ln.Delete(enr.UDP(0))
	n := ln.Node()
	var udp enr.UDP
	if err := n.Load(&udp); err == nil {
		t.Fatalf("expected UDP entry to be deleted, got %d", udp)
	}
}

func TestLocalNode_SetInvalidEntryIgnored(t *testing.T) {
	ln := newTestLocalNode(t)
	// WithEntry wrapping a channel cannot be RLP-encoded; validateLocalNodeEntry
	// should catch the panic from enr.Record.Set and Set() should simply ignore
	// the entry rather than panicking itself.
	ln.Set(enr.WithEntry("bad", make(chan int)))
	// No panic means success; the node must still be usable afterwards.
	if ln.Node() == nil {
		t.Fatal("expected LocalNode to remain usable after an invalid Set")
	}
}

func TestLocalNode_StaticAndFallbackIP(t *testing.T) {
	ln := newTestLocalNode(t)

	ln.SetStaticIP(net.ParseIP("1.2.3.4"))
	n := ln.Node()
	if n.IP().String() != "1.2.3.4" {
		t.Fatalf("IP() = %v, want 1.2.3.4 after SetStaticIP", n.IP())
	}

	ln.SetFallbackUDP(9999)
	n = ln.Node()
	if n.UDP() != 9999 {
		t.Fatalf("UDP() = %d, want 9999 after SetFallbackUDP", n.UDP())
	}

	ln2 := newTestLocalNode(t)
	ln2.SetFallbackIP(net.ParseIP("5.6.7.8"))
	n2 := ln2.Node()
	if n2.IP().String() != "5.6.7.8" {
		t.Fatalf("IP() = %v, want 5.6.7.8 after SetFallbackIP (no static set)", n2.IP())
	}
}

func TestLocalNode_FallbackIPv6(t *testing.T) {
	ln := newTestLocalNode(t)
	ipv6 := net.ParseIP("2001:db8::1")
	ln.SetFallbackIP(ipv6)
	n := ln.Node()
	if n.IP().String() != ipv6.String() {
		t.Fatalf("IP() = %v, want %v", n.IP(), ipv6)
	}
}

func TestLocalNode_UDPEndpointStatementAndContact(t *testing.T) {
	ln := newTestLocalNode(t)
	from := &net.UDPAddr{IP: net.ParseIP("9.9.9.9"), Port: 30303}
	endpoint := &net.UDPAddr{IP: net.ParseIP("1.2.3.4"), Port: 40404}

	// Exercise both UDPContact and UDPEndpointStatement; neither should panic,
	// and updateEndpoints runs as part of each.
	ln.UDPContact(from)
	ln.UDPEndpointStatement(from, endpoint)
	_ = ln.Node()
}

func TestValidateLocalNodeEntry(t *testing.T) {
	if err := validateLocalNodeEntry(nil); err == nil {
		t.Fatal("expected error for nil entry")
	}
	if err := validateLocalNodeEntry(enr.UDP(1)); err != nil {
		t.Fatalf("validateLocalNodeEntry(UDP): %v", err)
	}
	if err := validateLocalNodeEntry(enr.WithEntry("bad", make(chan int))); err == nil {
		t.Fatal("expected error for un-RLP-encodable entry")
	}
}

func TestLocalNodeEntryKey(t *testing.T) {
	if localNodeEntryKey(nil) != "<nil>" {
		t.Fatalf("localNodeEntryKey(nil) = %q, want <nil>", localNodeEntryKey(nil))
	}
	if localNodeEntryKey(enr.UDP(1)) != "udp" {
		t.Fatalf("localNodeEntryKey(UDP) = %q, want udp", localNodeEntryKey(enr.UDP(1)))
	}
}
