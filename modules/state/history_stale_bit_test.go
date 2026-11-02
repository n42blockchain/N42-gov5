package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// A branch switch deletes the reverted block's changesets but leaves its bit
// in the inverted index. The as-of read must step over that bit to the real
// next change; stopping there answered from current state.
func TestFindByHistorySkipsBitOfRevertedBlock(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	addr := types.Address{0: 0xAB}
	slot := types.Hash{31: 0x01}
	key := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])

	// Slot is 7 until block 10 sets it to 9, so as-of block 8 it is 7. Block 9
	// is the reverted sibling: only its index bit survives, not its changeset.
	w := NewChangeSetWriterPlain(tx, 10)
	if err := w.WriteAccountStorage(addr, slot, *uint256.NewInt(7), *uint256.NewInt(9)); err != nil {
		t.Fatalf("write storage: %v", err)
	}
	if err := w.WriteChangeSets(); err != nil {
		t.Fatalf("write changesets: %v", err)
	}
	agg := NewHistoryAggregator()
	agg.AddKey(modules.StorageHistory, key, 9)
	agg.AddKey(modules.StorageHistory, key, 10)
	if err := agg.Flush(tx); err != nil {
		t.Fatalf("flush index: %v", err)
	}

	histC, err := tx.Cursor(modules.StorageHistory)
	if err != nil {
		t.Fatalf("history cursor: %v", err)
	}
	defer histC.Close()
	csC, err := tx.CursorDupSort(modules.StorageChangeSet)
	if err != nil {
		t.Fatalf("changeset cursor: %v", err)
	}
	defer csC.Close()

	v, err := FindByHistory(tx, histC, csC, true /* storage */, key, 8)
	if err != nil {
		t.Fatalf("as-of block 8: %v", err)
	}
	if got := new(uint256.Int).SetBytes(v); !got.Eq(uint256.NewInt(7)) {
		t.Fatalf("as-of block 8 = %v, want 7", got)
	}
}
