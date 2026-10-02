package discover

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/p2p/enode"
)

// lookupTestNet maps node ID -> the nodes that querying it should return.
// It also records which nodes were queried. query() runs concurrently from
// multiple lookup goroutines, so access is guarded by a mutex.
type lookupTestNet struct {
	mu      sync.Mutex
	answers map[enode.ID][]*node
	queried map[enode.ID]int
}

func newLookupTestNet() *lookupTestNet {
	return &lookupTestNet{
		answers: make(map[enode.ID][]*node),
		queried: make(map[enode.ID]int),
	}
}

func (n *lookupTestNet) query(target *node) ([]*node, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.queried[target.ID()]++
	return n.answers[target.ID()], nil
}

func TestLookup_RunConvergesOnSeedNodes(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	self := tab.self().ID()
	target := self // arbitrary target

	// Seed the table with a handful of nodes close to self.
	seeds := nodesAtDistance(self, 250, 5)
	fillTable(tab, seeds, false)

	net := newLookupTestNet()
	// Each seed, when queried, reports no further nodes: the lookup should
	// terminate using just the initial local-table answer.
	l := newLookup(context.Background(), tab, target, net.query)
	result := l.run()

	if len(result) == 0 {
		t.Fatal("expected lookup to return at least the seeded nodes")
	}
	for _, n := range result {
		found := false
		for _, s := range seeds {
			if s.ID() == n.ID() {
				found = true
			}
		}
		if !found {
			t.Fatalf("result contains unexpected node %v", n.ID())
		}
	}
}

func TestLookup_CancelStopsEarly(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	self := tab.self().ID()
	seeds := nodesAtDistance(self, 250, 3)
	fillTable(tab, seeds, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	blockingQuery := func(n *node) ([]*node, error) {
		// Should not actually be reached for most seeds once cancelled, but
		// return quickly regardless to avoid hanging the test if it is.
		return nil, nil
	}

	l := newLookup(ctx, tab, self, blockingQuery)
	done := make(chan []*enode.Node, 1)
	go func() { done <- l.run() }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("lookup.run() did not return after context cancellation")
	}
}

func TestLookupIterator_NextAndClose(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	self := tab.self().ID()
	seeds := nodesAtDistance(self, 250, 4)
	fillTable(tab, seeds, false)

	net := newLookupTestNet()
	makeLookup := func(ctx context.Context) *lookup {
		return newLookup(ctx, tab, self, net.query)
	}

	it := newLookupIterator(context.Background(), makeLookup)
	defer it.Close()

	count := 0
	for it.Next() && count < len(seeds) {
		if it.Node() == nil {
			t.Fatal("Node() returned nil while Next() was true")
		}
		count++
	}
	if count == 0 {
		t.Fatal("expected at least one node from the iterator")
	}
}

func TestLookupIterator_CloseStopsNext(t *testing.T) {
	transport := newPingRecorder()
	tab, db := newInactiveTestTable(transport)
	defer db.Close()

	net := newLookupTestNet()
	makeLookup := func(ctx context.Context) *lookup {
		return newLookup(ctx, tab, tab.self().ID(), net.query)
	}
	it := newLookupIterator(context.Background(), makeLookup)
	it.Close()

	if it.Next() {
		t.Fatal("expected Next() to return false after Close()")
	}
}
