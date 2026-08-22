// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// A fleet-wide restart used to wedge every validator permanently. Two recovery
// paths disagreed: the execution layer reverts speculative (uncommitted) blocks
// back to the last committed head, while consensus restores a LockedQC that can
// name exactly the block that revert rolled back. Every leader then extended a
// parent that was no longer the applied head, checkQMDBLeaderSealParent
// rejected the seal as ErrStaleSeal, nothing was broadcast, and the view timed
// out — with no message above Info level to say why.
//
// These tests pin the gate that breaks the loop: production is deferred and the
// parent is re-applied first. The decisive assertion in each case is what the
// block producer was and was not asked to do, because a gate that logs but
// still produces would leave the wedge exactly as it was.

package hotstuff

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// parentAppliedFetcher records the re-apply requests instead of performing them.
type parentAppliedFetcher struct {
	fetched []types.Hash
}

func (f *parentAppliedFetcher) FetchBlockByHash(hash types.Hash) {
	f.fetched = append(f.fetched, hash)
}
func (f *parentAppliedFetcher) CatchUp()             {}
func (f *parentAppliedFetcher) HeightBehind() uint64 { return 0 }

// parentAppliedFixture builds the shape the live wedge had: a committed block
// and the uncommitted child consensus is locked on. The applied marker is left
// to the caller, since which block it names is the whole question.
type parentAppliedFixture struct {
	svc       *Service
	fetcher   *parentAppliedFetcher
	producer  *executionTestProducer
	committed types.Hash
	locked    types.Hash
	sibling   types.Hash
}

func newParentAppliedFixture(t *testing.T) *parentAppliedFixture {
	t.Helper()
	db := memdb.NewTestDB(t)

	committed := &block.Header{
		Number:     uint256.NewInt(14527897),
		Difficulty: uint256.NewInt(0),
	}
	locked := &block.Header{
		ParentHash: committed.Hash(),
		Number:     uint256.NewInt(14527898),
		Difficulty: uint256.NewInt(0),
	}
	// Same height as locked, different block: the ordinary branch-switch shape,
	// which the miner's own align phase owns.
	sibling := &block.Header{
		ParentHash: committed.Hash(),
		Number:     uint256.NewInt(14527898),
		Difficulty: uint256.NewInt(1),
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, committed)
		rawdb.WriteHeader(tx, locked)
		rawdb.WriteHeader(tx, sibling)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fetcher := &parentAppliedFetcher{}
	producer := &executionTestProducer{}
	return &parentAppliedFixture{
		svc: &Service{
			db:            db,
			ctx:           context.Background(),
			blockFetcher:  fetcher,
			blockProducer: producer,
		},
		fetcher:   fetcher,
		producer:  producer,
		committed: committed.Hash(),
		locked:    locked.Hash(),
		sibling:   sibling.Hash(),
	}
}

func (f *parentAppliedFixture) setApplied(t *testing.T, number uint64, hash types.Hash) {
	t.Helper()
	if err := f.svc.db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteQMDBApplied(tx, number, hash)
	}); err != nil {
		t.Fatal(err)
	}
}

// TestLeaderDefersProductionWhenTheParentWasRolledBack is the regression: the
// applied head is the committed block, consensus is locked on its uncommitted
// child, and a leader must NOT seal against a parent the write path will
// reject. It must ask for that parent to be re-applied instead.
func TestLeaderDefersProductionWhenTheParentWasRolledBack(t *testing.T) {
	f := newParentAppliedFixture(t)
	f.setApplied(t, 14527897, f.committed)
	before := metricParentReapplied.Get()

	f.svc.triggerBlockProduction(1242, f.locked)

	if len(f.producer.parents) != 0 {
		t.Fatalf("block production ran on parent %x, which is not the applied head — "+
			"the seal would be dropped as ErrStaleSeal and the view would time out",
			f.producer.parents[0])
	}
	if len(f.fetcher.fetched) != 1 || f.fetcher.fetched[0] != f.locked {
		t.Fatalf("re-apply requests = %x, want exactly [%x] — without it nothing "+
			"ever moves the applied head back onto the locked block", f.fetcher.fetched, f.locked)
	}
	if got := metricParentReapplied.Get(); got != before+1 {
		t.Fatalf("hotstuff_parent_reapplied_total = %d, want %d", got, before+1)
	}
}

