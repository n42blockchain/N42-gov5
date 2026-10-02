package discover

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/p2p/enode"
)

// TestUDPv4_ListenUDP checks the common.go ListenUDP wrapper delegates to
// ListenV4 and returns a working transport.
func TestUDPv4_ListenUDP(t *testing.T) {
	pipe := newpipe()
	defer pipe.Close()

	key := newkey()
	db := newTestDiscoverDB()
	defer db.Close()
	ln := enode.NewLocalNode(db, key)

	udp, err := ListenUDP(pipe, ln, Config{PrivateKey: key})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer udp.Close()

	if udp.Self() == nil {
		t.Fatal("expected a non-nil Self() node")
	}
}

// TestUDPv4_LookupPubkeyEmptyTable exercises LookupPubkey's empty-table
// refresh path and the underlying lookup machinery; with no bonded peers
// the lookup degrades to local-table-only results (possibly empty), but
// must complete without hanging.
func TestUDPv4_LookupPubkeyEmptyTable(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	done := make(chan []*enode.Node, 1)
	go func() {
		done <- test.udp.LookupPubkey(&test.remotekey.PublicKey)
	}()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("LookupPubkey() did not return in time")
	}
}

// TestUDPv4_RandomNodesIteratorClose checks that the RandomNodes() iterator
// can be created and closed cleanly without ever calling Next (which would
// block on network activity we aren't simulating here).
func TestUDPv4_RandomNodesIteratorClose(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	it := test.udp.RandomNodes()
	it.Close()
}

// TestUDPv4_ResolveNoKeyReturnsInput checks Resolve's early-return branch
// for a node record with no secp256k1 key.
func TestUDPv4_ResolveNoKeyReturnsInput(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	id := enode.ID{0x42}
	n := nodeFromID(id) // SignNull: no secp256k1 key present
	got := test.udp.Resolve(n)
	if got != n {
		t.Fatalf("Resolve() = %v, want the same node %v (no key, no lookup possible)", got, n)
	}
}
