// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
)

// TestParallelTransferBlockDeterministic runs one bench-shaped transfer
// block (sender chains, Zipf recipients) through ExecuteParallelAhead many
// times, two runs at a time as on a leader (its import and an ahead run
// share the executor pools), and requires every run's state root to equal
// the first. N42_TEST_DET_ROUNDS / N42_TEST_DET_TXS / N42_TEST_DET_SENDERS
// scale it (default a 24k block, 4 rounds).
func TestParallelTransferBlockDeterministic(t *testing.T) {
	env := func(k string, d int) int {
		if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
			return v
		}
		return d
	}
	nTxs := env("N42_TEST_DET_TXS", 24000)
	senders := env("N42_TEST_DET_SENDERS", nTxs/20)
	rounds := env("N42_TEST_DET_ROUNDS", 4)
	fx := newTransferBlockFixture(nTxs, senders, 22857, 1.1, 50)
	db := memdb.NewTestDB(t)
	fundTransferBlockStateFiller(t, db, fx, 1000)
	want := runFast2Ahead(t, db, fx, true)
	defer setTransferFast2ForTest(true)()
	for round := 0; round < rounds; round++ {
		var wg sync.WaitGroup
		got := make([]fast2AheadOutcome, 2)
		for lane := range got {
			wg.Add(1)
			go func(lane int) {
				defer wg.Done()
				got[lane] = runAheadNoToggle(t, db, fx)
			}(lane)
		}
		wg.Wait()
		for lane := range got {
			if got[lane].stateRoot != want.stateRoot {
				t.Fatalf("round %d lane %d: state root %x, first run %x", round, lane, got[lane].stateRoot, want.stateRoot)
			}
		}
	}
}

// runAheadNoToggle is runFast2Ahead without the switch's toggle (the
// caller set it), so two runs may share the process.
func runAheadNoToggle(t *testing.T, db kv.RwDB, fx *transferBlockFixture) fast2AheadOutcome {
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(fx.cfg, bc, nil)
	blk := block.NewBlock(fx.header, fx.txs).(*block.Block)
	var out fast2AheadOutcome
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		run, err := sp.ExecuteParallelAhead(blk, ibs, func(uint64) types.Hash { return types.Hash{} })
		if err != nil {
			return err
		}
		out.usedGas = run.UsedGas
		out.stateRoot = ibs.IntermediateRoot()
		return nil
	}))
	return out
}
