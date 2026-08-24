package replay

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestRunPostExportUsesTargetHeadAfterGapFill(t *testing.T) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db := memdb.NewTestDB(t)

	const (
		sourceHead = uint64(100)
		targetHead = uint64(107)
	)
	targetHash := types.HexToHash("0x1234")
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := rawdb.WriteHeaderNumber(tx, targetHash, targetHead); err != nil {
			return err
		}
		if err := rawdb.WriteCanonicalHash(tx, targetHash, targetHead); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, targetHash)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	outDir := t.TempDir()
	stats := NewStats()
	stats.CurrentBlock = sourceHead
	e := &EngineV2{
		cfg:   ConfigV2{TargetPath: outDir},
		dstDB: db,
		stats: stats,
	}
	if err := e.RunPostExport(context.Background()); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint CheckpointEntry
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint.Number != targetHead {
		t.Fatalf("checkpoint number = %d, want target head %d (source head was %d)", checkpoint.Number, targetHead, sourceHead)
	}
	if checkpoint.Hash != targetHash.Hex() {
		t.Fatalf("checkpoint hash = %s, want %s", checkpoint.Hash, targetHash.Hex())
	}
}
