// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"errors"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// availChain is a deferred-execution chain whose grandparent G (height 10,
// pre-fork: its result is its own header) is written and whose parent P
// (height 11) this node only holds as an available block, never executed.
// The store holds the frames a child of P references.
type availChain struct {
	bc      *BlockChain
	g, p    *block.Header
	st      *store.Store
	hashes  []types.Hash
	raws    [][]byte
	gasEach uint64
}

func newAvailChain(t *testing.T) *availChain {
	t.Helper()
	prevDeferred := block.DeferredExecutionTime
	block.DeferredExecutionTime = 1000
	t.Cleanup(func() { block.DeferredExecutionTime = prevDeferred })
	db := memdb.NewTestDB(t)
	hc, _ := lru.New[types.Hash, *block.Header](64)
	bc := &BlockChain{ChainDB: db, ctx: context.Background(), headerCache: hc,
		chainConfig: &params.ChainConfig{DeferredExecutionTime: bigInt(1000)}}
	g := &block.Header{Number: uint256.NewInt(10), Time: 999, Root: types.Hash{0xA1}, ReceiptHash: types.Hash{0xA2},
		GasUsed: 5, GasLimit: 1 << 30, Difficulty: uint256.NewInt(0)}
	p := &block.Header{Number: uint256.NewInt(11), Time: 1000, ParentHash: g.Hash(), Root: types.Hash{0xB1},
		GasLimit: 1 << 30, Difficulty: uint256.NewInt(0)}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rawdb.WriteHeader(tx, g)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	bc.RememberAvailable(block.NewBlock(p, nil).(*block.Block))

	keys := frametest.Keys(t, 3, 41)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	raws, hashes, _ := fb.Frames(t, 2, 3)
	st := store.New(1 << 24)
	for i := range raws {
		st.Put(hashes[i], raws[i])
	}
	bc.SetFrameStore(st)
	return &availChain{bc: bc, g: g, p: p, st: st, hashes: hashes, raws: raws, gasEach: 21000}
}

// child builds height 12 on P carrying res as its execution fields.
func (c *availChain) child(res rawdb.ExecutedResult, hashes []types.Hash) *block.Block {
	hdr := &block.Header{Number: uint256.NewInt(12), Time: 1001, ParentHash: c.p.Hash(), GasLimit: 1 << 30,
		Difficulty: uint256.NewInt(0), Root: res.Root, ReceiptHash: res.ReceiptHash, Bloom: res.Bloom, GasUsed: res.GasUsed}
	return block.NewFrameBlockFromReceipt(hdr, hashes, nil, nil, nil).(*block.Block)
}

func gResult(g *block.Header) rawdb.ExecutedResult {
	return rawdb.ExecutedResult{Root: g.Root, ReceiptHash: g.ReceiptHash, Bloom: g.Bloom, GasUsed: g.GasUsed}
}

// TestAvailabilityVoteChecksWithoutExecutingTheParent (P2a item b): with
// the switch on, a frame block on a parent this node never executed is
// voted on once its header, frame list and frames check out; it becomes
// available and is queued for the lag executor. With the switch off the
// same block waits for the parent, as before.
func TestAvailabilityVoteChecksWithoutExecutingTheParent(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	c := newAvailChain(t)
	blk := c.child(gResult(c.g), c.hashes)

	restore := block.SetVoteOnAvailabilityForTest(false)
	checked, retry, err := c.bc.CheckDeferredBlock(blk)
	restore()
	if !checked || !retry || err == nil {
		t.Fatalf("switch off: checked=%v retry=%v err=%v (must wait for the parent)", checked, retry, err)
	}

	defer block.SetVoteOnAvailabilityForTest(true)()
	blk = c.child(gResult(c.g), c.hashes)
	checked, retry, err = c.bc.CheckDeferredBlock(blk)
	if !checked || retry || err != nil {
		t.Fatalf("availability vote: checked=%v retry=%v err=%v", checked, retry, err)
	}
	if c.bc.AvailableBlock(blk.Hash()) == nil || !c.bc.LagImportQueued(blk.Hash()) {
		t.Fatal("a checked block must be available and queued for the lag executor")
	}
	if len(blk.Transactions()) != 6 {
		t.Fatalf("frames not resolved: %d txs", len(blk.Transactions()))
	}
	// Its header is now findable, so a leader can build on it unexecuted.
	if h, _ := c.bc.GetHeaderByHash(blk.Hash()); h == nil {
		t.Fatal("available header not findable")
	}
}

// TestAvailabilityVoteRefuses: a frame list that is not the header's, a
// missing frame (retry: it may be pulled later) and a lagged result that
// differs from this node's execution of the block it reports (the
// equivocation, refused at vote time when the result is known).
func TestAvailabilityVoteRefuses(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	c := newAvailChain(t)

	bad := c.child(gResult(c.g), c.hashes)
	bad.Body().(*block.Body).FrameHashes = []types.Hash{c.hashes[1], c.hashes[0]}
	if _, retry, err := c.bc.CheckDeferredBlock(bad); err == nil || retry {
		t.Fatalf("reordered frame list: retry=%v err=%v", retry, err)
	}

	missing := c.child(gResult(c.g), []types.Hash{c.hashes[0], {0x99}})
	if _, retry, err := c.bc.CheckDeferredBlock(missing); !retry || !errors.Is(err, ErrFramesUnavailable) {
		t.Fatalf("missing frame: retry=%v err=%v", retry, err)
	}

	before := LaggedEquivocations()
	wrong := gResult(c.g)
	wrong.Root = types.Hash{0xEE}
	if _, retry, err := c.bc.CheckDeferredBlock(c.child(wrong, c.hashes)); err == nil || retry {
		t.Fatalf("lagged root mismatch: retry=%v err=%v", retry, err)
	}
	if LaggedEquivocations() != before+1 {
		t.Fatal("lagged equivocation not counted")
	}

	// At depth 1 the same header reports P, which this node has not
	// executed: the result is checked at import, the vote goes ahead.
	restore := block.SetDeferredDepthForTest(1, 0)
	defer restore()
	if _, retry, err := c.bc.CheckDeferredBlock(c.child(wrong, c.hashes)); err != nil || retry {
		t.Fatalf("unknown lagged result must not block the vote: retry=%v err=%v", retry, err)
	}
}

