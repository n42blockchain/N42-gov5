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

// An own-unverified mark keeps a block out of convergence without reading
// as a validation failure, and clearing it must not touch a bad mark.
func TestOwnUnverifiedMarkIsNotABadMarkAndClears(t *testing.T) {
	prev := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })
	db := memdb.NewTestDB(t)
	own := types.Hash{0x6d, 0x1b, 0x3e}
	bad := types.Hash{0x2b, 0x10, 0x31}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := WriteOwnUnverifiedMark(tx, own, 13699518); err != nil {
			return err
		}
		return WriteBadHeaderMark(tx, bad, 13694447)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if !IsOwnUnverifiedMarked(tx, own) || IsBadHeaderMarked(tx, own) {
			t.Fatal("own-unverified mark must read as own-unverified, not as bad")
		}
		if IsOwnUnverifiedMarked(tx, bad) || !IsBadHeaderMarked(tx, bad) {
			t.Fatal("bad mark must read as bad, not as own-unverified")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := ClearOwnUnverifiedMark(tx, own); err != nil {
			return err
		}
		return ClearOwnUnverifiedMark(tx, bad) // must be a no-op
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		if IsOwnUnverifiedMarked(tx, own) {
			t.Fatal("own-unverified mark survived the clear")
		}
		if !IsBadHeaderMarked(tx, bad) {
			t.Fatal("clearing an own-unverified mark removed a bad mark")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
