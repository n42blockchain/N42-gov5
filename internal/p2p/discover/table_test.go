package discover

import (
	"testing"

	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/internal/p2p/enr"
)

func TestTable_PingReplace(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	if tab.len() != 0 {
		t.Fatalf("len() = %d, want 0", tab.len())
	}
}

func TestTable_BucketAtDistance(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	if got := tab.bucketAtDistance(bucketMinDistance); got != tab.buckets[0] {
		t.Fatal("expected distance <= bucketMinDistance to map to bucket 0")
	}
	if got := tab.bucketAtDistance(bucketMinDistance + 1); got != tab.buckets[0] {
		t.Fatal("expected distance bucketMinDistance+1 to map to bucket 0")
	}
	if got := tab.bucketAtDistance(bucketMinDistance + 2); got != tab.buckets[1] {
		t.Fatal("expected distance bucketMinDistance+2 to map to bucket 1")
	}
}

func TestTable_AddSeenNode(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	self := tab.self().ID()
	n := nodeAtDistance(self, 200, intIP(1))

	tab.addSeenNode(wrapNode(n))
	if tab.len() != 1 {
		t.Fatalf("len() = %d, want 1", tab.len())
	}
	if got := tab.getNode(n.ID()); got == nil || got.ID() != n.ID() {
		t.Fatalf("getNode() = %v, want node %v", got, n.ID())
	}

	// Adding the same node again must not duplicate it.
	tab.addSeenNode(wrapNode(n))
	if tab.len() != 1 {
		t.Fatalf("len() after duplicate add = %d, want 1", tab.len())
	}
}

func TestTable_AddSeenNode_RejectsSelf(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	selfNode := tab.self()
	tab.addSeenNode(wrapNode(selfNode))
	if tab.len() != 0 {
		t.Fatalf("len() = %d, want 0 (self must never be added)", tab.len())
	}
}

func TestTable_AddVerifiedNode_RequiresInitDone(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	// isInitDone() is false until the table's loop runs and finishes a
	// refresh; addVerifiedNode must be a no-op before that.
	n := nodeAtDistance(tab.self().ID(), 200, intIP(1))
	tab.addVerifiedNode(wrapNode(n))
	if tab.len() != 0 {
		t.Fatalf("len() = %d, want 0 before init is done", tab.len())
	}
}

func TestTable_BucketFillAndReplacement(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	target := enode.ID{0xaa}
	last := fillBucket(tab, target)
	b := tab.bucket(target)
	if len(b.entries) != bucketSize {
		t.Fatalf("bucket len = %d, want %d", len(b.entries), bucketSize)
	}

	// A node for the same (full) bucket is added as a replacement, not a
	// bucket entry.
	ld := enode.LogDist(tab.self().ID(), target)
	extra := wrapNode(nodeAtDistance(tab.self().ID(), ld, intIP(999)))
	tab.addSeenNode(extra)
	if len(b.entries) != bucketSize {
		t.Fatalf("bucket len after overflow add = %d, want unchanged %d", len(b.entries), bucketSize)
	}
	found := false
	for _, r := range b.replacements {
		if r.ID() == extra.ID() {
			found = true
		}
	}
	if !found {
		t.Fatal("expected overflow node to land in the replacement list")
	}

	// replace() swaps the last entry out for a replacement.
	replacement := tab.replace(b, last)
	if replacement == nil {
		t.Fatal("expected replace() to return a replacement node")
	}
	if b.entries[len(b.entries)-1].ID() != replacement.ID() {
		t.Fatal("expected replacement to now be the last bucket entry")
	}
}

func TestTable_DeleteNode(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	n := wrapNode(nodeAtDistance(tab.self().ID(), 200, intIP(1)))
	tab.addSeenNode(n)
	if tab.len() != 1 {
		t.Fatalf("len() = %d, want 1", tab.len())
	}
	tab.delete(n)
	if tab.len() != 0 {
		t.Fatalf("len() after delete = %d, want 0", tab.len())
	}
}

func TestTable_FindnodeByID(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	nodes := nodesAtDistance(tab.self().ID(), 230, 5)
	fillTable(tab, nodes, false)

	target := tab.self().ID()
	result := tab.findnodeByID(target, 3, false)
	if len(result.entries) != 3 {
		t.Fatalf("findnodeByID returned %d entries, want 3", len(result.entries))
	}
	if !sortedByDistanceTo(target, unwrapNodes(result.entries)) {
		t.Fatal("expected results sorted by distance to target")
	}
}

func TestTable_ReadRandomNodesBeforeInit(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	buf := make([]*enode.Node, 10)
	// isInitDone() is false until refresh completes; ReadRandomNodes must
	// report 0 rather than reading a half-populated table.
	if n := tab.ReadRandomNodes(buf); n != 0 {
		t.Fatalf("ReadRandomNodes() = %d, want 0 before init done", n)
	}
}

func TestNodesByDistancePush(t *testing.T) {
	target := enode.ID{}
	h := &nodesByDistance{target: target}

	ids := []enode.ID{{3}, {1}, {2}}
	for _, id := range ids {
		h.push(wrapNode(nodeFromID(id)), 10)
	}
	if len(h.entries) != 3 {
		t.Fatalf("len = %d, want 3", len(h.entries))
	}
	for i := 1; i < len(h.entries); i++ {
		if enode.DistCmp(target, h.entries[i-1].ID(), h.entries[i].ID()) > 0 {
			t.Fatal("entries not sorted by distance to target")
		}
	}
}

func TestNodesByDistancePush_RespectsMax(t *testing.T) {
	target := enode.ID{}
	h := &nodesByDistance{target: target}
	for i := 0; i < 10; i++ {
		h.push(wrapNode(nodeFromID(enode.ID{byte(i)})), 3)
	}
	if len(h.entries) != 3 {
		t.Fatalf("len = %d, want 3 (maxElems)", len(h.entries))
	}
}

func nodeFromID(id enode.ID) *enode.Node {
	var rec enr.Record
	return enode.SignNull(&rec, id)
}
