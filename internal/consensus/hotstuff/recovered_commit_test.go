// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"fmt"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// laggedChainProducer mimics BlockChain.CommitToCanonical under the lag
// writer: a block above the durable applied height is refused ("ahead of
// applied head"); otherwise the canonical rows are rewritten from the block
// down to the canonical chain and the head pointers move.
type laggedChainProducer struct {
	db      kv.RwDB
	applied uint64
	calls   int
}

func (p *laggedChainProducer) TriggerBlockProduction(types.Hash) {}

func (p *laggedChainProducer) CommitToCanonical(hash types.Hash) error {
	p.calls++
	return p.db.Update(context.Background(), func(tx kv.RwTx) error {
		n := rawdb.ReadHeaderNumber(tx, hash)
		if n == nil {
			return fmt.Errorf("committed block %s not in db", hash.Hex())
		}
		if *n > p.applied {
			return fmt.Errorf("commit-to-canonical %d ahead of applied head %d", *n, p.applied)
		}
		if hn := rawdb.ReadHeaderNumber(tx, rawdb.ReadHeadBlockHash(tx)); hn != nil && *hn >= *n {
			if c, _ := rawdb.ReadCanonicalHash(tx, *n); c == hash {
				return nil // late or duplicate commit, as the real guard
			}
		}
		cur, num := hash, *n
		for {
			existing, err := rawdb.ReadCanonicalHash(tx, num)
			if err != nil {
				return err
			}
			if existing == cur {
				break
			}
			if err := rawdb.WriteCanonicalHash(tx, cur, num); err != nil {
				return err
			}
			hdr := rawdb.ReadHeader(tx, cur, num)
			cur, num = hdr.ParentHash, num-1
		}
		rawdb.WriteHeadBlockHash(tx, hash)
		return rawdb.WriteHotStuffCommittedHead(tx, hash)
	})
}

