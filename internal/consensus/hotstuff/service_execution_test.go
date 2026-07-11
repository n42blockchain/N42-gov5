// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type executionTestFetcher struct {
	applied    bool
	lastHash   types.Hash
	lastNumber uint64
}

func (f *executionTestFetcher) FetchBlockByHash(types.Hash) {}
func (f *executionTestFetcher) CatchUp()                    {}
func (f *executionTestFetcher) HeightBehind() uint64        { return 0 }
func (f *executionTestFetcher) BlockApplied(hash types.Hash, number uint64) bool {
	f.lastHash = hash
	f.lastNumber = number
	return f.applied
}

type executionTestProducer struct {
	parents []types.Hash
}

func (p *executionTestProducer) TriggerBlockProduction(parentHash types.Hash) {
	p.parents = append(p.parents, parentHash)
}
func (p *executionTestProducer) CommitToCanonical(types.Hash) error { return nil }

func newExecutionTestService(t *testing.T, applied bool) (*Service, *executionTestFetcher, types.Hash) {
	t.Helper()
	db := memdb.NewTestDB(t)
	header := &block.Header{
		Number:     uint256.NewInt(42),
		Difficulty: uint256.NewInt(0),
	}
	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, header)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &executionTestFetcher{applied: applied}
	return &Service{
		db:           db,
		ctx:          context.Background(),
		blockFetcher: fetcher,
	}, fetcher, header.Hash()
}

func TestCommittedUnexecutedRaisesMetricAndTracksFailure(t *testing.T) {
	svc, fetcher, hash := newExecutionTestService(t, false)
	before := metricCommittedUnexecuted.Get()

	applied, failures := svc.observeCommittedExecution(hash, true)
	if applied || failures != 1 {
		t.Fatalf("observeCommittedExecution() = (%v, %d), want (false, 1)", applied, failures)
	}
	if got := metricCommittedUnexecuted.Get(); got != before+1 {
		t.Fatalf("hotstuff_committed_unexecuted_total = %d, want %d", got, before+1)
	}
	if fetcher.lastHash != hash || fetcher.lastNumber != 42 {
		t.Fatalf("BlockApplied called with (%s, %d), want (%s, 42)", fetcher.lastHash, fetcher.lastNumber, hash)
	}
	if svc.unexecutedCommitHash != hash || svc.unexecutedCommitFailures != 1 {
		t.Fatal("unexecuted committed block was not tracked")
	}
}

func TestBlockProductionRejectsPersistentlyUnexecutedCommittedParent(t *testing.T) {
	svc, _, hash := newExecutionTestService(t, false)
	producer := new(executionTestProducer)
	svc.blockProducer = producer
	svc.unexecutedCommitHash = hash
	svc.unexecutedCommitFailures = committedUnexecutedRetryLimit - 1

	svc.triggerBlockProduction(10, hash)
	if len(producer.parents) != 0 {
		t.Fatalf("produced on unexecuted committed parent %s", producer.parents[0])
	}
	if svc.unexecutedCommitFailures != committedUnexecutedRetryLimit {
		t.Fatalf("failure count = %d, want %d", svc.unexecutedCommitFailures, committedUnexecutedRetryLimit)
	}
}

func TestBlockProductionPreservesAppliedParentBehavior(t *testing.T) {
	svc, _, hash := newExecutionTestService(t, true)
	producer := new(executionTestProducer)
	svc.blockProducer = producer
	svc.unexecutedCommitHash = hash
	svc.unexecutedCommitFailures = committedUnexecutedRetryLimit - 1

	svc.triggerBlockProduction(10, hash)
	if len(producer.parents) != 1 || producer.parents[0] != hash {
		t.Fatalf("production parents = %v, want [%s]", producer.parents, hash)
	}
	if svc.unexecutedCommitHash != (types.Hash{}) || svc.unexecutedCommitFailures != 0 {
		t.Fatal("successful applied probe did not clear execution guard")
	}
}
