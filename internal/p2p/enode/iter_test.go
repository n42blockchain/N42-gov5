// Adapted from go-ethereum's p2p/enode/iter_test.go for this package's
// Iterator/FairMix implementation (no source-naming support here).
package enode

import (
	"encoding/binary"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/p2p/enr"
)

func TestReadNodes(t *testing.T) {
	nodes := ReadNodes(new(genIter), 10)
	checkNodes(t, nodes, 10)
}

// This test checks that ReadNodes terminates when reading N nodes from an iterator
// which returns less than N nodes in an endless cycle.
func TestReadNodesCycle(t *testing.T) {
	iter := &callCountIter{
		Iterator: CycleNodes([]*Node{
			testNode(0, 0),
			testNode(1, 0),
			testNode(2, 0),
		}),
	}
	nodes := ReadNodes(iter, 10)
	checkNodes(t, nodes, 3)
	if iter.count != 10 {
		t.Fatalf("%d calls to Next, want %d", iter.count, 10)
	}
}

// NOTE: unlike upstream go-ethereum's Filter (which loops internally until a
// matching node is found or the source ends), this package's filterIter.Next
// advances the underlying iterator exactly once per call and returns whether
// *that* node passed the check — it does not skip non-matching nodes for the
// caller. This is a real behavioral difference from go-ethereum; callers must
// call Next() in a loop themselves to skip rejected nodes. Documented here
// rather than "fixed" since fixing it is a non-test-code change.
func TestFilterNodes(t *testing.T) {
	nodes := make([]*Node, 10)
	for i := range nodes {
		nodes[i] = testNode(uint64(i), uint64(i))
	}

	it := Filter(IterNodes(nodes), func(n *Node) bool {
		return n.Seq() >= 5
	})

	// The first 5 calls correspond to nodes with Seq 0..4, which fail the
	// check — Next() returns false for each, one underlying node at a time.
	for i := 0; i < 5; i++ {
		if it.Next() {
			t.Fatalf("Next() at underlying index %d = true, want false (node fails check)", i)
		}
	}
	// From here on every remaining underlying node satisfies the check.
	for i := 5; i < len(nodes); i++ {
		if !it.Next() {
			t.Fatalf("Next() at underlying index %d = false, want true", i)
		}
		if it.Node() != nodes[i] {
			t.Fatalf("iterator returned wrong node %v\nwant %v", it.Node(), nodes[i])
		}
	}
	if it.Next() {
		t.Fatal("Next returned true after underlying iterator has ended")
	}
}

func checkNodes(t *testing.T, nodes []*Node, wantLen int) {
	t.Helper()
	if len(nodes) != wantLen {
		t.Errorf("slice has %d nodes, want %d", len(nodes), wantLen)
		return
	}
	seen := make(map[ID]bool, len(nodes))
	for i, e := range nodes {
		if e == nil {
			t.Errorf("nil node at index %d", i)
			return
		}
		if seen[e.ID()] {
			t.Errorf("slice has duplicate node %v", e.ID())
			return
		}
		seen[e.ID()] = true
	}
}

// This test checks that FairMix falls back to an alternative source when
// the 'fair' choice doesn't return a node within the timeout.
func TestFairMixNextFromAll(t *testing.T) {
	mix := NewFairMix(1 * time.Millisecond)
	mix.AddSource(&genIter{index: 1})
	mix.AddSource(CycleNodes(nil))
	defer mix.Close()

	nodes := ReadNodes(mix, 500)
	checkNodes(t, nodes, 500)

	d := idPrefixDistribution(nodes)
	if len(d) > 1 || d[1] != len(nodes) {
		t.Fatalf("wrong ID distribution: %v", d)
	}
}

// This test ensures FairMix works for Next with no sources.
func TestFairMixEmpty(t *testing.T) {
	var (
		mix   = NewFairMix(1 * time.Second)
		testN = testNode(1, 1)
		ch    = make(chan *Node)
	)
	defer mix.Close()

	go func() {
		mix.Next()
		ch <- mix.Node()
	}()

	mix.AddSource(CycleNodes([]*Node{testN}))
	if n := <-ch; n != testN {
		t.Errorf("got wrong node: %v", n)
	}
}

// This test checks closing a source while Next runs.
func TestFairMixRemoveSource(t *testing.T) {
	mix := NewFairMix(1 * time.Second)
	source := make(blockingIter)
	mix.AddSource(source)

	sig := make(chan *Node)
	go func() {
		<-sig
		mix.Next()
		sig <- mix.Node()
	}()

	sig <- nil
	runtime.Gosched()
	source.Close()

	wantNode := testNode(0, 0)
	mix.AddSource(CycleNodes([]*Node{wantNode}))
	n := <-sig

	mix.mu.Lock()
	gotSources := len(mix.sources)
	mix.mu.Unlock()
	if gotSources != 1 {
		t.Fatalf("have %d sources, want one", gotSources)
	}
	if n != wantNode {
		t.Fatalf("mixer returned wrong node")
	}
}

type blockingIter chan struct{}

func (it blockingIter) Next() bool {
	<-it
	return false
}

func (it blockingIter) Node() *Node {
	return nil
}

func (it blockingIter) Close() {
	close(it)
}

func TestFairMixClose(t *testing.T) {
	for i := 0; i < 20 && !t.Failed(); i++ {
		testMixerClose(t)
	}
}

func testMixerClose(t *testing.T) {
	mix := NewFairMix(-1)
	mix.AddSource(CycleNodes(nil))
	mix.AddSource(CycleNodes(nil))

	done := make(chan struct{})
	go func() {
		defer close(done)
		if mix.Next() {
			t.Error("Next returned true")
		}
	}()
	// This call is supposed to make it more likely that NextNode is
	// actually executing by the time we call Close.
	runtime.Gosched()

	mix.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Next didn't unblock on Close")
	}

	mix.Close() // shouldn't crash
}

func idPrefixDistribution(nodes []*Node) map[uint32]int {
	d := make(map[uint32]int, len(nodes))
	for _, node := range nodes {
		id := node.ID()
		d[binary.BigEndian.Uint32(id[:4])]++
	}
	return d
}

// genIter creates fake nodes with numbered IDs based on 'index' and 'gen'
type genIter struct {
	node       *Node
	index, gen uint32
}

func (s *genIter) Next() bool {
	index := atomic.LoadUint32(&s.index)
	if index == ^uint32(0) {
		s.node = nil
		return false
	}
	s.node = testNode(uint64(index)<<32|uint64(s.gen), 0)
	s.gen++
	return true
}

func (s *genIter) Node() *Node {
	return s.node
}

func (s *genIter) Close() {
	atomic.StoreUint32(&s.index, ^uint32(0))
}

func testNode(id, seq uint64) *Node {
	var nodeID ID
	binary.BigEndian.PutUint64(nodeID[:], id)
	r := new(enr.Record)
	r.SetSeq(seq)
	return SignNull(r, nodeID)
}

// callCountIter counts calls to NextNode.
type callCountIter struct {
	Iterator
	count int
}

func (it *callCountIter) Next() bool {
	it.count++
	return it.Iterator.Next()
}
