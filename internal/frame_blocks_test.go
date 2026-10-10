// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/internal/ingest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestResolveFramesPullsMissing: a follower holds two of a block's three
// frames; without a puller the block stays unresolved (and is refused by
// body validation, so not voted for); with the proposer reachable over
// FRQ1 the third frame is pulled, admitted, and the block resolves to the
// leader's transaction list.
func TestResolveFramesPullsMissing(t *testing.T) {
	keys := frametest.Keys(t, 4, 5)
	gwAddr := crypto.PubkeyToAddress(keys[0].PublicKey)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	raws, hashes, txs := fb.Frames(t, 3, 30)

	// The proposer's ingest endpoint holds all three.
	leaderStore := store.New(1 << 24)
	for i := range raws {
		leaderStore.Put(hashes[i], raws[i])
	}
	leader := ingest.NewServer("127.0.0.1:0", nil, 1<<30, 1<<30)
	if err := leader.EnableFrames(ingest.FrameConfig{Gateways: []types.Address{gwAddr}, Store: leaderStore}); err != nil {
		t.Fatal(err)
	}
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leader.Stop)

	// The follower lacks frame 1.
	followerStore := store.New(1 << 24)
	followerStore.Put(hashes[0], raws[0])
	followerStore.Put(hashes[2], raws[2])
	follower := ingest.NewServer("127.0.0.1:0", nil, 1<<30, 1<<30)
	if err := follower.EnableFrames(ingest.FrameConfig{Gateways: []types.Address{gwAddr}, Store: followerStore}); err != nil {
		t.Fatal(err)
	}
	bc := &BlockChain{ctx: context.Background(), ChainDB: memdb.NewTestDB(t)}
	bc.SetFrameStore(followerStore)

	hdr := &block.Header{Number: uint256.NewInt(5), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1), GasLimit: 1 << 30}
	sealed := block.NewFrameBlockFromReceipt(hdr, hashes, txs, nil, nil)
	wire, err := rlp.EncodeToBytes(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire) > 1024 {
		t.Fatalf("frame block wire form is %d bytes", len(wire))
	}
	decode := func() *block.Block {
		var b block.Block
		if err := rlp.DecodeBytes(wire, &b); err != nil {
			t.Fatal(err)
		}
		return &b
	}

	blk := decode()
	if err := bc.ResolveFrames(blk); !errors.Is(err, ErrFramesUnavailable) {
		t.Fatalf("resolve without puller: %v", err)
	}
	if blk.Transactions() != nil {
		t.Fatal("partially resolved body")
	}
	v := &BlockValidator{}
	if err := v.validateTxRoot(blk); !errors.Is(err, ErrFramesUnavailable) {
		t.Fatalf("unresolved body passed validation: %v", err)
	}

	bc.SetFramePuller(func(ctx context.Context, want []types.Hash) map[types.Hash][]byte {
		return follower.PullFrames(ctx, []string{leader.Addr()}, want)
	})
	blk = decode()
	if err := bc.ResolveFrames(blk); err != nil {
		t.Fatal(err)
	}
	got := blk.Transactions()
	if len(got) != len(txs) {
		t.Fatalf("resolved %d txs, want %d", len(got), len(txs))
	}
	for i := range txs {
		if got[i].Hash() != txs[i].Hash() {
			t.Fatalf("tx %d differs", i)
		}
	}
	if !followerStore.Has(hashes[1]) {
		t.Fatal("pulled frame not admitted")
	}
	if err := v.validateTxRoot(blk); err != nil {
		t.Fatal(err)
	}
	if in := bc.FramesInFlight(); !in(hashes[0]) || !in(hashes[2]) {
		t.Fatal("resolved frames not in flight")
	}
	// Writing the block pins its frames in the store (not fillable, not
	// evictable) and drops them from the in-flight set.
	bc.framesPersisted(blk)
	if followerStore.Len() != 3 || bc.FramesInFlight()(hashes[1]) {
		t.Fatal("persisted frames dropped or still in flight")
	}
	for _, h := range hashes {
		if !followerStore.Included(h) {
			t.Fatalf("frame %s not pinned by the written block", h)
		}
	}
	// Committing it marks them; KeepBlocks commits later they are evicted
	// and a resend is refused.
	bc.framesCommitted(blk)
	keep := followerStore.Stats().KeepBlocks
	if followerStore.Len() != 3 || followerStore.Stats().Committed != 3 {
		t.Fatalf("after commit: %+v", followerStore.Stats())
	}
	followerStore.Commit(5+keep, nil)
	if followerStore.Len() != 0 || followerStore.Put(hashes[0], raws[0]) {
		t.Fatalf("after the keep margin: %+v", followerStore.Stats())
	}
}

// TestFramesCommittedWalksAncestors: blocks 6 and 7 are written; a sibling
// 6' holds another frame. The commit of 7 also commits 6 (its parent, read
// from the database), so 6's frames are marked committed rather than
// released, while the frame only 6' held becomes fillable again.
func TestFramesCommittedWalksAncestors(t *testing.T) {
	keys := frametest.Keys(t, 3, 21)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 3, 2)
	st := store.NewWithKeep(1<<24, 4)
	for i := range raws {
		st.Put(hashes[i], raws[i])
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	bc := &BlockChain{ctx: context.Background(), ChainDB: memdb.NewTestDB(t)}
	bc.SetFrameStore(st)
	st.Commit(5, nil) // the committed head before this test

	mk := func(n uint64, parent types.Hash, extra byte, idx ...int) *block.Block {
		hdr := &block.Header{Number: uint256.NewInt(n), ParentHash: parent, Difficulty: uint256.NewInt(0),
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
	b6 := mk(6, types.Hash{0x05}, 0, 0)
	b6s := mk(6, types.Hash{0x05}, 1, 2)
	b7 := mk(7, b6.Hash(), 0, 1)
	if err := bc.ChainDB.Update(context.Background(), func(tx kv.RwTx) error {
		for _, b := range []*block.Block{b6, b6s, b7} {
			if err := rawdb.WriteBlock(tx, b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, b := range []*block.Block{b6, b6s, b7} {
		bc.framesPersisted(b)
	}
	for i := range hashes {
		if !st.Included(hashes[i]) {
			t.Fatalf("frame %d not pinned", i)
		}
	}
	bc.framesCommitted(b7)
	if s := st.Stats(); s.Committed != 2 || s.Pending != 0 || s.Released != 1 || s.CommittedHead != 7 {
		t.Fatalf("after commit of 7: %+v", s)
	}
	if st.Included(hashes[2]) || !st.Included(hashes[0]) {
		t.Fatal("fork frame still pinned, or the committed ancestor's frame released")
	}
	// Keep 4: block 6's frame goes at head 10, block 7's at 11.
	st.Commit(10, nil)
	if st.Has(hashes[0]) || !st.Has(hashes[1]) || !st.Has(hashes[2]) {
		t.Fatalf("at head 10: %+v", st.Stats())
	}
}
