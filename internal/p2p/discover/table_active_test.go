package discover

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/p2p/enode"
)

// waitInitDone blocks until the table's loop finishes its first refresh.
func waitInitDone(t *testing.T, tab *Table) {
	t.Helper()
	select {
	case <-tab.initDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for table init to complete")
	}
}

func TestTable_AddVerifiedNodeAfterInit(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newTestTable(transport)
	defer db.Close()
	defer tab.close()

	waitInitDone(t, tab)

	n := nodeAtDistance(tab.self().ID(), 200, intIP(1))
	tab.addVerifiedNode(wrapNode(n))

	if tab.len() != 1 {
		t.Fatalf("len() = %d, want 1 after addVerifiedNode post-init", tab.len())
	}
	got := tab.getNode(n.ID())
	if got == nil || got.ID() != n.ID() {
		t.Fatalf("getNode() = %v, want %v", got, n.ID())
	}

	// Re-adding (bump) must move it to front without duplicating.
	tab.addVerifiedNode(wrapNode(n))
	if tab.len() != 1 {
		t.Fatalf("len() after re-add = %d, want 1", tab.len())
	}
}

func TestTable_ReadRandomNodesAfterInit(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newTestTable(transport)
	defer db.Close()
	defer tab.close()

	waitInitDone(t, tab)

	nodes := nodesAtDistance(tab.self().ID(), 220, 5)
	fillTable(tab, nodes, true)

	buf := make([]*enode.Node, 10)
	n := tab.ReadRandomNodes(buf)
	if n == 0 {
		t.Fatal("expected ReadRandomNodes to return at least one node after init")
	}
	if hasDuplicates(buf[:n]) {
		t.Fatal("ReadRandomNodes returned duplicates")
	}
}

func TestTable_AddIPLimit(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	b := tab.buckets[0]
	ip := []byte{8, 8, 8, 8} // non-LAN
	if !tab.addIP(b, ip) {
		t.Fatal("expected first addIP to succeed")
	}
	// addIP with an empty IP must fail.
	if tab.addIP(b, nil) {
		t.Fatal("expected addIP(nil) to fail")
	}
	tab.removeIP(b, ip)
}

func TestTable_SetFallbackNodesRejectsIncomplete(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	incomplete := enode.NewV4(&key.PublicKey, nil, 0, 0) // no IP: Incomplete()==true
	if err := tab.setFallbackNodes([]*enode.Node{incomplete}); err == nil {
		t.Fatal("expected error for incomplete bootstrap node")
	}
}
