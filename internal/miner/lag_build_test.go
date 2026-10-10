// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

func mnTBalance(t *testing.T, f *mnTChainFixture, addr types.Address) *uint256.Int {
	t.Helper()
	var bal *uint256.Int
	if err := f.DB.View(context.Background(), func(tx kv.Tx) error {
		bal = state.New(state.NewPlainStateReader(tx)).GetBalance(addr).Clone()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return bal
}

// TestDropInFlightPending: under lag mode the pool still holds what a local
// frame of an unexecuted block took; the next build leaves each account's
// in-flight prefix out (keeping its later nonces) until the block's height
// is committed, after which the entries are forgotten.
func TestDropInFlightPending(t *testing.T) {
	keys := frametest.Keys(t, 3, 51)
	fb := frametest.NewBuilder(keys[0], big.NewInt(94), keys[1:])
	a0, a1, a2 := fb.Tx(t, 0), fb.Tx(t, 0), fb.Tx(t, 0) // senders 1,2,1
	b0 := fb.Tx(t, 0)                                   // sender 2, nonce 1
	addrA, addrB := types.Address{0xA}, types.Address{0xB}
	pending := map[types.Address][]*transaction.Transaction{addrA: {a0, a2}, addrB: {a1, b0}}

	w := &worker{}
	frame := types.Hash{0xF}
	w.localFrames.txsOf = map[types.Hash][]types.Hash{frame: {a0.Hash(), a1.Hash(), b0.Hash()}}
	w.markLocalFramesInFlight([]types.Hash{frame}, 50)

	if n := w.dropInFlightPending(pending, 48); n != 3 {
		t.Fatalf("dropped %d, want 3", n)
	}
	if len(pending[addrA]) != 1 || pending[addrA][0] != a2 {
		t.Fatal("account A must keep its later nonce")
	}
	if _, ok := pending[addrB]; ok {
		t.Fatal("account B is fully in flight")
	}
	// Committed at 50: the pool drops them itself; nothing is held back.
	again := map[types.Address][]*transaction.Transaction{addrA: {a0, a2}}
	if n := w.dropInFlightPending(again, 50); n != 0 || len(again[addrA]) != 2 {
		t.Fatalf("after the commit: dropped %d", n)
	}
	if len(w.localFrames.inFlightTxs) != 0 {
		t.Fatal("entries not forgotten")
	}
}

// lagTestEngine is the miner fixture's engine with the frame assembly of
// the HotStuff adapter (AssembleWithoutExecution).
type lagTestEngine struct{ mnTFakerEngine }

func (lagTestEngine) AssembleWithoutExecution(_ consensus.ChainHeaderReader, header *block.Header, hashes []types.Hash, data [][]byte) (block.IBlock, error) {
	b := block.NewFrameBlockFromReceipt(header, hashes, nil, nil, nil)
	b.Body().(*block.Body).FrameData = data
	return b, nil
}

// TestCommitWorkLaggedProposesWithoutExecuting (P2a item c): at depth 2 the
// leader's build takes the stored frames in arrival order, stamps this
// node's result of block N-2 (not N-1) into the header, executes nothing
// (the state is untouched), marks the frames in flight and hands a lagged
// task to the sealer; a speculative request is dropped.
func TestCommitWorkLaggedProposesWithoutExecuting(t *testing.T) {
	defer internal.SetFrameBlocksForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	f := mnTNewChainFixture(t)
	b1 := f.mnTBuildAndInsert(t, []*transaction.Transaction{f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(5))})
	b2 := f.mnTBuildAndInsert(t, []*transaction.Transaction{f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(5))})
	st, hashes := frameStoreOf(t, 4, 3)
	f.Chain.SetFrameStore(st)
	// Assembly keeps the stamped execution fields under deferred execution
	// (the fixture chain itself is not deferred: its results are its
	// headers' own fields).
	prevDeferred := block.DeferredExecutionTime
	block.DeferredExecutionTime = 1
	defer func() { block.DeferredExecutionTime = prevDeferred }()

	w := mnTBareWorker(t, f, &mnTStubTxsPool{})
	w.engine = lagTestEngine{}
	w.coinbase = f.Coinbase
	if !w.lagBuildEnabled() {
		t.Fatal("lag build must be enabled at depth 2 with frame blocks and a frame store")
	}
	balBefore := mnTBalance(t, f, f.Senders[1])
	if err := w.commitWork(nil, false, time.Now().Unix(), b2.Hash(), true, time.Now()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.taskCh:
		t.Fatal("a speculative lag build must be dropped")
	default:
	}
	if err := w.commitWork(nil, false, time.Now().Unix(), b2.Hash(), false, time.Now()); err != nil {
		t.Fatal(err)
	}
	var tk *task
	select {
	case tk = <-w.taskCh:
	default:
		t.Fatal("no task handed to the sealer")
	}
	if !tk.lagged || tk.state != nil || len(tk.receipts) != 0 {
		t.Fatal("a lag build carries no state and no receipts")
	}
	hdr := tk.block.Header().(*block.Header)
	b1h := b1.Header().(*block.Header)
	if hdr.Number.Uint64() != 3 || hdr.ParentHash != b2.Hash() {
		t.Fatalf("built %d on %x", hdr.Number.Uint64(), hdr.ParentHash[:4])
	}
	if hdr.Root != b1h.Root || hdr.ReceiptHash != b1h.ReceiptHash || hdr.GasUsed != b1h.GasUsed {
		t.Fatal("depth 2: header 3 must carry block 1's executed result")
	}
	body := tk.block.Body().(*block.Body)
	if len(body.FrameHashes) != len(hashes) || tk.block.TxHash() != block.FrameTxRoot(hashes) {
		t.Fatalf("frames %d, want %d in arrival order", len(body.FrameHashes), len(hashes))
	}
	for i := range hashes {
		if body.FrameHashes[i] != hashes[i] {
			t.Fatalf("frame %d out of arrival order", i)
		}
	}
	if in := f.Chain.FramesInFlight(); !in(hashes[0]) || !in(hashes[3]) {
		t.Fatal("frames of the proposed block not in flight")
	}
	if mnTBalance(t, f, f.Senders[1]).Cmp(balBefore) != 0 {
		t.Fatal("the lag build executed something")
	}
}