// recoveredChain writes a canonical chain 98..100 and the committed but not
// canonical 101, 102 above it, and returns the hashes by height.
func recoveredChain(t *testing.T, db kv.RwDB) map[uint64]types.Hash {
	t.Helper()
	hashes := map[uint64]types.Hash{}
	var parent types.Hash
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for n := uint64(98); n <= 102; n++ {
			h := &block.Header{ParentHash: parent, Number: uint256.NewInt(n), Difficulty: uint256.NewInt(0), Extra: []byte{byte(n)}}
			rawdb.WriteHeader(tx, h)
			parent = h.Hash()
			hashes[n] = parent
			if n <= 100 {
				if err := rawdb.WriteCanonicalHash(tx, parent, n); err != nil {
					return err
				}
				rawdb.WriteHeadBlockHash(tx, parent)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return hashes
}

func TestCommittedQCAheadOfCanonical(t *testing.T) {
	db := memdb.NewTestDB(t)
	hashes := recoveredChain(t, db)
	sibling := &block.Header{ParentHash: hashes[99], Number: uint256.NewInt(100), Difficulty: uint256.NewInt(0), Extra: []byte{0xEE}}
	siblingChild := &block.Header{ParentHash: sibling.Hash(), Number: uint256.NewInt(101), Difficulty: uint256.NewInt(0)}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, sibling)
		rawdb.WriteHeader(tx, siblingChild)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		hash types.Hash
		want bool
	}{
		{"child of the head", hashes[101], true},
		{"grandchild of the head", hashes[102], true},
		{"the head itself", hashes[100], false},
		{"below the head", hashes[99], false},
		{"another branch", siblingChild.Hash(), false},
		{"unknown", types.Hash{0xDE, 0xAD}, false},
		{"zero", types.Hash{}, false},
	}
	for _, c := range cases {
		var got bool
		if err := db.View(context.Background(), func(tx kv.Tx) error {
			var err error
			got, _, err = CommittedQCAheadOfCanonical(tx, c.hash)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: ahead=%v, want %v", c.name, got, c.want)
		}
	}
}

// TestRestartWithCommittedQCAheadOfCanonical reproduces the S145 recovery
// gap: the committed QC for 102 was persisted while CommitToCanonical was
// deferred (its lagged write not durable), then the node stopped. After the
// restart the state is not reported as diverged, the commit stays pending
// through a notification that arrives before the block is applied, and the
// import that applies it moves canonical to the QC block.
func TestRestartWithCommittedQCAheadOfCanonical(t *testing.T) {
	s, _, _, _ := hsTNewNotifyService(t)
	hashes := recoveredChain(t, s.db)
	producer := &laggedChainProducer{db: s.db, applied: 100}
	s.blockProducer = producer

	qc := QuorumCertificate{View: 18, BlockHash: hashes[102]}
	persisted := &ConsensusState{View: 20, LockedQC: qc, LastCommittedQC: qc, LastVotedView: 19, LastVotedHash: hashes[102]}
	if err := s.db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, persisted)
	}); err != nil {
		t.Fatal(err)
	}
	if got := s.reportPersistedStateDivergence(persisted); got != persistedStateAhead {
		t.Fatalf("status %d, want ahead (not diverged)", got)
	}

	if err := s.recoverState(); err != nil {
		t.Fatal(err)
	}
	if got := s.engine.Engine().LastCommittedQC(); got.BlockHash != hashes[102] {
		t.Fatalf("committed QC not restored: %+v", got)
	}
	if s.recoveredCommit != hashes[102] {
		t.Fatalf("recovered commit %s, want %s", s.recoveredCommit, hashes[102])
	}
	s.retryRecoveredCommit(types.Hash{}) // what Start does
	// The startup revert unwound 101/102: a sync notification of the body
	// arrives before the lag executor applies them again.
	s.NotifyBlockImported(hashes[102], types.Hash{})
	if s.recoveredCommit != hashes[102] {
		t.Fatal("a failed retry dropped the recovered commit")
	}
	assertHead(t, s.db, hashes[100])

	// An unrelated block's import does not retry it.
	calls := producer.calls
	s.NotifyBlockImported(hashes[101], types.Hash{})
	if producer.calls != calls {
		t.Fatal("an unrelated import retried the recovered commit")
	}

	// The lag executor replays 101 and 102; 102's durable write notifies.
	producer.applied = 102
	s.NotifyBlockImported(hashes[102], types.Hash{})
	if s.recoveredCommit != (types.Hash{}) {
		t.Fatal("recovered commit still pending after it became canonical")
	}
	assertHead(t, s.db, hashes[102])
	if err := s.db.View(context.Background(), func(tx kv.Tx) error {
		for n := uint64(98); n <= 102; n++ {
			if c, _ := rawdb.ReadCanonicalHash(tx, n); c != hashes[n] {
				t.Errorf("canonical[%d] = %s, want %s", n, c, hashes[n])
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveredCommitClearedByLaterCommit(t *testing.T) {
	s, _, _, _ := hsTNewNotifyService(t)
	hashes := recoveredChain(t, s.db)
	s.recoveredCommit = hashes[101]
	s.blockProducer = &laggedChainProducer{db: s.db, applied: 102}
	s.handleBlockCommitted(EngineOutput{Type: OutputBlockCommitted, View: 30, Hash: hashes[102]})
	if s.recoveredCommit != (types.Hash{}) {
		t.Fatal("a later commit left the recovered commit pending")
	}
	assertHead(t, s.db, hashes[102])
}

// TestRecoveredUnknownBlockConflictingWithCanonical: the committed QC's
// block was unknown at restart; canonical then reaches its height on
// another hash (sync), and the block is imported. The recovered commit is
// dropped without rewriting canonical.
func TestRecoveredUnknownBlockConflictingWithCanonical(t *testing.T) {
	s, _, _, _ := hsTNewNotifyService(t)
	hashes := recoveredChain(t, s.db)
	producer := &laggedChainProducer{db: s.db, applied: 102}
	s.blockProducer = producer

	other := &block.Header{ParentHash: hashes[100], Number: uint256.NewInt(101), Difficulty: uint256.NewInt(0), Extra: []byte{0xAB}}
	unknown := &block.Header{ParentHash: hashes[100], Number: uint256.NewInt(101), Difficulty: uint256.NewInt(0), Extra: []byte{0xCD}}
	qc := QuorumCertificate{View: 18, BlockHash: unknown.Hash()}
	persisted := &ConsensusState{View: 20, LockedQC: qc, LastCommittedQC: qc}
	if err := s.db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveConsensusState(tx, persisted)
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverState(); err != nil {
		t.Fatal(err)
	}
	if s.recoveredCommit != unknown.Hash() {
		t.Fatal("unknown committed QC block not recovered as a pending commit")
	}
	s.retryRecoveredCommit(types.Hash{})
	if s.recoveredCommit != unknown.Hash() {
		t.Fatal("a missing block dropped the recovered commit")
	}

	// Sync canonicalizes another block at 101.
	hsTWriteHeader(t, s, other)
	if err := producer.CommitToCanonical(other.Hash()); err != nil {
		t.Fatal(err)
	}
	// The recovered block arrives.
	hsTWriteHeader(t, s, unknown)
	calls := producer.calls
	s.NotifyBlockImported(unknown.Hash(), types.Hash{})
	if producer.calls != calls {
		t.Fatal("a recovered block conflicting with canonical was committed")
	}
	if s.recoveredCommit != (types.Hash{}) {
		t.Fatal("conflicting recovered commit not cleared")
	}
	assertHead(t, s.db, other.Hash())
	if err := s.db.View(context.Background(), func(tx kv.Tx) error {
		if c, _ := rawdb.ReadCanonicalHash(tx, 101); c != other.Hash() {
			t.Errorf("canonical[101] rewritten to %s", c)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveredCommitClearedWhenAlreadyCanonical: canonical reached the
// recovered block by another path; the retry clears it without committing.
func TestRecoveredCommitClearedWhenAlreadyCanonical(t *testing.T) {
	s, _, _, _ := hsTNewNotifyService(t)
	hashes := recoveredChain(t, s.db)
	producer := &laggedChainProducer{db: s.db, applied: 102}
	s.blockProducer = producer
	if err := producer.CommitToCanonical(hashes[102]); err != nil {
		t.Fatal(err)
	}
	s.recoveredCommit = hashes[101]
	calls := producer.calls
	s.FlushPendingCommits()
	if s.recoveredCommit != (types.Hash{}) || producer.calls != calls {
		t.Fatalf("recovered=%s calls=%d->%d", s.recoveredCommit, calls, producer.calls)
	}
}

// TestFlushPendingCommitsAtShutdown: a commit deferred in-run whose write
// became durable after the service stopped is finished by the chain's
// drained hook.
func TestFlushPendingCommitsAtShutdown(t *testing.T) {
	s, _, _, _ := hsTNewNotifyService(t)
	hashes := recoveredChain(t, s.db)
	producer := &laggedChainProducer{db: s.db, applied: 100}
	s.blockProducer = producer
	s.pendingCommits = map[types.Hash]struct{}{hashes[101]: {}, hashes[102]: {}}

	s.FlushPendingCommits()
	if len(s.pendingCommits) != 2 {
		t.Fatal("a failed flush dropped pending commits")
	}
	producer.applied = 102
	s.FlushPendingCommits()
	if len(s.pendingCommits) != 0 {
		t.Fatalf("pending commits left: %d", len(s.pendingCommits))
	}
	assertHead(t, s.db, hashes[102])
}

func assertHead(t *testing.T, db kv.RwDB, want types.Hash) {
	t.Helper()
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if got := rawdb.ReadHeadBlockHash(tx); got != want {
			t.Errorf("head %s, want %s", got, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
