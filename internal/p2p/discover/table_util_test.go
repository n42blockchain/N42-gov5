// Test helpers adapted from go-ethereum's p2p/discover/table_util_test.go to
// this package's (older-vintage) Table API: newTable(t, db, bootnodes, log),
// node (not tableNode), addSeenNode/addVerifiedNode (not addFoundNode).
package discover

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/internal/p2p/enr"
	log "github.com/n42blockchain/N42/log"
)

var nullNode *enode.Node

func init() {
	var r enr.Record
	r.Set(enr.IP{0, 0, 0, 0})
	nullNode = enode.SignNull(&r, enode.ID{})
}

func newTestDiscoverDB() *enode.DB {
	db, err := enode.OpenDB(context.Background(), "", "")
	if err != nil {
		panic(err)
	}
	return db
}

// newInactiveTestTable creates a Table backed by an in-memory enode.DB
// WITHOUT starting its main loop. isInitDone() stays false forever and
// tab.close() must NOT be called on the result (it would block waiting for
// a loop goroutine that never started) — only db.Close() is needed.
func newInactiveTestTable(transport transport) (*Table, *enode.DB) {
	db := newTestDiscoverDB()
	tab, err := newTable(transport, db, nil, log.New())
	if err != nil {
		panic(err)
	}
	return tab, db
}

// newTestTable creates a Table backed by an in-memory enode.DB and starts
// its main loop, so isInitDone() becomes true and tab.close() works.
func newTestTable(transport transport) (*Table, *enode.DB) {
	tab, db := newInactiveTestTable(transport)
	go tab.loop()
	return tab, db
}

// nodeAtDistance creates a node for which enode.LogDist(base, n.id) == ld.
func nodeAtDistance(base enode.ID, ld int, ip net.IP) *enode.Node {
	var r enr.Record
	r.Set(enr.IP(ip))
	r.Set(enr.UDP(30303))
	return enode.SignNull(&r, idAtDistance(base, ld))
}

// nodesAtDistance creates n nodes for which enode.LogDist(base, node.ID()) == ld.
func nodesAtDistance(base enode.ID, ld int, n int) []*enode.Node {
	results := make([]*enode.Node, n)
	for i := range results {
		results[i] = nodeAtDistance(base, ld, intIP(i))
	}
	return results
}

// idAtDistance returns a random hash such that enode.LogDist(a, b) == n.
func idAtDistance(a enode.ID, n int) (b enode.ID) {
	if n == 0 {
		return a
	}
	b = a
	pos := len(a) - n/8 - 1
	bit := byte(0x01) << (byte(n%8) - 1)
	if bit == 0 {
		pos++
		bit = 0x80
	}
	b[pos] = a[pos]&^bit | ^a[pos]&bit
	for i := pos + 1; i < len(a); i++ {
		b[i] = byte(rand.Intn(255))
	}
	return b
}

// intIP returns a LAN IP address based on i.
func intIP(i int) net.IP {
	return net.IP{10, 0, byte(i >> 8), byte(i & 0xFF)}
}

// fillBucket inserts nodes into the given bucket (via addSeenNode) until full.
func fillBucket(tab *Table, id enode.ID) *node {
	ld := enode.LogDist(tab.self().ID(), id)
	b := tab.bucket(id)
	for len(b.entries) < bucketSize {
		n := wrapNode(nodeAtDistance(tab.self().ID(), ld, intIP(len(b.entries)+1)))
		tab.addSeenNode(n)
	}
	return b.entries[bucketSize-1]
}

// fillTable adds nodes to the table, using addSeenNode or addVerifiedNode.
func fillTable(tab *Table, nodes []*enode.Node, setLive bool) {
	for _, n := range nodes {
		wn := wrapNode(n)
		if setLive {
			wn.livenessChecks = 1
			tab.addVerifiedNode(wn)
		} else {
			tab.addSeenNode(wn)
		}
	}
}

type pingRecorder struct {
	mu      sync.Mutex
	cond    *sync.Cond
	dead    map[enode.ID]bool
	records map[enode.ID]*enode.Node
	pinged  []*enode.Node
	n       *enode.Node
}

func newPingRecorder() *pingRecorder {
	var r enr.Record
	r.Set(enr.IP{0, 0, 0, 0})
	n := enode.SignNull(&r, enode.ID{})

	t := &pingRecorder{
		dead:    make(map[enode.ID]bool),
		records: make(map[enode.ID]*enode.Node),
		n:       n,
	}
	t.cond = sync.NewCond(&t.mu)
	return t
}

func (t *pingRecorder) updateRecord(n *enode.Node) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records[n.ID()] = n
}

// Stubs to satisfy the transport interface.
func (t *pingRecorder) Self() *enode.Node           { return nullNode }
func (t *pingRecorder) lookupSelf() []*enode.Node   { return nil }
func (t *pingRecorder) lookupRandom() []*enode.Node { return nil }

func (t *pingRecorder) waitPing(timeout time.Duration) *enode.Node {
	t.mu.Lock()
	defer t.mu.Unlock()

	done := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		close(done)
		t.cond.Broadcast()
	})
	defer timer.Stop()

	for {
		select {
		case <-done:
			return nil
		default:
		}
		if len(t.pinged) > 0 {
			n := t.pinged[0]
			t.pinged = append(t.pinged[:0], t.pinged[1:]...)
			return n
		}
		t.cond.Wait()
	}
}

// ping simulates a ping request.
func (t *pingRecorder) ping(n *enode.Node) (seq uint64, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.pinged = append(t.pinged, n)
	t.cond.Broadcast()

	if t.dead[n.ID()] {
		return 0, errTimeout
	}
	if t.records[n.ID()] != nil {
		seq = t.records[n.ID()].Seq()
	}
	return seq, nil
}

// RequestENR simulates an ENR request.
func (t *pingRecorder) RequestENR(n *enode.Node) (*enode.Node, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.dead[n.ID()] || t.records[n.ID()] == nil {
		return nil, errTimeout
	}
	return t.records[n.ID()], nil
}

func hasDuplicates(slice []*enode.Node) bool {
	seen := make(map[enode.ID]bool, len(slice))
	for _, e := range slice {
		if e == nil {
			panic("nil *Node in slice")
		}
		if seen[e.ID()] {
			return true
		}
		seen[e.ID()] = true
	}
	return false
}

func sortedByDistanceTo(distbase enode.ID, slice []*enode.Node) bool {
	for i := 1; i < len(slice); i++ {
		if enode.DistCmp(distbase, slice[i].ID(), slice[i-1].ID()) < 0 {
			return false
		}
	}
	return true
}

// hexEncPrivkey decodes h as a private key.
func hexEncPrivkey(h string) *ecdsa.PrivateKey {
	b, err := hex.DecodeString(h)
	if err != nil {
		panic(err)
	}
	key, err := crypto.ToECDSA(b)
	if err != nil {
		panic(err)
	}
	return key
}
