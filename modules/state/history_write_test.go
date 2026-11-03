package state

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"

	"github.com/RoaringBitmap/roaring/roaring64"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/ethdb/bitmapdb"
)

// Keep the former read/merge/chunk/write path as an independent disk-format
// oracle, including its chunk selection and roaring normalization.
func writeHistoryReference(tx kv.RwTx, bucket string, key []byte, height uint64) error {
	index, err := bitmapdb.Get64(tx, bucket, key, math.MaxUint32, math.MaxUint32)
	if err != nil {
		return err
	}
	index.Add(height)
	return bitmapdb.WalkChunkWithKeys64(key, index, bitmapdb.ChunkLimit, func(k []byte, chunk *roaring64.Bitmap) error {
		value, err := chunk.ToBytes()
		if err != nil {
			return err
		}
		return tx.Put(bucket, k, value)
	})
}

func TestWriteHistorySortedCursorEquivalence(t *testing.T) {
	for _, keyLen := range []int{20, 52} {
		t.Run(fmt.Sprint(keyLen), func(t *testing.T) {
			bucket := modules.AccountsHistory
			if keyLen == 52 {
				bucket = modules.StorageHistory
			}
			_, actual := memdb.NewTestTx(t)
			_, expected := memdb.NewTestTx(t)
			key := func(i int) []byte {
				k := make([]byte, keyLen)
				binary.BigEndian.PutUint64(k[len(k)-8:], uint64(i))
				return k
			}
			// Seed multiple shards on one key; neighboring keys must not absorb its
			// history and the old finalized shards must remain unchanged.
			for _, tx := range []kv.RwTx{actual, expected} {
				bm := roaring64.New()
				for n := uint64(0); n < 30000; n++ {
					bm.Add(n * 7)
				}
				if err := bitmapdb.WalkChunkWithKeys64(key(50), bm, bitmapdb.ChunkLimit, func(k []byte, chunk *roaring64.Bitmap) error {
					value, err := chunk.ToBytes()
					if err != nil {
						return err
					}
					return tx.Put(bucket, k, value)
				}); err != nil {
					t.Fatal(err)
				}
			}
			order := rand.New(rand.NewSource(42)).Perm(101)
			for _, height := range []uint64{300000, 300001, math.MaxUint32 - 1, math.MaxUint32 + 7} {
				cs := changeset.NewChangeSet()
				for _, n := range order {
					if err := cs.Add(key(n), nil); err != nil {
						t.Fatal(err)
					}
					if err := writeHistoryReference(expected, bucket, key(n), height); err != nil {
						t.Fatal(err)
					}
				}
				if err := writeIndex(height, cs, bucket, actual); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(dumpTable(t, actual, bucket), dumpTable(t, expected, bucket)) {
					t.Fatalf("history table differs at height %d", height)
				}
				for _, n := range []int{0, 50, 100} {
					bm, err := bitmapdb.Get64(actual, bucket, key(n), 0, math.MaxUint64)
					if err != nil {
						t.Fatal(err)
					}
					if !bm.Contains(height) {
						t.Fatalf("key %d missing block %d", n, height)
					}
				}
			}
			// The lookup must still distinguish neighboring full-length storage keys.
			c, err := actual.Cursor(bucket)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			found, _, err := c.Seek(key(101))
			if err != nil || bytes.HasPrefix(found, key(101)) {
				t.Fatalf("unexpected adjacent history: %x, %v", found, err)
			}
		})
	}
}

