package replay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
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
		var sourceBuf [8]byte
		binary.BigEndian.PutUint64(sourceBuf[:], sourceHead)
		if err := tx.Put(jmtstore.JMTRootTable, []byte("replay_src_height"), sourceBuf[:]); err != nil {
			return err
		}
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
	stats.CurrentBlock = sourceHead - 1 // persisted replay_src_height is authoritative
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
	if checkpoint.SourceHead != sourceHead {
		t.Fatalf("checkpoint sourceHead = %d, want source head %d", checkpoint.SourceHead, sourceHead)
	}
	if checkpoint.Hash != targetHash.Hex() {
		t.Fatalf("checkpoint hash = %s, want %s", checkpoint.Hash, targetHash.Hex())
	}
}

func TestCheckpointEntryOldConsumersCanIgnoreSourceHead(t *testing.T) {
	type oldCheckpointEntry struct {
		Number uint64 `json:"number"`
		Hash   string `json:"hash"`
	}

	data, err := json.Marshal(CheckpointEntry{
		SourceHead: 100,
		Number:     107,
		Hash:       "0x1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	var old oldCheckpointEntry
	if err := json.Unmarshal(data, &old); err != nil {
		t.Fatal(err)
	}
	if old.Number != 107 || old.Hash != "0x1234" {
		t.Fatalf("old consumer decoded number=%d hash=%q", old.Number, old.Hash)
	}
}