// TestDeferredHeaderCheckDepthTwo: at depth 2 header N must carry N-2's
// result; N-1's is refused, and the depth starts at N42_DEFERRED_DEPTH_FROM.
func TestDeferredHeaderCheckDepthTwo(t *testing.T) {
	cfg := &params.ChainConfig{DeferredExecutionTime: bigInt(1000)}
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	g := &block.Header{Number: uint256.NewInt(10), Time: 999, Root: types.Hash{1}, GasUsed: 1}
	p := &block.Header{Number: uint256.NewInt(11), Time: 1000, ParentHash: g.Hash(), Root: g.Root, GasUsed: 1}
	rawdb.WriteHeader(tx, g)
	rawdb.WriteHeader(tx, p)
	pRes := rawdb.ExecutedResult{Root: types.Hash{2}, GasUsed: 2}
	if err := rawdb.WriteExecutedResult(tx, p.Hash(), pRes); err != nil {
		t.Fatal(err)
	}
	carryG := &block.Header{Number: uint256.NewInt(12), Time: 1001, ParentHash: p.Hash(), Root: g.Root, GasUsed: 1}
	carryP := &block.Header{Number: uint256.NewInt(12), Time: 1001, ParentHash: p.Hash(), Root: pRes.Root, GasUsed: 2}

	defer block.SetDeferredDepthForTest(2, 0)()
	if err := checkDeferredHeader(cfg, tx, carryG, p); err != nil {
		t.Fatalf("depth 2: N carrying N-2's result must pass: %v", err)
	}
	if err := checkDeferredHeader(cfg, tx, carryP, p); err == nil {
		t.Fatal("depth 2: N carrying N-1's result must fail")
	}
	// Depth 2 from height 13: height 12 keeps depth 1.
	block.SetDeferredDepthForTest(2, 13)
	if err := checkDeferredHeader(cfg, tx, carryP, p); err != nil {
		t.Fatalf("below the depth's first height the parent's result applies: %v", err)
	}
	// A short chain reports genesis.
	block.SetDeferredDepthForTest(2, 0)
	gen := &block.Header{Number: uint256.NewInt(0), Root: types.Hash{9}}
	one := &block.Header{Number: uint256.NewInt(1), ParentHash: gen.Hash(), Time: 2000}
	if tgt, err := deferredTarget(one, gen, func(types.Hash, uint64) *block.Header { return nil }); err != nil || tgt != gen {
		t.Fatalf("height 1 at depth 2 must report genesis: %v", err)
	}
}

// TestLagReadyWaitsForNMinusTwo (P2a item c): a leader may propose height N
// on parent N-1 only once it executed N-2; the gate names the block it
// waits for.
func TestLagReadyWaitsForNMinusTwo(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	db := memdb.NewTestDB(t)
	bc := &BlockChain{ChainDB: db, ctx: context.Background(),
		chainConfig: &params.ChainConfig{DeferredExecutionTime: bigInt(1000)}}
	g := &block.Header{Number: uint256.NewInt(20), Time: 1000, Root: types.Hash{1}}
	p := &block.Header{Number: uint256.NewInt(21), Time: 1001, ParentHash: g.Hash()}
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rawdb.WriteHeader(tx, g)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if ready, waitFor, _ := bc.LagReady(p.Hash()); ready || waitFor != p.Hash() {
		t.Fatalf("unknown parent: ready=%v waitFor=%x", ready, waitFor[:4])
	}
	bc.RememberAvailable(block.NewBlock(p, nil).(*block.Block))
	if ready, waitFor, _ := bc.LagReady(p.Hash()); ready || waitFor != g.Hash() {
		t.Fatalf("N-2 not executed: ready=%v waitFor=%x", ready, waitFor[:4])
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return rawdb.WriteExecutedResult(tx, g.Hash(), rawdb.ExecutedResult{Root: types.Hash{3}})
	}); err != nil {
		t.Fatal(err)
	}
	if ready, _, _ := bc.LagReady(p.Hash()); !ready {
		t.Fatal("N-2 executed: the leader may propose N")
	}
	// The header a build on p carries is g's executed result.
	child := &block.Header{Number: uint256.NewInt(22), ParentHash: p.Hash(), Time: 1002}
	rtx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rtx.Rollback()
	res, target, err := bc.LaggedExecutedResult(rtx, child, nil)
	if err != nil || target.Hash() != g.Hash() || res.Root != (types.Hash{3}) {
		t.Fatalf("lagged result: %+v err=%v", res, err)
	}
}
