package sharedexec_test

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestMetadataInitPreservesOwnerGenesisWithoutAllocOrExecution(t *testing.T) {
	f := sharedexec.NewExecutionFixtureForTest(t)
	db := memdb.New(t.TempDir())
	defer db.Close()
	genesis := f.Chain.GenesisBlock().(*block.Block)
	for i := 0; i < 2; i++ {
		if err := sharedvalidator.InitializeMetadata(context.Background(), db, f.Chain.Config(), genesis); err != nil {
			t.Fatal(err)
		}
	}
	assertNoExecutionState(t, db)
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		metadata, err := sharedvalidator.IsMetadataStore(tx)
		if err != nil {
			return err
		}
		stored, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if !metadata || stored != genesis.Hash() || rawdb.ReadHeadBlockHash(tx) != genesis.Hash() {
			t.Fatal("metadata changed shared genesis identity")
		}
		_, _, applied, err := rawdb.ReadQMDBApplied(tx)
		if applied {
			t.Fatal("metadata init fabricated applied state")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := sharedvalidator.InitializeMetadata(context.Background(), f.Chain.DB(), f.Chain.Config(), genesis); err == nil {
		t.Fatal("owner account database was relabeled as metadata")
	}
	cfg := *f.Chain.Config()
	performance := *cfg.Performance
	performance.NetworkNamespace += "-different"
	cfg.Performance = &performance
	if err := sharedvalidator.InitializeMetadata(context.Background(), db, &cfg, genesis); err == nil {
		t.Fatal("existing metadata binding silently changed")
	}
}
