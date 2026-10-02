// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package bitmapdb

import (
	"math"
	"testing"

	"github.com/RoaringBitmap/roaring"
	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

const covTestBucket = kv.E2AccountsHistory

func covTestTx(t *testing.T) kv.RwTx {
	t.Helper()
	_, tx := memdb.NewTestTx(t)
	return tx
}

// --- 32-bit pool helpers ---

func TestCov_NewBitmapPoolRoundTrip(t *testing.T) {
	bm := NewBitmap()
	if bm == nil {
		t.Fatal("NewBitmap returned nil")
	}
	if bm.GetCardinality() != 0 {
		t.Fatalf("expected fresh bitmap from pool to be empty, got cardinality %d", bm.GetCardinality())
	}
	bm.Add(42)
	ReturnToPool(bm)

	// Fetch again; pool item must be clean regardless of reuse.
	bm2 := NewBitmap()
	if bm2.GetCardinality() != 0 {
		t.Fatalf("expected pooled bitmap to be cleared, got cardinality %d", bm2.GetCardinality())
	}
}

func TestCov_NewBitmap64PoolRoundTrip(t *testing.T) {
	bm := NewBitmap64()
	if bm == nil {
		t.Fatal("NewBitmap64 returned nil")
	}
	if bm.GetCardinality() != 0 {
		t.Fatalf("expected fresh bitmap64 from pool to be empty, got cardinality %d", bm.GetCardinality())
	}
	bm.Add(123456789)
	ReturnToPool64(bm)

	bm2 := NewBitmap64()
	if bm2.GetCardinality() != 0 {
		t.Fatalf("expected pooled bitmap64 to be cleared, got cardinality %d", bm2.GetCardinality())
	}
}

// --- CutLeft / CutLeft64 ---

func TestCov_CutLeft_SmallFitsWhole(t *testing.T) {
	bm := roaring.New()
	bm.AddMany([]uint32{1, 2, 3, 100})

	lft := CutLeft(bm, ChunkLimit)
	if lft == nil {
		t.Fatal("expected non-nil result")
	}
	if lft.GetCardinality() != 4 {
		t.Fatalf("expected all 4 elements cut, got %d", lft.GetCardinality())
	}
	if bm.GetCardinality() != 0 {
		t.Fatalf("expected source bitmap to be drained, got %d remaining", bm.GetCardinality())
	}
}

func TestCov_CutLeft_EmptyReturnsNil(t *testing.T) {
	bm := roaring.New()
	if got := CutLeft(bm, ChunkLimit); got != nil {
		t.Fatalf("expected nil for empty bitmap, got %v", got)
	}
}

func TestCov_CutLeft_LargeSplitsIntoChunks(t *testing.T) {
	bm := roaring.New()
	for i := uint32(0); i < 200000; i++ {
		bm.Add(i)
	}
	originalCard := bm.GetCardinality()

	// Use a tiny size limit to force the binary-search splitting branch.
	first := CutLeft(bm, 64)
	if first == nil {
		t.Fatal("expected a non-nil first chunk")
	}
	if first.GetCardinality() == 0 {
		t.Fatal("expected first chunk to carry at least one element")
	}
	if bm.GetCardinality() == 0 {
		t.Fatal("expected remainder bitmap to still have elements")
	}
	if first.GetCardinality()+bm.GetCardinality() != originalCard {
		t.Fatalf("cardinality mismatch: first=%d remainder=%d original=%d",
			first.GetCardinality(), bm.GetCardinality(), originalCard)
	}
}

func TestCov_CutLeft64_SmallFitsWhole(t *testing.T) {
	bm := roaring64.New()
	bm.AddMany([]uint64{1, 2, 3, 100})

	lft := CutLeft64(bm, ChunkLimit)
	if lft == nil {
		t.Fatal("expected non-nil result")
	}
	if lft.GetCardinality() != 4 {
		t.Fatalf("expected all 4 elements cut, got %d", lft.GetCardinality())
	}
	if bm.GetCardinality() != 0 {
		t.Fatalf("expected source bitmap64 to be drained, got %d remaining", bm.GetCardinality())
	}
}

func TestCov_CutLeft64_EmptyReturnsNil(t *testing.T) {
	bm := roaring64.New()
	if got := CutLeft64(bm, ChunkLimit); got != nil {
		t.Fatalf("expected nil for empty bitmap64, got %v", got)
	}
}

func TestCov_CutLeft64_LargeSplitsIntoChunks(t *testing.T) {
	bm := roaring64.New()
	for i := uint64(0); i < 200000; i++ {
		bm.Add(i)
	}
	originalCard := bm.GetCardinality()

	first := CutLeft64(bm, 64)
	if first == nil {
		t.Fatal("expected a non-nil first chunk")
	}
	if first.GetCardinality() == 0 {
		t.Fatal("expected first chunk to carry at least one element")
	}
	if bm.GetCardinality() == 0 {
		t.Fatal("expected remainder bitmap64 to still have elements")
	}
	if first.GetCardinality()+bm.GetCardinality() != originalCard {
		t.Fatalf("cardinality mismatch: first=%d remainder=%d original=%d",
			first.GetCardinality(), bm.GetCardinality(), originalCard)
	}
}

// --- WalkChunks / WalkChunkWithKeys ---

func TestCov_WalkChunks_VisitsAllAndMarksLast(t *testing.T) {
	bm := roaring.New()
	for i := uint32(0); i < 50000; i++ {
		bm.Add(i)
	}

	var total uint64
	var lastSeen int
	err := WalkChunks(bm, 128, func(chunk *roaring.Bitmap, isLast bool) error {
		total += chunk.GetCardinality()
		if isLast {
			lastSeen++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkChunks returned error: %v", err)
	}
	if total != 50000 {
		t.Fatalf("expected to visit 50000 total elements, got %d", total)
	}
	if lastSeen != 1 {
		t.Fatalf("expected exactly one isLast=true chunk, got %d", lastSeen)
	}
}

func TestCov_WalkChunks_PropagatesCallbackError(t *testing.T) {
	bm := roaring.New()
	bm.AddMany([]uint32{1, 2, 3})
	sentinel := errTestSentinel{}
	err := WalkChunks(bm, ChunkLimit, func(chunk *roaring.Bitmap, isLast bool) error {
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("expected sentinel error to propagate, got %v", err)
	}
}

type errTestSentinel struct{}

func (errTestSentinel) Error() string { return "sentinel" }

func TestCov_WalkChunkWithKeys_KeySuffixes(t *testing.T) {
	bm := roaring.New()
	for i := uint32(0); i < 50000; i++ {
		bm.Add(i)
	}
	baseKey := []byte("prefix")

	var keys [][]byte
	err := WalkChunkWithKeys(baseKey, bm, 128, func(chunkKey []byte, chunk *roaring.Bitmap) error {
		cp := make([]byte, len(chunkKey))
		copy(cp, chunkKey)
		keys = append(keys, cp)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkChunkWithKeys returned error: %v", err)
	}
	if len(keys) < 2 {
		t.Fatalf("expected multiple chunk keys for a large bitmap, got %d", len(keys))
	}
	last := keys[len(keys)-1]
	if last[len(last)-4] != 0xff || last[len(last)-3] != 0xff || last[len(last)-2] != 0xff || last[len(last)-1] != 0xff {
		t.Fatalf("expected last key suffix to be 0xffffffff, got % x", last[len(last)-4:])
	}
	for _, k := range keys[:len(keys)-1] {
		if len(k) != len(baseKey)+4 {
			t.Fatalf("expected key length %d, got %d", len(baseKey)+4, len(k))
		}
	}
}

func TestCov_WalkChunkWithKeys64_KeySuffixes(t *testing.T) {
	bm := roaring64.New()
	for i := uint64(0); i < 50000; i++ {
		bm.Add(i)
	}
	baseKey := []byte("prefix64")

	var keys [][]byte
	err := WalkChunkWithKeys64(baseKey, bm, 128, func(chunkKey []byte, chunk *roaring64.Bitmap) error {
		cp := make([]byte, len(chunkKey))
		copy(cp, chunkKey)
		keys = append(keys, cp)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkChunkWithKeys64 returned error: %v", err)
	}
	if len(keys) < 2 {
		t.Fatalf("expected multiple chunk keys for a large bitmap64, got %d", len(keys))
	}
	last := keys[len(keys)-1]
	for _, b := range last[len(last)-8:] {
		if b != 0xff {
			t.Fatalf("expected last key suffix all 0xff, got % x", last[len(last)-8:])
		}
	}
}

// --- SeekInBitmap / SeekInBitmap64 ---

func TestCov_SeekInBitmap(t *testing.T) {
	bm := roaring.New()
	bm.AddMany([]uint32{5, 10, 20})

	if found, ok := SeekInBitmap(bm, 0); !ok || found != 5 {
		t.Fatalf("expected (5,true), got (%d,%v)", found, ok)
	}
	if found, ok := SeekInBitmap(bm, 11); !ok || found != 20 {
		t.Fatalf("expected (20,true), got (%d,%v)", found, ok)
	}
	if _, ok := SeekInBitmap(bm, 21); ok {
		t.Fatal("expected ok=false when seeking past the maximum")
	}
}

func TestCov_SeekInBitmap64(t *testing.T) {
	bm := roaring64.New()

	if _, ok := SeekInBitmap64(bm, 5); ok {
		t.Fatal("expected ok=false for empty bitmap64")
	}

	bm.AddMany([]uint64{5, 10, 20})

	if found, ok := SeekInBitmap64(bm, 0); !ok || found != 5 {
		t.Fatalf("expected (5,true) for n=0, got (%d,%v)", found, ok)
	}
	if found, ok := SeekInBitmap64(bm, 11); !ok || found != 20 {
		t.Fatalf("expected (20,true), got (%d,%v)", found, ok)
	}
	if _, ok := SeekInBitmap64(bm, 21); ok {
		t.Fatal("expected ok=false when seeking past the maximum")
	}
}

// --- Get / Get64 against a real (in-memory MDBX) transaction ---

func putChunked(t *testing.T, tx kv.RwTx, key []byte, bm *roaring.Bitmap) {
	t.Helper()
	err := WalkChunkWithKeys(key, bm.Clone(), ChunkLimit, func(chunkKey []byte, chunk *roaring.Bitmap) error {
		buf, err := chunk.ToBytes()
		if err != nil {
			return err
		}
		return tx.Put(covTestBucket, chunkKey, buf)
	})
	if err != nil {
		t.Fatalf("putChunked failed: %v", err)
	}
}

func putChunked64(t *testing.T, tx kv.RwTx, key []byte, bm *roaring64.Bitmap) {
	t.Helper()
	err := WalkChunkWithKeys64(key, bm.Clone(), ChunkLimit, func(chunkKey []byte, chunk *roaring64.Bitmap) error {
		buf, err := chunk.ToBytes()
		if err != nil {
			return err
		}
		return tx.Put(covTestBucket, chunkKey, buf)
	})
	if err != nil {
		t.Fatalf("putChunked64 failed: %v", err)
	}
}

func TestCov_GetAndTruncateRange(t *testing.T) {
	tx := covTestTx(t)
	key := []byte("idxA")

	bm := roaring.New()
	for i := uint32(0); i < 30000; i++ {
		bm.Add(i)
	}
	// Force multiple chunks by using a tiny ChunkLimit equivalent via direct WalkChunkWithKeys call.
	err := WalkChunkWithKeys(key, bm.Clone(), 512, func(chunkKey []byte, chunk *roaring.Bitmap) error {
		buf, err := chunk.ToBytes()
		if err != nil {
			return err
		}
		return tx.Put(covTestBucket, chunkKey, buf)
	})
	if err != nil {
		t.Fatalf("seeding chunks failed: %v", err)
	}

	got, err := Get(tx, covTestBucket, key, 0, math.MaxUint32)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got.GetCardinality() != 30000 {
		t.Fatalf("expected to read back 30000 elements, got %d", got.GetCardinality())
	}

	// Range-limited Get: only ask for a sub-range; it should still include that range.
	sub, err := Get(tx, covTestBucket, key, 100, 200)
	if err != nil {
		t.Fatalf("ranged Get returned error: %v", err)
	}
	if sub.GetCardinality() == 0 {
		t.Fatal("expected ranged Get to find some elements")
	}

	// Truncate everything from 15000 onward and verify the remainder.
	if err := TruncateRange(tx, covTestBucket, key, 15000); err != nil {
		t.Fatalf("TruncateRange returned error: %v", err)
	}
	after, err := Get(tx, covTestBucket, key, 0, math.MaxUint32)
	if err != nil {
		t.Fatalf("Get after truncate returned error: %v", err)
	}
	if after.GetCardinality() != 15000 {
		t.Fatalf("expected 15000 elements remaining after truncate, got %d", after.GetCardinality())
	}
	if after.GetCardinality() > 0 && after.Maximum() >= 15000 {
		t.Fatalf("expected max element < 15000 after truncate, got %d", after.Maximum())
	}
}

func TestCov_Get_EmptyKeyReturnsEmptyBitmap(t *testing.T) {
	tx := covTestTx(t)
	got, err := Get(tx, covTestBucket, []byte("missing-key"), 0, math.MaxUint32)
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got == nil || got.GetCardinality() != 0 {
		t.Fatalf("expected empty bitmap for missing key, got %v", got)
	}
}

func TestCov_Get64AndTruncateRange64(t *testing.T) {
	tx := covTestTx(t)
	key := []byte("idxB")

	bm := roaring64.New()
	for i := uint64(0); i < 30000; i++ {
		bm.Add(i)
	}
	err := WalkChunkWithKeys64(key, bm.Clone(), 512, func(chunkKey []byte, chunk *roaring64.Bitmap) error {
		buf, err := chunk.ToBytes()
		if err != nil {
			return err
		}
		return tx.Put(covTestBucket, chunkKey, buf)
	})
	if err != nil {
		t.Fatalf("seeding chunks64 failed: %v", err)
	}

	got, err := Get64(tx, covTestBucket, key, 0, math.MaxUint64)
	if err != nil {
		t.Fatalf("Get64 returned error: %v", err)
	}
	if got.GetCardinality() != 30000 {
		t.Fatalf("expected to read back 30000 elements, got %d", got.GetCardinality())
	}

	sub, err := Get64(tx, covTestBucket, key, 100, 200)
	if err != nil {
		t.Fatalf("ranged Get64 returned error: %v", err)
	}
	if sub.GetCardinality() == 0 {
		t.Fatal("expected ranged Get64 to find some elements")
	}

	if err := TruncateRange64(tx, covTestBucket, key, 15000); err != nil {
		t.Fatalf("TruncateRange64 returned error: %v", err)
	}
	after, err := Get64(tx, covTestBucket, key, 0, math.MaxUint64)
	if err != nil {
		t.Fatalf("Get64 after truncate returned error: %v", err)
	}
	if after.GetCardinality() != 15000 {
		t.Fatalf("expected 15000 elements remaining after truncate64, got %d", after.GetCardinality())
	}
	if after.GetCardinality() > 0 && after.Maximum() >= 15000 {
		t.Fatalf("expected max element < 15000 after truncate64, got %d", after.Maximum())
	}
}

func TestCov_Get64_EmptyKeyReturnsEmptyBitmap(t *testing.T) {
	tx := covTestTx(t)
	got, err := Get64(tx, covTestBucket, []byte("missing-key-64"), 0, math.MaxUint64)
	if err != nil {
		t.Fatalf("Get64 returned error: %v", err)
	}
	if got == nil || got.GetCardinality() != 0 {
		t.Fatalf("expected empty bitmap64 for missing key, got %v", got)
	}
}

func TestCov_TruncateRange_NoExistingData(t *testing.T) {
	tx := covTestTx(t)
	// Truncating a key with no stored chunks should succeed as a no-op.
	if err := TruncateRange(tx, covTestBucket, []byte("ghost"), 100); err != nil {
		t.Fatalf("expected nil error truncating absent key, got %v", err)
	}
}

func TestCov_TruncateRange64_NoExistingData(t *testing.T) {
	tx := covTestTx(t)
	if err := TruncateRange64(tx, covTestBucket, []byte("ghost64"), 100); err != nil {
		t.Fatalf("expected nil error truncating absent key64, got %v", err)
	}
}
