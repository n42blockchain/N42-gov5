package state

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/ethdb"
)

// TestFindByHistoryStorageSeeksThePlainKey pins the StorageHistory seek key.
// writeIndex stores each chunk under the 52-byte plain storage key
// (addr||slot) plus an 8-byte shard suffix, so the reader must seek with
// exactly that prefix. A seek key that still skipped a 2-byte incarnation
// landed two bytes off, missed the chunk, and GetAsOf silently answered with
// the tip value instead of the as-of value.
func TestFindByHistoryStorageSeeksThePlainKey(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	var addr types.Address
	addr[0] = 0xAB
	var slot types.Hash
	// slot[2:] sorts after slot[:30]: a seek that drops the first two bytes
	// lands past every chunk of this key and misses it.
	slot[2], slot[31] = 0xFF, 0x01
	old := uint256.NewInt(7)
	cur := uint256.NewInt(9)

	// Block 5 changeset + history index: slot changed 7 -> 9 at block 5.
	w := NewChangeSetWriterPlain(tx, 5)
	if err := w.WriteAccountStorage(addr, slot, *old, *cur); err != nil {
		t.Fatalf("write storage: %v", err)
	}
	if err := w.WriteChangeSets(); err != nil {
		t.Fatalf("write changesets: %v", err)
	}
	if err := w.WriteHistory(); err != nil {
		t.Fatalf("write history: %v", err)
	}

	key := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])

	// The index row on disk is the plain key plus the shard suffix.
	histC, err := tx.Cursor(modules.StorageHistory)
	if err != nil {
		t.Fatalf("history cursor: %v", err)
	}
	defer histC.Close()
	k, _, err := histC.Seek(key)
	if err != nil {
		t.Fatalf("seek: %v", err)
	}
	if !bytes.HasPrefix(k, key) || len(k) != len(key)+8 {
		t.Fatalf("index key = %x, want %x + 8-byte shard", k, key)
	}
	csC, err := tx.CursorDupSort(modules.StorageChangeSet)
	if err != nil {
		t.Fatalf("changeset cursor: %v", err)
	}
	defer csC.Close()

	// As-of block 5 (post-state of block 4): the OLD value.
	v, err := FindByHistory(tx, histC, csC, true /* storage */, key, 5)
	if err != nil {
		t.Fatalf("find @5: %v", err)
	}
	if got := new(uint256.Int).SetBytes(v); !got.Eq(old) {
		t.Fatalf("as-of block 5 = %v, want %v", got, old)
	}

	// Past the last change there is no history entry; the caller reads the tip.
	if _, err := FindByHistory(tx, histC, csC, true /* storage */, key, 6); !errors.Is(err, ethdb.ErrKeyNotFound) {
		t.Fatalf("find @6 err = %v, want ErrKeyNotFound", err)
	}

	// The seek key the reader builds is the plain key plus the block number.
	if got := modules.StorageIndexChunkKey(key, 5); !bytes.HasPrefix(got, key) || len(got) != len(key)+8 {
		t.Fatalf("StorageIndexChunkKey = %x, want %x + block", got, key)
	}
}
