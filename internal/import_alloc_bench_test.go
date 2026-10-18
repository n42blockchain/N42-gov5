// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"strconv"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// BenchmarkImportBlockAllocs measures what a follower's import allocates
// per transaction for the fast2 fixture's block (160k distinct-sender
// transfers over Zipf recipients), stage by stage, with the per-block
// scratch pool (N42_BLOCK_SCRATCH_POOL) off and on:
//
//   - decode: the block's Ethereum encodings through a batch decoder, as
//     a frame body is decoded (frame.DecodeTxs);
//   - prep: the ahead preparation (prepareParallelRun) and the base
//     prefill's key lists (aheadBaseKeys);
//   - run: the ahead run on a state set up as the lag pipeline sets it up
//     (prefetch layer, detached pending credits), adopting the
//     preparation, and the receipts' root and compact row.
//
// B/tx is the bytes allocated per transaction (B/op over the block's
// transactions). Run:
//
//	GOMAXPROCS=4 go test -tags nosqlite,noboltdb -run '^$' -bench BenchmarkImportBlockAllocs -benchtime 5x -benchmem ./internal/
func BenchmarkImportBlockAllocs(b *testing.B) {
	fx := fast2BenchFixture()
	db := memdb.NewTestDB(b)
	fundTransferBlockState(b, db, fx)
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(fx.cfg, bc, nil)
	blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
	signer := transaction.MakeSignerWithTimestamp(fx.cfg, fx.header.Number.ToBig(), fx.header.Time)
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	encs := make([][]byte, len(fx.txs))
	for i, tx := range fx.txs {
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			b.Fatal(err)
		}
		encs[i] = enc
	}

	prepare := func(b *testing.B) *aheadPrep {
		prepared, aff := sp.prepareParallelRun(fx.txs, signer, fx.header.BaseFee, parallelWorkers(), aheadPrepWorkers())
		if aff == nil {
			b.Fatal("preparation computed nothing")
		}
		keys := bc.aheadBaseKeys(fx.header, fx.txs, prepared, sp)
		releaseAheadBaseKeys(keys)
		return &aheadPrep{ok: true, txs: fx.txs, signer: signer, baseFee: fx.header.BaseFee, engine: sp.engine != nil,
			prepared: prepared, aff: aff, done: make(chan struct{})}
	}
	run := func(b *testing.B, pr *aheadPrep) {
		roTx, err := db.BeginRo(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		defer roTx.Rollback()
		prefetch := state.NewAccountPrefetch(state.LayerPostStates(nil, state.NewPlainStateReader(roTx)))
		ibs := state.New(prefetch)
		ibs.SetAccountPrefetch(prefetch)
		ibs.SetDetachedPendingCredits(true)
		r, err := sp.executeParallelAhead(blk, ibs, hashFn, &aheadClaim{prep: pr})
		if err != nil {
			b.Fatalf("ExecuteParallelAhead: %v", err)
		}
		if len(r.Receipts) != len(fx.txs) {
			b.Fatalf("receipts %d of %d", len(r.Receipts), len(fx.txs))
		}
		block.ReceiptsDigest(r.Receipts, true, true)
	}

	for _, pool := range []bool{false, true} {
		b.Run("pool="+strconv.FormatBool(pool), func(b *testing.B) {
			defer SetBlockScratchPoolForTest(pool)()
			defer setTransferFast2ForTest(true)()
			run(b, prepare(b)) // warm-up: sender memos, pools
			b.Run("decode", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					d := transaction.NewEthBatchDecoder(len(encs))
					for _, e := range encs {
						if _, err := d.DecodeAliased(e); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
			b.Run("prep", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					prepare(b).prepared.release()
				}
			})
			b.Run("run", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					b.StopTimer()
					pr := prepare(b)
					b.StartTimer()
					run(b, pr)
				}
			})
		})
	}
}
