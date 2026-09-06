package rawdb

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func TestBadHeaderMarkRoundTrip(t *testing.T) {
	prev := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })
	db := memdb.NewTestDB(t)
	h := types.Hash{0xb7, 0x32, 0x3b}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if IsBadHeaderMarked(tx, h) {
			t.Fatal("unmarked hash reads as bad")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteBadHeaderMark(tx, h, 13751682)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if !IsBadHeaderMarked(tx, h) {
			t.Fatal("mark not persisted")
		}
		if IsBadHeaderMarked(tx, types.Hash{1}) {
			t.Fatal("other hash reads as bad")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
