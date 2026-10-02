// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// executor_outputs_test.go runs the full Run() path with an output
// freezer configured (NoOutputs=false), which wires up the async output
// writer and drives snapshotOutputs/witness recording on every block —
// not exercised by the no-outFreezer tests in executor_e2e_test.go.

package ethel

import (
	"context"
	"testing"

	"github.com/golang/snappy"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	_ "github.com/n42blockchain/N42/modules" // register tables
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

func mustSnappyEmpty(t *testing.T) []byte {
	t.Helper()
	return snappy.Encode(nil, []byte{})
}

func hashBytes(h *block.Header) []byte {
	hb := h.Hash()
	return hb[:]
}

func TestExecutorRunWithOutputFreezer(t *testing.T) {
	inDir := t.TempDir()
	fz := buildSyntheticGethFreezer(t, inDir, 3)
	defer fz.Close()

	outDir := t.TempDir()
	outFz, err := freezer.New(outDir, 0)
	require.NoError(t, err)
	defer outFz.Close()

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	cfg := ExecutorConfig{
		StartBlock:     0,
		EndBlock:       2,
		CommitInterval: 1,
		VerifyInterval: 1,
	}
	executor := NewExecutor(fz, db, chainCfg, engine, cfg, outFz)
	require.NoError(t, executor.Run(context.Background()))

	// Block-0 genesis accounting + every subsequent block's (empty)
	// changeset must have been written to the witness/acctcs/storcs
	// tables via the async output writer.
	witnessTbl := outFz.Table(freezer.TableBlockWitness)
	require.NotNil(t, witnessTbl)
	require.GreaterOrEqual(t, witnessTbl.Items(), uint64(1))
}

// TestExecutorRunSkipErrors drives a block whose claimed GasUsed cannot
// match an empty block's actual zero gas, with SkipErrors=true so Run
// logs and continues rather than aborting — covering executeBlock's
// gas-mismatch SkipErrors branch and Run's SkipErrors continue path.
func TestExecutorRunSkipErrors(t *testing.T) {
	dir := t.TempDir()
	fzRaw, err := freezer.New(dir, 0)
	require.NoError(t, err)
	defer fzRaw.Close()

	h0 := mkHeader(0, tsAnchor, types.Hash{}, types.Hash{}, EthReceiptHash(nil))
	h1 := mkHeader(1, tsAnchor+1, h0.Hash(), types.Hash{}, EthReceiptHash(nil))
	h1.GasUsed = 21000 // claims gas used, but the body carries no txs

	headers := [][]byte{encodeGethHeader(t, h0), encodeGethHeader(t, h1)}
	bodies := [][]byte{emptyGethBody(t), emptyGethBody(t)}
	receipts := [][]byte{mustSnappyEmpty(t), mustSnappyEmpty(t)}
	hashes := [][]byte{hashBytes(h0), hashBytes(h1)}
	diffs := [][]byte{{0}, {0}}
	require.NoError(t, fzRaw.Freeze(0, &freezer.FreezeData{
		Headers: headers, Bodies: bodies, Receipts: receipts,
		Hashes: hashes, Difficulty: diffs,
	}))

	db := memdb.NewTestDB(t)
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)
	cfg := ExecutorConfig{StartBlock: 0, EndBlock: 1, CommitInterval: 1, SkipErrors: true}
	executor := NewExecutor(fzRaw, db, chainCfg, engine, cfg, nil)
	require.NoError(t, executor.Run(context.Background()), "SkipErrors must swallow the gas mismatch")
}