// TestLeaderProducesOnceTheParentIsTheAppliedHead is the other half: after the
// re-apply lands, the very next leader must build. A gate that stayed closed
// here would trade one permanent wedge for another.
func TestLeaderProducesOnceTheParentIsTheAppliedHead(t *testing.T) {
	f := newParentAppliedFixture(t)
	f.setApplied(t, 14527898, f.locked)

	f.svc.triggerBlockProduction(1243, f.locked)

	if len(f.producer.parents) != 1 || f.producer.parents[0] != f.locked {
		t.Fatalf("block production parents = %x, want [%x]", f.producer.parents, f.locked)
	}
	if len(f.fetcher.fetched) != 0 {
		t.Fatalf("re-apply requested for %x although the parent already is the applied head",
			f.fetcher.fetched)
	}
}

// TestSiblingBranchIsLeftToTheMinerAlign keeps this gate out of the ordinary
// branch switch. There the applied head is not behind the parent, and unwinding
// onto the proposed branch is something the miner's align phase already does;
// deferring production would stall a case that works today.
func TestSiblingBranchIsLeftToTheMinerAlign(t *testing.T) {
	f := newParentAppliedFixture(t)
	f.setApplied(t, 14527898, f.sibling)

	f.svc.triggerBlockProduction(1244, f.locked)

	if len(f.producer.parents) != 1 || f.producer.parents[0] != f.locked {
		t.Fatalf("block production parents = %x, want [%x] — a same-height sibling is a "+
			"branch switch, not a rolled-back parent", f.producer.parents, f.locked)
	}
	if len(f.fetcher.fetched) != 0 {
		t.Fatalf("re-apply requested for %x on an ordinary branch switch", f.fetcher.fetched)
	}
}

// TestMissingParentHeaderIsFetchedBeforeProducing covers the parent this node
// has never seen: there is no header to compare heights with, and building on
// it cannot work, so the body has to come from a peer first.
func TestMissingParentHeaderIsFetchedBeforeProducing(t *testing.T) {
	f := newParentAppliedFixture(t)
	f.setApplied(t, 14527897, f.committed)
	unknown := types.Hash{0xde, 0xad, 0xbe, 0xef}

	f.svc.triggerBlockProduction(1245, unknown)

	if len(f.producer.parents) != 0 {
		t.Fatalf("block production ran on unknown parent %x", f.producer.parents[0])
	}
	if len(f.fetcher.fetched) != 1 || f.fetcher.fetched[0] != unknown {
		t.Fatalf("re-apply requests = %x, want exactly [%x]", f.fetcher.fetched, unknown)
	}
}

// TestNoAppliedMarkerLeavesProductionAlone pins the non-QMDB path. Without the
// marker there is no speculative-apply tracking and no stale-seal rejection to
// pre-empt, so this gate must be invisible.
func TestNoAppliedMarkerLeavesProductionAlone(t *testing.T) {
	f := newParentAppliedFixture(t)
	// deliberately no setApplied

	f.svc.triggerBlockProduction(1246, f.locked)

	if len(f.producer.parents) != 1 || f.producer.parents[0] != f.locked {
		t.Fatalf("block production parents = %x, want [%x] on a chain with no applied marker",
			f.producer.parents, f.locked)
	}
	if len(f.fetcher.fetched) != 0 {
		t.Fatalf("re-apply requested for %x with no applied marker", f.fetcher.fetched)
	}
}

// TestNoFetcherLeavesProductionAlone keeps the gate from becoming a hard
// dependency on the sync layer: with no way to re-apply anything, deferring
// production would just stop the node.
func TestNoFetcherLeavesProductionAlone(t *testing.T) {
	f := newParentAppliedFixture(t)
	f.setApplied(t, 14527897, f.committed)
	f.svc.blockFetcher = nil

	f.svc.triggerBlockProduction(1247, f.locked)

	if len(f.producer.parents) != 1 || f.producer.parents[0] != f.locked {
		t.Fatalf("block production parents = %x, want [%x] with no fetcher wired",
			f.producer.parents, f.locked)
	}
}
