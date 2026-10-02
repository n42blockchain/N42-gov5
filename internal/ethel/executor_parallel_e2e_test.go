// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// executor_parallel_e2e_test.go runs the same synthetic empty-block
// chain through both the sequential and Block-STM parallel executor
// paths and checks they land on the same (empty) state root, exercising
// executeBlockParallel's pre-block/post-block hook sequencing without
// requiring real transactions.

package ethel

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	_ "github.com/n42blockchain/N42/modules" // register tables
	"github.com/n42blockchain/N42/params"
)

func TestExecutorParallelVsSequentialEmptyChain(t *testing.T) {
	dir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, dir, 3)
	defer fz.Close()

	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	runOne := func(parallel bool) types.Hash {
		db := memdb.NewTestDB(t)
		cfg := ExecutorConfig{
			StartBlock:      0,
			EndBlock:        2,
			CommitInterval:  1,
			ParallelEVM:     parallel,
			ParallelWorkers: 2,
		}
		executor := NewExecutor(fz, db, chainCfg, engine, cfg, nil)
		require.NoError(t, executor.Run(context.Background()))

		tx, err := db.BeginRw(context.Background())
		require.NoError(t, err)
		defer tx.Rollback()
		require.NoError(t, InitHashState(tx))
		root, err := VerifyStateRoot(tx)
		require.NoError(t, err)
		return root
	}

	// executeBlockParallel's hook sequencing (DAO/beacon-root pre-block,
	// empty tx loop, Prague/Finalize post-block) runs on an empty chain
	// with zero transactions; both paths must converge on the same
	// (empty) state root.
	seqRoot := runOne(false)
	parRoot := runOne(true)
	require.Equal(t, seqRoot, parRoot)
}
