package discover

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/p2p/discover/v4wire"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/internal/p2p/enr"
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

// TestUDPv4_ResolveSuccess drives Resolve's happy path: RequestENR succeeds
// on the first try and its record is returned directly.
func TestUDPv4_ResolveSuccess(t *testing.T) {
	test := newUDPTest(t)
	defer test.close()

	target := remoteEnodeNode(test)

	resc := make(chan *enode.Node, 1)
	go func() { resc <- test.udp.Resolve(target) }()

	// ensureBond: answer the solicited ping.
	answerPing(t, test)

	// Answer the ENRRequest with a properly V4-signed record for the SAME
	// key/ID as target, so enode.New(ValidSchemes, ...) accepts it and the
	// ID check in RequestENR passes.
	dg, err := test.pipe.receive()
	if err != nil {
		t.Fatalf("expected outgoing ENRRequest, got error: %v", err)
	}
	decoded, _, hash, err := v4wire.Decode(dg.data)
	if err != nil {
		t.Fatalf("decode ENRRequest: %v", err)
	}
	if _, ok := decoded.(*v4wire.ENRRequest); !ok {
		t.Fatalf("outgoing packet type = %T, want *v4wire.ENRRequest", decoded)
	}

	var rec enr.Record
	rec.Set(enr.IP(test.remoteaddr.IP))
	rec.Set(enr.UDP(test.remoteaddr.Port))
	if err := enode.SignV4(&rec, test.remotekey); err != nil {
		t.Fatalf("SignV4: %v", err)
	}
	resp := &v4wire.ENRResponse{ReplyTok: hash, Record: rec}
	if err := test.packetIn(resp); err != nil {
		t.Fatalf("packetIn(ENRResponse): %v", err)
	}

	select {
	case got := <-resc:
		if got == nil || got.ID() != target.ID() {
			t.Fatalf("Resolve() = %v, want node with ID %v", got, target.ID())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Resolve() did not return in time")
	}
}
