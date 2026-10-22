package sharedexec

import (
	"context"
	"math/big"
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

func TestCommitReceiptExecutionReaderPreservesExecutedResult(t *testing.T) {
	f, b, _ := certifiedCanonicalFixture(t, false)
	configs := []*params.ChainConfig{
		{},
		{StateScheme: string(params.StateCommitmentPresetEthereumMPT), ByzantiumBlock: big.NewInt(2)},
		{StateScheme: string(params.StateCommitmentPresetEthereumMPT), ByzantiumBlock: big.NewInt(0)},
	}
	if err := f.chain.DB().View(context.Background(), func(tx kv.Tx) error {
		fullBlock, err := rawdb.ReadBlockByHash(tx, b.Hash())
		if err != nil {
			return err
		}
		executionBlock, err := rawdb.ReadBlockByHash(tx, b.Hash())
		if err != nil {
			return err
		}
		senders, err := rawdb.ReadSenders(tx, b.Hash(), b.Number64().Uint64())
		if err != nil {
			return err
		}
		full, err := rawdb.ReadReceiptsWithError(tx, fullBlock, senders)
		if err != nil {
			return err
		}
		execution, err := rawdb.ReadReceiptsForExecutionWithError(tx, executionBlock, senders)
		if err != nil {
			return err
		}
		for _, config := range configs {
			want := internal.ExecutedResultFor(config, b.Number64().Uint64(), b.StateRoot(), full)
			got := internal.ExecutedResultFor(config, b.Number64().Uint64(), b.StateRoot(), execution)
			if got != want {
				t.Fatalf("execution evidence changed result: got %+v want %+v", got, want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// Trusted-owner canonical duplicates use durableStoredCommit too. They must keep
// checking the physical evidence on each call rather than inherit prior success.
func TestTrustedCanonicalDuplicateRechecksExecutionReceipts(t *testing.T) {
	for _, mode := range []string{"valid", "missing-body", "malformed-body", "missing-receipts", "malformed-receipts", "truncated-receipts", "changed-receipt"} {
		t.Run(mode, func(t *testing.T) {
			f, b, request := certifiedCanonicalFixture(t, false)
			owner := [32]byte{1}
			f.server.commitOwner = owner
			first, err := f.server.Commit(context.Background(), request.CommitRequest, owner)
			if err != nil || !first.Committed {
				t.Fatalf("initial commit: %+v %v", first, err)
			}
			if mode != "valid" {
				damageFreshCommitEvidence(t, f, b, mode)
			}
			before, err := f.server.StrictIngressStatus()
			if err != nil {
				t.Fatal(err)
			}
			got, err := f.server.Commit(context.Background(), request.CommitRequest, owner)
			if mode == "valid" {
				if err != nil || !got.Committed || got.HeadHash != first.HeadHash {
					t.Fatalf("valid duplicate: %+v %v", got, err)
				}
			} else if err == nil || got.Committed {
				t.Fatalf("damaged physical evidence accepted: %+v %v", got, err)
			}
			after, err := f.server.StrictIngressStatus()
			if err != nil {
				t.Fatal(err)
			}
			if mode != "valid" && !reflect.DeepEqual(before, after) {
				t.Fatal("duplicate changed ingress state")
			}
		})
	}
}
