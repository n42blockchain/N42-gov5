// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// p1Smoke is an in-process network of four profile-1 engines (H2-v4 domain,
// fixed genesis set, two-phase vote gate). Messages are delivered in order
// and instantly. A block is a hash over (parent, view, transactions); every
// node "checks" and "imports" a block when its engine asks to execute it,
// reporting the real parent, so the profile-1 vote gates (deferred check,
// parent import, extends rule, own import before the CommitVote) all run.
type p1Smoke struct {
	t        *testing.T
	setup    *testSetup
	engines  []*ConsensusEngine
	chans    []chan EngineOutput
	parent   map[types.Hash]types.Hash
	txs      map[types.Hash]int
	commits  [][]types.Hash
	seen     []map[types.Hash]bool
	proposed map[ViewNumber]bool
	queue    []rotDelivery
}

func newP1Smoke(t *testing.T) *p1Smoke {
	s := &p1Smoke{
		t: t, setup: newTestSetup(t, 4),
		parent: map[types.Hash]types.Hash{}, txs: map[types.Hash]int{},
		proposed: map[ViewNumber]bool{},
	}
	for i := 0; i < 4; i++ {
		e, ch := newP1Engine(t, s.setup, i)
		s.engines = append(s.engines, e)
		s.chans = append(s.chans, ch)
		s.commits = append(s.commits, nil)
		s.seen = append(s.seen, map[types.Hash]bool{})
	}
	return s
}

// imported queues the deferred check and the import of h at node i.
func (s *p1Smoke) imported(i int, h types.Hash) {
	p := s.parent[h]
	s.queue = append(s.queue,
		rotDelivery{to: i, ev: ConsensusEvent{Type: EventBlockChecked, Hash: h, ParentHash: p}},
		rotDelivery{to: i, ev: ConsensusEvent{Type: EventBlockImported, Hash: h, ParentHash: p}})
}

func (s *p1Smoke) route(from int, o EngineOutput) {
	switch o.Type {
	case OutputBroadcast:
		for j := range s.engines {
			if j != from {
				s.queue = append(s.queue, rotDelivery{to: j, ev: ConsensusEvent{Type: EventMessage, Msg: *o.Message}})
			}
		}
	case OutputSendToValidator:
		s.queue = append(s.queue, rotDelivery{to: int(o.Target), ev: ConsensusEvent{Type: EventMessage, Msg: *o.Message}})
	case OutputExecuteBlock:
		if _, ok := s.parent[o.Hash]; !ok {
			s.t.Fatalf("node %d asked to execute unknown block %x", from, o.Hash[:4])
		}
		s.imported(from, o.Hash)
	case OutputBlockCommitted:
		if !s.seen[from][o.Hash] {
			s.seen[from][o.Hash] = true
			s.commits[from] = append(s.commits[from], o.Hash)
		}
	}
}

func (s *p1Smoke) pump() {
	for {
		progressed := false
		for i, ch := range s.chans {
			for _, o := range drainOutputs(ch) {
				progressed = true
				s.route(i, o)
			}
		}
		for len(s.queue) > 0 {
			d := s.queue[0]
			s.queue = s.queue[1:]
			progressed = true
			if err := s.engines[d.to].ProcessEvent(d.ev); err != nil {
				s.t.Logf("node %d event %d: %v", d.to, d.ev.Type, err)
			}
		}
		if !progressed {
			return
		}
	}
}

// propose lets the leader of its current view build a block on its LockedQC
// block (the genesis block under the genesis QC) and propose it. It reports
// whether a leader proposed.
func (s *p1Smoke) propose() bool {
	for i, e := range s.engines {
		v := e.CurrentView()
		if LeaderForViewWithTenure(v, s.setup.vs, e.leaderTenure) != ValidatorIndex(i) || s.proposed[v] {
			continue
		}
		s.proposed[v] = true
		parent := e.LockedQC().BlockHash
		if parent == (types.Hash{}) {
			parent = p1TestIdentity.GenesisHash
		}
		ntx := 1 + int(v%5)
		buf := make([]byte, 0, 40+ntx*8)
		buf = append(buf, parent[:]...)
		buf = binary.BigEndian.AppendUint64(buf, uint64(v))
		for k := 0; k < ntx; k++ {
			buf = binary.BigEndian.AppendUint64(buf, uint64(v)<<16|uint64(k)) // a transaction
		}
		h := crypto.Keccak256Hash(buf)
		s.parent[h], s.txs[h] = parent, ntx
		s.imported(i, h) // the leader holds its own block
		s.pump()
		if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: h}); err != nil {
			s.t.Fatalf("node %d propose view %d: %v", i, v, err)
		}
		s.pump()
		return true
	}
	return false
}

// TestProfile1SmokeFourValidators: four profile-1 validators produce and
// commit at least 20 blocks carrying transactions, every node commits the
// same hashes in the same order, the committed chain links parent to child
// from the genesis block, and no view times out.
func TestProfile1SmokeFourValidators(t *testing.T) {
	const want = 20
	s := newP1Smoke(t)
	for step := 0; step < 400; step++ {
		done := true
		for _, c := range s.commits {
			if len(c) < want {
				done = false
			}
		}
		if done {
			break
		}
		if !s.propose() {
			t.Fatalf("step %d: no leader can propose (views %d %d %d %d, commits %d %d %d %d)", step,
				s.engines[0].CurrentView(), s.engines[1].CurrentView(), s.engines[2].CurrentView(), s.engines[3].CurrentView(),
				len(s.commits[0]), len(s.commits[1]), len(s.commits[2]), len(s.commits[3]))
		}
	}
	ref := s.commits[0]
	if len(ref) < want {
		t.Fatalf("node 0 committed %d blocks, want >= %d", len(ref), want)
	}
	for i := 1; i < 4; i++ {
		c := s.commits[i]
		n := min(len(c), len(ref))
		if n < want {
			t.Fatalf("node %d committed %d blocks, want >= %d", i, len(c), want)
		}
		for k := 0; k < n; k++ {
			if c[k] != ref[k] {
				t.Fatalf("node %d commit %d = %x, node 0 = %x", i, k, c[k][:4], ref[k][:4])
			}
		}
	}
	prev, txs := p1TestIdentity.GenesisHash, 0
	for k, h := range ref {
		if p := s.parent[h]; p != prev {
			t.Fatalf("commit %d %x: parent %x, want the previous commit %x", k, h[:4], p[:4], prev[:4])
		}
		prev, txs = h, txs+s.txs[h]
	}
	for i, e := range s.engines {
		if n := e.roundState.ConsecutiveTimeouts(); n != 0 {
			t.Fatalf("node %d: %d consecutive timeouts", i, n)
		}
	}
	t.Logf("4 validators committed %d blocks (%d transactions), identical on every node; head %x",
		len(ref), txs, ref[len(ref)-1][:8])
}