// Pin roaring's array/run/bitmap representations on either side of the chunk
// threshold: the single-tail shortcut must retain the legacy table bytes.
func TestWriteHistoryTailRepresentations(t *testing.T) {
	_, actual := memdb.NewTestTx(t)
	_, expected := memdb.NewTestTx(t)
	cs := changeset.NewAccountChangeSet()
	for i := 0; i < 40; i++ {
		key := make([]byte, 20)
		key[19] = byte(i)
		if err := cs.Add(key, nil); err != nil {
			t.Fatal(err)
		}
		for _, tx := range []kv.RwTx{actual, expected} {
			bm := roaring64.New()
			switch i % 4 {
			case 0:
				for n := uint64(0); n < 800; n++ {
					bm.Add(n * 17)
				}
			case 1:
				bm.AddRange(0, 3000)
			case 2:
				for n := uint64(0); n < 5000; n++ {
					bm.Add(n * 3)
				}
			case 3:
				bm.Add(1)
				bm.Add(math.MaxUint32 + 10)
				bm.Add(math.MaxUint32 + 30)
			}
			bm.RunOptimize()
			if err := bitmapdb.WalkChunkWithKeys64(key, bm, bitmapdb.ChunkLimit, func(k []byte, chunk *roaring64.Bitmap) error {
				value, err := chunk.ToBytes()
				if err != nil {
					return err
				}
				return tx.Put(modules.AccountsHistory, k, value)
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, height := range []uint64{math.MaxUint32 + 100, math.MaxUint32 + 101} {
		for _, change := range cs.Changes {
			if err := writeHistoryReference(expected, modules.AccountsHistory, change.Key, height); err != nil {
				t.Fatal(err)
			}
		}
		if err := writeIndex(height, cs, modules.AccountsHistory, actual); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(dumpTable(t, actual, modules.AccountsHistory), dumpTable(t, expected, modules.AccountsHistory)) {
			t.Fatalf("tail encoding changed at block %d", height)
		}
	}
}

// Repeated tail bytes must produce the same table as independent merges,
// including after more distinct histories than the bounded memo can retain.
func TestWriteHistoryRepeatedTailEquivalence(t *testing.T) {
	for _, keyLen := range []int{20, 52} {
		t.Run(fmt.Sprint(keyLen), func(t *testing.T) {
			_, actual := memdb.NewTestTx(t)
			_, expected := memdb.NewTestTx(t)
			bucket := modules.AccountsHistory
			if keyLen == 52 {
				bucket = modules.StorageHistory
			}
			cs := changeset.NewChangeSet()
			for _, n := range rand.New(rand.NewSource(53)).Perm(600) {
				key := make([]byte, keyLen)
				binary.BigEndian.PutUint64(key[keyLen-8:], uint64(n))
				if err := cs.Add(key, nil); err != nil {
					t.Fatal(err)
				}
				bm := roaring64.BitmapOf(uint64(n%150), uint64(n%150+1000))
				bm.RunOptimize()
				value, err := bm.ToBytes()
				if err != nil {
					t.Fatal(err)
				}
				tail := append(types.CopyBytes(key), make([]byte, 8)...)
				binary.BigEndian.PutUint64(tail[keyLen:], math.MaxUint64)
				for _, tx := range []kv.RwTx{actual, expected} {
					if err := tx.Put(bucket, tail, value); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, height := range []uint64{5000, 5001, 5001, math.MaxUint32 + 1} {
				for _, change := range cs.Changes {
					if err := writeHistoryReference(expected, bucket, change.Key, height); err != nil {
						t.Fatal(err)
					}
				}
				if err := writeIndex(height, cs, bucket, actual); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(dumpTable(t, actual, bucket), dumpTable(t, expected, bucket)) {
					t.Fatalf("history differs at height %d", height)
				}
			}
		})
	}
}

// The wrapper intentionally exposes its underlying MDBX cursor. The optimized
// writer must still route writes through the wrapper's transaction hook.
type historyPutObserver struct {
	kv.RwTx
	writes int
}

func (tx *historyPutObserver) Put(table string, key, value []byte) error {
	tx.writes++
	return tx.RwTx.Put(table, key, value)
}
func TestWriteHistoryPreservesTransactionPutHooks(t *testing.T) {
	_, underlying := memdb.NewTestTx(t)
	tx := &historyPutObserver{RwTx: underlying}
	cs := changeset.NewAccountChangeSet()
	for i := byte(1); i <= 3; i++ {
		key := make([]byte, 20)
		key[19] = i
		if err := cs.Add(key, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, height := range []uint64{100, 101} {
		if err := writeIndex(height, cs, modules.AccountsHistory, tx); err != nil {
			t.Fatal(err)
		}
	}
	if tx.writes != 6 {
		t.Fatalf("observed %d writes, want 6", tx.writes)
	}
	for _, change := range cs.Changes {
		bm, err := bitmapdb.Get64(underlying, modules.AccountsHistory, change.Key, 0, math.MaxUint64)
		if err != nil {
			t.Fatal(err)
		}
		if !bm.Contains(100) || !bm.Contains(101) || bm.GetCardinality() != 2 {
			t.Fatalf("history lost for key %x: %v", change.Key, bm)
		}
	}
}
