package internal

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func TestPerformanceCommitRechecksAppliedAfterMutationLock(t *testing.T) {
	db := newRealignTestDB(t)
	ctx := context.Background()
	genesis := block.NewBlock(&block.Header{Number: uint256.NewInt(0), Difficulty: uint256.NewInt(0)}, nil).(*block.Block)
	b := block.NewBlock(&block.Header{Number: uint256.NewInt(1), ParentHash: genesis.Hash(), Difficulty: uint256.NewInt(0)}, nil).(*block.Block)
	bc := &BlockChain{ChainDB: db, ctx: ctx, qmdbEnabled: true, chainConfig: &params.ChainConfig{Performance: &params.PerformanceChainConfig{}}}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, genesis); err != nil {
			return err
		}
		if err := rawdb.WriteBlock(tx, b); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, genesis.Hash(), 0); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, genesis.Hash())
		return rawdb.WriteQMDBApplied(tx, 1, b.Hash())
	}); err != nil {
		t.Fatal(err)
	}
	bc.lock.Lock()
	done := make(chan error, 1)
	go func() { done <- bc.CommitExecutedExtension(b.Hash()) }()
	select {
	case err := <-done:
		bc.lock.Unlock()
		t.Fatalf("commit bypassed mutation lock: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	// Simulate an importer switching the applied branch while holding bc.lock.
	if err := db.Update(ctx, func(tx kv.RwTx) error { return rawdb.WriteQMDBApplied(tx, 1, types.Hash{1}) }); err != nil {
		bc.lock.Unlock()
		t.Fatal(err)
	}
	bc.lock.Unlock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale applied evidence committed")
		}
	case <-time.After(time.Second):
		t.Fatal("commit did not resume")
	}
	if err := db.View(ctx, func(tx kv.Tx) error {
		if rawdb.ReadHeadBlockHash(tx) != genesis.Hash() || rawdb.ReadHotStuffCommittedHead(tx) != (types.Hash{}) {
			t.Fatal("stale commit changed canonical finality")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPerformanceCommitRejectsDefaultChain(t *testing.T) {
	bc := &BlockChain{}
	if err := bc.CommitExecutedExtension(types.Hash{1}); err == nil {
		t.Fatal("default chain accepted performance commit API")
	}
}
