// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// The follower's per-transaction CPU between a frame body and its first
// execution wave (P14): decode, sender attachment, hash and the parallel
// run's preparation, on 20 frames of 500 transfers (10k transactions).
//
// Run: go test -tags nosqlite,noboltdb -run XXX -bench BenchmarkFrameImportPrep -benchmem ./internal/

const (
	benchFrames   = 20
	benchPerFrame = frame.MaxTxs
)

type frameBenchFixture struct {
	raws   [][]byte
	hashes []types.Hash
	signer transaction.Signer
	st     *store.Store
	sp     *StateProcessor
	bc     *BlockChain
	header *block.Header
}

func newFrameBenchFixture(b *testing.B, withSenders bool) *frameBenchFixture {
	b.Helper()
	keys := frametest.Keys(b, 65, 77)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	raws, hashes, _ := fb.Frames(b, benchFrames, benchPerFrame)
	signer := transaction.LatestSignerForChainID(senderRecoveryChainID)
	st := store.New(1 << 30)
	for i := range raws {
		st.Put(hashes[i], raws[i])
		if withSenders {
			ss, _, err := frame.RecoverSenders(signer, raws[i])
			if err != nil {
				b.Fatal(err)
			}
			st.SetSenders(hashes[i], ss)
		}
	}
	bc := &BlockChain{ctx: context.Background()}
	bc.SetFrameStore(st)
	return &frameBenchFixture{
		raws: raws, hashes: hashes, signer: signer, st: st, bc: bc,
		sp: NewStateProcessor(benchTransferConfig(), bc, coreTFakerEngine{}),
		header: &block.Header{Number: uint256.NewInt(3), Difficulty: uint256.NewInt(0), BaseFee: uint256.NewInt(1),
			GasLimit: 1 << 40, Time: 1},
	}
}

// resolve builds a fresh frame body and resolves it as an import does.
func (f *frameBenchFixture) resolve(b *testing.B) []*transaction.Transaction {
	blk := block.NewFrameBlockFromReceipt(f.header, f.hashes, nil, nil, nil).(*block.Block)
	if err := f.bc.ResolveFrames(blk); err != nil {
		b.Fatal(err)
	}
	txs := blk.Transactions()
	f.bc.ReleaseFramesInFlight(f.hashes, 3)
	f.bc.takeSendersDone(blk.Hash(), f.signer, txs)
	return txs
}

// BenchmarkFrameImportPrep: the import side with admission-cached senders
// (the store holds every frame's senders, as after admission recovery or
// attestation): resolve (decode + attach senders), every hash, and the
// parallel run's preparation (message, hash, affinity key), serially.
func BenchmarkFrameImportPrep(b *testing.B) {
	f := newFrameBenchFixture(b, true)
	b.Run("resolve", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = f.resolve(b)
		}
	})
	b.Run("resolve+hash+prep", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			txs := f.resolve(b)
			for _, tx := range txs {
				tx.Hash()
			}
			prepared, aff := f.sp.prepareParallelRun(txs, f.signer, f.header.BaseFee, 32, 1)
			if aff == nil || !prepared[len(prepared)-1].ok {
				b.Fatal("preparation computed nothing")
			}
		}
	})
}

// BenchmarkFrameAdmissionRecover: admission recovery of the same frames
// (decode + signing hash + ecrecover per transaction), serial.
func BenchmarkFrameAdmissionRecover(b *testing.B) {
	f := newFrameBenchFixture(b, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, raw := range f.raws {
			ss, failed, err := frame.RecoverSenders(f.signer, raw)
			if err != nil || failed != 0 || len(ss.Addrs) != benchPerFrame {
				b.Fatal(err, failed)
			}
		}
	}
}
