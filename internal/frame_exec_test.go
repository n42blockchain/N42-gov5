// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

type frameExecFixture struct {
	bc     *BlockChain
	sp     *StateProcessor
	header *block.Header
}

func newFrameExecFixture(t *testing.T, funded []*ecdsa.PrivateKey) *frameExecFixture {
	t.Helper()
	db := memdb.NewTestDB(t)
	addrs := make([]types.Address, len(funded))
	for i, k := range funded {
		addrs[i] = crypto.PubkeyToAddress(k.PublicKey)
	}
	fundBenchAccounts(t, db, addrs, uint256.NewInt(1e18))
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	return &frameExecFixture{
		bc: bc,
		sp: NewStateProcessor(benchTransferConfig(), bc, coreTFakerEngine{}),
		header: &block.Header{
			Number: uint256.NewInt(1), Time: uint64(time.Now().Unix()), Difficulty: uint256.NewInt(0),
			BaseFee: uint256.NewInt(1), GasLimit: 1_000_000_000, Coinbase: types.Address{0xC0, 0x1B},
		},
	}
}

type execOut struct {
	root     types.Hash
	receipts block.Receipts
	gas      uint64
	skipped  int
	reasons  FrameSkipCounts
}

func (f *frameExecFixture) run(t *testing.T, blk *block.Block, parallel bool) execOut {
	t.Helper()
	var out execOut
	err := f.bc.ChainDB.View(context.Background(), func(tx kv.Tx) error {
		reader := state.NewPlainStateReader(tx)
		ibs := state.New(reader)
		hashFn := func(uint64) types.Hash { return types.Hash{} }
		var err error
		if parallel {
			out.receipts, _, _, out.gas, err = f.sp.ProcessParallel(blk, ibs, reader, state.NewNoopWriter(), hashFn)
		} else {
			out.receipts, _, _, out.gas, err = f.sp.Process(blk, ibs, reader, state.NewNoopWriter(), hashFn)
		}
		if err != nil {
			return err
		}
		out.skipped = f.sp.LastPhases().Skipped
		out.reasons = f.sp.LastPhases().SkipReasons
		out.root = ibs.IntermediateRoot()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// builderGas runs the leader's frame build to learn the block's gas, as the
// leader stamps it into the header.
func (f *frameExecFixture) builderRun(t *testing.T, txs []*transaction.Transaction) *parallelRun {
	t.Helper()
	var run *parallelRun
	err := f.bc.ChainDB.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		var err error
		run, err = f.sp.BuildParallelFrames(f.header, txs, nil, ibs, func(uint64) types.Hash { return types.Hash{} })
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func sameReceipts(t *testing.T, what string, a, b block.Receipts) {
	t.Helper()
	if len(a) != len(b) {
		t.Fatalf("%s: %d receipts vs %d", what, len(a), len(b))
	}
	for i := range a {
		if a[i].Status != b[i].Status || a[i].GasUsed != b[i].GasUsed || a[i].CumulativeGasUsed != b[i].CumulativeGasUsed ||
			a[i].TxHash != b[i].TxHash || a[i].TransactionIndex != b[i].TransactionIndex {
			t.Fatalf("%s: receipt %d differs: %+v vs %+v", what, i, a[i], b[i])
		}
	}
}

// TestFrameBlockSkipRule executes a frame block holding valid transfers,
// stale and future nonces and an unfunded sender: the invalid ones get skip
// receipts (status 0, gas 0, cumulative gas unchanged), are counted, and the
// result is identical across runs and between the builder, the serial and
// the Block-STM import.
func TestFrameBlockSkipRule(t *testing.T) {
	keys := frametest.Keys(t, 6, 3)
	funded, unfunded := keys[1:5], keys[5]
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, funded)
	f := newFrameExecFixture(t, funded)

	var batch1 []*transaction.Transaction
	for i := 0; i < 12; i++ {
		batch1 = append(batch1, fb.Tx(t, 0))
	}
	var batch2 []*transaction.Transaction
	want := map[int]bool{} // global index -> skipped
	for i := 0; i < 12; i++ {
		switch i % 4 {
		case 1:
			want[len(batch1)+len(batch2)] = true
			batch2 = append(batch2, fb.Tx(t, -1)) // stale nonce
		case 3:
			want[len(batch1)+len(batch2)] = true
			batch2 = append(batch2, fb.Tx(t, 7)) // nonce gap
		default:
			batch2 = append(batch2, fb.Tx(t, 0))
		}
	}
	ub := frametest.NewBuilder(keys[0], senderRecoveryChainID, []*ecdsa.PrivateKey{unfunded})
	want[len(batch1)+len(batch2)] = true
	batch2 = append(batch2, ub.Tx(t, 0)) // cannot pay gas

	raw1, h1 := fb.Frame(t, batch1)
	raw2, h2 := fb.Frame(t, batch2)
	txs, dropped, err := frame.DecodeFrames([][]byte{raw1, raw2}, 2)
	if err != nil || dropped != 0 {
		t.Fatal(err, dropped)
	}

	run := f.builderRun(t, txs)
	if run.Skipped != len(want) || len(run.Included) != len(txs) {
		t.Fatalf("builder: skipped %d included %d", run.Skipped, len(run.Included))
	}
	var wantReasons FrameSkipCounts
	wantReasons[SkipStaleNonce], wantReasons[SkipFutureNonce], wantReasons[SkipCannotPay] = 3, 3, 1
	if run.SkipReasons != wantReasons {
		t.Fatalf("builder skip reasons %v, want %v", run.SkipReasons, wantReasons)
	}
	f.header.GasUsed = run.UsedGas
	blk := block.NewFrameBlockFromReceipt(f.header, []types.Hash{h1, h2}, txs, run.Receipts, nil).(*block.Block)

	for _, parallel := range []bool{false, true} {
		// Fresh decode each time: an imported block arrives without senders.
		again, _, _ := frame.DecodeFrames([][]byte{raw1, raw2}, 2)
		blk2 := block.NewFrameBlockFromReceipt(f.header, []types.Hash{h1, h2}, again, nil, nil).(*block.Block)
		a := f.run(t, blk, parallel)
		b := f.run(t, blk2, parallel)
		if a.root != b.root || a.gas != b.gas {
			t.Fatalf("parallel=%v: nondeterministic root %s vs %s", parallel, a.root, b.root)
		}
		if a.skipped != len(want) || a.gas != run.UsedGas {
			t.Fatalf("parallel=%v: skipped %d gas %d (builder %d)", parallel, a.skipped, a.gas, run.UsedGas)
		}
		if a.reasons != wantReasons || a.reasons.Total() != a.skipped {
			t.Fatalf("parallel=%v: skip reasons %v, want %v", parallel, a.reasons, wantReasons)
		}
		sameReceipts(t, "builder vs import", run.Receipts, a.receipts)
		var cum uint64
		for i, r := range a.receipts {
			if want[i] {
				if r.Status != block.ReceiptStatusFailed || r.GasUsed != 0 || r.CumulativeGasUsed != cum || r.TransactionIndex != uint(i) {
					t.Fatalf("parallel=%v: skip receipt %d wrong: %+v", parallel, i, r)
				}
				continue
			}
			if r.Status != block.ReceiptStatusSuccessful || r.GasUsed != 21000 {
				t.Fatalf("parallel=%v: receipt %d should succeed: %+v", parallel, i, r)
			}
			cum = r.CumulativeGasUsed
		}
	}

	// The same block body without frames (today's rule) fails on the first
	// invalid transaction.
	plain := block.NewBlockFromReceipt(f.header, txs, nil, nil, nil).(*block.Block)
	err = f.bc.ChainDB.View(context.Background(), func(tx kv.Tx) error {
		reader := state.NewPlainStateReader(tx)
		_, _, _, _, err := f.sp.Process(plain, state.New(reader), reader, state.NewNoopWriter(), func(uint64) types.Hash { return types.Hash{} })
		return err
	})
	if err == nil {
		t.Fatal("ordinary block with a stale nonce was accepted")
	}
}

// TestFrameBlockMatchesTxBlock: with nothing to skip, a frame block and an
// ordinary block over the same transactions execute identically on both
// import paths.
func TestFrameBlockMatchesTxBlock(t *testing.T) {
	keys := frametest.Keys(t, 9, 4)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	f := newFrameExecFixture(t, keys[1:])
	raws, hashes, _ := fb.Frames(t, 4, 25)
	decode := func() []*transaction.Transaction {
		txs, _, err := frame.DecodeFrames(raws, 0)
		if err != nil {
			t.Fatal(err)
		}
		return txs
	}
	run := f.builderRun(t, decode())
	if run.Skipped != 0 {
		t.Fatalf("skipped %d", run.Skipped)
	}
	f.header.GasUsed = run.UsedGas
	for _, parallel := range []bool{false, true} {
		fr := f.run(t, block.NewFrameBlockFromReceipt(f.header, hashes, decode(), nil, nil).(*block.Block), parallel)
		tx := f.run(t, block.NewBlockFromReceipt(f.header, decode(), nil, nil, nil).(*block.Block), parallel)
		if fr.root != tx.root || fr.gas != tx.gas || fr.skipped != 0 {
			t.Fatalf("parallel=%v: frame root %s gas %d, tx root %s gas %d", parallel, fr.root, fr.gas, tx.root, tx.gas)
		}
		sameReceipts(t, "frame vs tx block", fr.receipts, tx.receipts)
	}
}

// TestClassifyFrameSkip maps each skippable error, wrapped as the state
// transition wraps it, to its reason.
func TestClassifyFrameSkip(t *testing.T) {
	cases := []struct {
		err  error
		want FrameSkipReason
	}{
		{ErrNonceTooLow, SkipStaleNonce},
		{ErrNonceTooHigh, SkipFutureNonce},
		{ErrInsufficientFunds, SkipCannotPay},
		{ErrInsufficientFundsForTransfer, SkipCannotPay},
		{ErrIntrinsicGas, SkipIntrinsicGas},
		{ErrFeeCapTooLow, SkipFeeCap},
		{ErrTipAboveFeeCap, SkipFeeCap},
		{ErrTipVeryHigh, SkipFeeCap},
		{ErrFeeCapVeryHigh, SkipFeeCap},
		{ErrSenderNoEOA, SkipNonEOA},
		{transaction.ErrInvalidSig, SkipBadSig},
		{transaction.ErrInvalidChainId, SkipBadSig},
		{errors.New("recover: invalid point"), SkipBadSig},
		{ErrNonceMax, SkipOther},
		{ErrGasUintOverflow, SkipOther},
		{ErrTxTypeNotSupported, SkipOther},
	}
	var c FrameSkipCounts
	for _, tc := range cases {
		err := fmt.Errorf("tx 0x01: %w", tc.err)
		if got := ClassifyFrameSkip(err); got != tc.want {
			t.Errorf("%v: reason %d, want %d", tc.err, got, tc.want)
		}
		c.Add(err)
	}
	if c.Total() != len(cases) || c[SkipFeeCap] != 4 || c[SkipBadSig] != 3 || c[SkipOther] != 3 {
		t.Fatalf("counts %v", c)
	}
	LogFrameSkips("import", 1, 10, c.Total(), &c)
}
