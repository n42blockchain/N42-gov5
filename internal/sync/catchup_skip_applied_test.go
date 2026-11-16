// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

type skipAppliedChain struct {
	common.IBlockChain
	applied  map[types.Hash]bool
	held     map[types.Hash]bool
	inserted []types.Hash
	// onInsert runs inside InsertChainAuthorized (the race: the block turns
	// applied between the trim's check and the chain's lock).
	onInsert func(types.Hash)
}

func (c *skipAppliedChain) HasAppliedBlock(h types.Hash, _ uint64) bool  { return c.applied[h] }
func (c *skipAppliedChain) LagPipelineHolds(h types.Hash, _ uint64) bool { return c.held[h] }
func (c *skipAppliedChain) InsertChainAuthorized(chain []block.IBlock) (int, error) {
	for _, b := range chain {
		c.inserted = append(c.inserted, b.Hash())
		if c.onInsert != nil {
			c.onInsert(b.Hash())
		}
	}
	return len(chain), nil
}
func (c *skipAppliedChain) InsertChain(chain []block.IBlock) (int, error) {
	return c.InsertChainAuthorized(chain)
}

type orderNotifier struct{ hashes []types.Hash }

func (n *orderNotifier) NotifyBlockImported(h types.Hash, _ types.Hash) {
	n.hashes = append(n.hashes, h)
}
func (n *orderNotifier) NotifyBlockChecked(types.Hash, types.Hash)             {}
func (n *orderNotifier) NotifyBlockRejected(types.Hash)                        {}
func (n *orderNotifier) NotifyBlockHeaderKnown(types.Hash, types.Hash, uint64) {}

func skipRange() []block.IBlock {
	b1 := testBlock(10, types.Hash{0x10})
	b2 := testBlock(11, b1.Hash())
	b3 := testBlock(12, b2.Hash())
	b4 := testBlock(13, b3.Hash())
	return []block.IBlock{b1, b2, b3, b4}
}

func TestTrimCatchUpAppliedSkipsAppliedAndHeld(t *testing.T) {
	r := skipRange()
	chain := &skipAppliedChain{
		applied: map[types.Hash]bool{r[0].Hash(): true},
		held:    map[types.Hash]bool{r[1].Hash(): true, r[2].Hash(): true},
	}
	svc := newBadBlockTestService(t, chain)
	rest, applied, held := svc.trimCatchUpApplied(r)
	if len(applied) != 1 || applied[0] != r[0] || held != 2 {
		t.Fatalf("applied %d held %d, want 1 and 2", len(applied), held)
	}
	if len(rest) != 1 || rest[0] != r[3] {
		t.Fatalf("rest = %d blocks, want only the last", len(rest))
	}
}

// A genuinely missing block -- and everything after it -- is inserted
// exactly as today, even if a later block of the range is held.
func TestTrimCatchUpAppliedKeepsMissingBlocks(t *testing.T) {
	r := skipRange()
	chain := &skipAppliedChain{
		applied: map[types.Hash]bool{r[0].Hash(): true},
		held:    map[types.Hash]bool{r[2].Hash(): true},
	}
	svc := newBadBlockTestService(t, chain)
	rest, _, held := svc.trimCatchUpApplied(r)
	if held != 0 || len(rest) != 3 || rest[0] != r[1] {
		t.Fatalf("rest = %d (held %d), want blocks 11..13 untouched", len(rest), held)
	}
	_, imported, err := svc.insertCatchUpBlocks(context.Background(), rest, true)
	if err != nil || imported != 3 || len(chain.inserted) != 3 {
		t.Fatalf("insert = (%d, %v), inserted %d, want all three", imported, err, len(chain.inserted))
	}
}

// The race: a block not applied at the trim becomes applied before the
// chain takes its lock. The trim keeps it, so the chain sees it and
// decides (insertChainMode skips it as known under bc.lock).
func TestTrimCatchUpAppliedRaceGoesThroughTheChain(t *testing.T) {
	r := skipRange()
	chain := &skipAppliedChain{applied: map[types.Hash]bool{}, held: map[types.Hash]bool{}}
	chain.onInsert = func(h types.Hash) { chain.applied[h] = true }
	svc := newBadBlockTestService(t, chain)
	rest, applied, held := svc.trimCatchUpApplied(r[:1])
	if len(applied) != 0 || held != 0 || len(rest) != 1 {
		t.Fatal("an unapplied block was trimmed")
	}
	if _, _, err := svc.insertCatchUpBlocks(context.Background(), rest, true); err != nil {
		t.Fatal(err)
	}
	if len(chain.inserted) != 1 {
		t.Fatalf("the chain saw %d inserts, want 1", len(chain.inserted))
	}
}

func TestNotifyCatchUpAppliedInOrder(t *testing.T) {
	r := skipRange()
	n := &orderNotifier{}
	svc := newBadBlockTestService(t, &skipAppliedChain{})
	svc.cfg.blockImportNotifier = n
	svc.notifyCatchUpApplied(r[:2])
	if len(n.hashes) != 2 || n.hashes[0] != r[0].Hash() || n.hashes[1] != r[1].Hash() {
		t.Fatalf("notified %v", n.hashes)
	}
}

func TestBlockArrivingTracksPushInflight(t *testing.T) {
	svc := newBadBlockTestService(t, &skipAppliedChain{})
	h := types.Hash{0x42}
	if svc.BlockArriving(h) {
		t.Fatal("arriving before the push")
	}
	svc.pushInflight.Store(h, struct{}{})
	if !svc.BlockArriving(h) {
		t.Fatal("not arriving during the push")
	}
	svc.pushInflight.Delete(h)
	if svc.BlockArriving(h) {
		t.Fatal("still arriving after the push")
	}
}
