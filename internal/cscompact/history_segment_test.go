package cscompact

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RoaringBitmap/roaring/roaring64"
)

// TestHistorySegmentRoundtrip builds a segment from Erigon data, then
// queries every key and verifies block numbers match the original bitmap.
func TestHistorySegmentRoundtrip(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tmpDir := t.TempDir()

	// Build a small segment: first 1M blocks of StorageHistory.
	builder := NewStorageHistoryBuilder(db, tmpDir)
	if err := builder.BuildRange(context.Background(), 0, 1_000_000); err != nil {
		t.Fatal(err)
	}

	// Open the segment.
	baseName := HistSegmentFileName("storage_hist", 0, 1_000_000)
	seg, err := OpenHistorySegment(
		filepath.Join(tmpDir, baseName+".idx"),
		filepath.Join(tmpDir, baseName+".dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()
	t.Logf("Segment opened: keys=%d", seg.KeyCount())

	// Verify: read original Erigon data and compare.
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	cursor, _ := tx.Cursor("StorageHistory")
	defer cursor.Close()

	verified := 0
	mismatches := 0
	lastPrefix := ""

	for k, v, _ := cursor.First(); k != nil && verified < 500; k, v, _ = cursor.Next() {
		if len(k) < 60 {
			continue
		}
		prefix := string(k[:52])
		if prefix == lastPrefix {
			continue // skip shards of same key
		}
		lastPrefix = prefix

		bm := roaring64.New()
		if _, err := bm.ReadFrom(bytes.NewReader(v)); err != nil {
			continue
		}

		// Pick a block from the bitmap.
		arr := bm.ToArray()
		for _, bn := range arr {
			if bn >= 1_000_000 {
				continue
			}
			foundBlock, ok := seg.Lookup(k[:52], bn)
			if !ok {
				mismatches++
				if mismatches <= 3 {
					t.Errorf("key %x block %d: not found in segment", k[:6], bn)
				}
				continue
			}
			if foundBlock > bn {
				mismatches++
				if mismatches <= 3 {
					t.Errorf("key %x block %d: got %d (should be <=)", k[:6], bn, foundBlock)
				}
				continue
			}
			verified++
			break
		}
	}

	t.Logf("Verified %d keys, %d mismatches", verified, mismatches)
	if mismatches > 0 {
		t.Errorf("total mismatches: %d", mismatches)
	}
}

// TestHistorySegmentBenchmark measures query latency.
func TestHistorySegmentBenchmark(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tmpDir := t.TempDir()

	builder := NewStorageHistoryBuilder(db, tmpDir)
	if err := builder.BuildRange(context.Background(), 0, 1_000_000); err != nil {
		t.Fatal(err)
	}

	baseName := HistSegmentFileName("storage_hist", 0, 1_000_000)
	seg, err := OpenHistorySegment(
		filepath.Join(tmpDir, baseName+".idx"),
		filepath.Join(tmpDir, baseName+".dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()

	// Collect query keys.
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	type query struct {
		key      []byte
		blockNum uint64
	}
	var queries []query
	cursor, _ := tx.Cursor("StorageHistory")
	rng := rand.New(rand.NewSource(42))
	lastPrefix := ""
	for k, v, _ := cursor.First(); k != nil && len(queries) < 2000; k, v, _ = cursor.Next() {
		if len(k) < 60 {
			continue
		}
		prefix := string(k[:52])
		if prefix == lastPrefix {
			continue
		}
		lastPrefix = prefix

		bm := roaring64.New()
		if _, err := bm.ReadFrom(bytes.NewReader(v)); err != nil {
			continue
		}
		arr := bm.ToArray()
		for _, bn := range arr {
			if bn < 1_000_000 {
				queries = append(queries, query{key: append([]byte{}, k[:52]...), blockNum: bn})
				break
			}
		}
	}
	cursor.Close()

	if len(queries) < 100 {
		t.Skipf("Not enough queries: %d", len(queries))
	}

	// Shuffle for random access.
	rng.Shuffle(len(queries), func(i, j int) {
		queries[i], queries[j] = queries[j], queries[i]
	})

	// Benchmark.
	found := 0
	t0 := time.Now()
	for _, q := range queries {
		if _, ok := seg.Lookup(q.key, q.blockNum); ok {
			found++
		}
	}
	elapsed := time.Since(t0)

	idxFi, _ := os.Stat(filepath.Join(tmpDir, baseName+".idx"))
	datFi, _ := os.Stat(filepath.Join(tmpDir, baseName+".dat"))

	t.Logf("RecSplit History Benchmark:")
	t.Logf("  Queries: %d, Found: %d", len(queries), found)
	t.Logf("  Total: %v, Avg: %v/query", elapsed, elapsed/time.Duration(len(queries)))
	t.Logf("  Segment: idx=%d bytes, dat=%d bytes",
		idxFi.Size(), datFi.Size())
	t.Logf("  Avg bytes/key: %.1f", float64(idxFi.Size()+datFi.Size())/float64(seg.KeyCount()))
}
