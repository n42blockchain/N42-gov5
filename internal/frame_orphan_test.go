// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// fillOrder is the leader's frame pick (miner.pickFrames) without its gas
// and count limits: fillable store frames in arrival order, minus the
// frames unwritten blocks hold.
func fillOrder(st *store.Store, inFlight func(types.Hash) bool) []types.Hash {
	var out []types.Hash
	st.ScanFillable(func(h types.Hash, _ store.Entry) bool {
		if !inFlight(h) {
			out = append(out, h)
		}
		return true
	})
	return out
}

// TestOrphanFramesFillFirstAfterCommit reproduces S90's cascade (B1): the
// leader sealed height H twice. The loser A took frames 0-2 (the senders'
// next nonces) and was never written; the winner B took frames 3-5 and was
// committed. Before the fix A's frames stayed in flight for 16 blocks, so
// the next fill started at frame 6 -- every transaction behind a nonce gap,
// ~44 blocks executing nothing. Now the commit of H releases them and the
// next fill takes them first, in arrival order. A loser that WAS written
// (pinned in the store at H) is released by the same commit.
func TestOrphanFramesFillFirstAfterCommit(t *testing.T) {
	keys := frametest.Keys(t, 3, 31)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 9, 2)
	st := store.NewWithKeep(1<<24, 64)
	for i := range raws {
		st.Put(hashes[i], raws[i])
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	bc := &BlockChain{ctx: context.Background(), ChainDB: memdb.NewTestDB(t)}
	bc.SetFrameStore(st)
	const H = 20
	st.Commit(H-1, nil)

	mk := func(extra byte, idx ...int) *block.Block {
		hdr := &block.Header{Number: uint256.NewInt(H), ParentHash: types.Hash{0x19}, Difficulty: uint256.NewInt(0),
			BaseFee: uint256.NewInt(1), GasLimit: 1 << 30, Extra: []byte{extra}}
		var hs []types.Hash
		var rs [][]byte
		for _, i := range idx {
			hs = append(hs, hashes[i])
			rs = append(rs, raws[i])
		}
		b := block.NewFrameBlockFromReceipt(hdr, hs, nil, nil, nil).(*block.Block)
		b.Body().(*block.Body).FrameData = rs
		return b
	}
	a := mk(1, 0, 1, 2) // the loser, sealed first
	b := mk(2, 3, 4, 5) // the winner
	c := mk(3, 6)       // a second loser that this node wrote
	for _, blk := range []*block.Block{a, b, c} {
		bc.MarkFramesInFlight(blk.Body().(*block.Body).FrameHashes, H)
	}
	// B and C are written (C pinned in the store at H), A is not.
	if err := bc.ChainDB.Update(context.Background(), func(tx kv.RwTx) error {
		for _, blk := range []*block.Block{b, c} {
			if err := rawdb.WriteBlock(tx, blk); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	bc.framesPersisted(b)
	bc.framesPersisted(c)
	if got := fillOrder(st, bc.FramesInFlight()); len(got) != 2 || got[0] != hashes[7] {
		t.Fatalf("before the commit the fill must skip every held frame, got %d frames", len(got))
	}

	bc.framesCommitted(b)

	got := fillOrder(st, bc.FramesInFlight())
	want := []types.Hash{hashes[0], hashes[1], hashes[2], hashes[6], hashes[7], hashes[8]}
	if len(got) != len(want) {
		t.Fatalf("fill after the commit took %d frames, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fill position %d: got %x want %x (orphan frames must come first, in arrival order)", i, got[i][:4], want[i][:4])
		}
	}
	if bc.OrphanFramesReleased() != 3 {
		t.Fatalf("released %d in-flight orphan frames, want 3", bc.OrphanFramesReleased())
	}
	if s := st.Stats(); s.Released != 1 || s.Committed != 3 {
		t.Fatalf("store after the commit: %+v", s)
	}
}

// TestReleaseFramesInFlightKeepsLaterHolders: releasing a losing block's
// frames leaves an entry a later block re-recorded.
func TestReleaseFramesInFlightKeepsLaterHolders(t *testing.T) {
	bc := &BlockChain{}
	h1, h2 := types.Hash{1}, types.Hash{2}
	bc.MarkFramesInFlight([]types.Hash{h1, h2}, 7)
	bc.MarkFramesInFlight([]types.Hash{h2}, 8)
	if n := bc.ReleaseFramesInFlight([]types.Hash{h1, h2}, 7); n != 1 {
		t.Fatalf("released %d", n)
	}
	in := bc.FramesInFlight()
	if in(h1) || !in(h2) {
		t.Fatal("wrong entries released")
	}
}
