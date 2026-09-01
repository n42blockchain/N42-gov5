package cscompact

import (
	"bytes"
	"context"
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/RoaringBitmap/roaring/roaring64"

	"github.com/n42blockchain/N42/lib/kv/bitmapdb"
	"github.com/n42blockchain/N42/lib/recsplit"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

// TestHistoryGetAsOfBenchmark benchmarks 2000 random GetAsOf queries:
// (1) Current MDBX history + changeset
// (2) RecSplit + adaptive value (simulated)
func TestHistoryGetAsOfBenchmark(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Step 1: Collect real storage keys from StorageHistory for test queries.
	t.Log("Collecting sample keys from StorageHistory...")
	type queryKey struct {
		histKey  []byte // addr(20)+slot(32) = 52B for history lookup
		blockNum uint64
	}

	var samples []queryKey
	cursor, err := tx.Cursor("StorageHistory")
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(42))
	skip := 0
	for k, v, err := cursor.First(); k != nil && len(samples) < 3000; k, v, err = cursor.Next() {
		if err != nil {
			break
		}
		skip++
		// Sample randomly (skip ~90% of entries for spread).
		if rng.Intn(10) != 0 {
			continue
		}
		if len(k) < 60 { // addr(20)+slot(32)+shardId(8)
			continue
		}

		// Parse bitmap to get a real block number.
		bm := roaring64.New()
		if _, err := bm.ReadFrom(bytes.NewReader(v)); err != nil {
			continue
		}
		if bm.GetCardinality() == 0 {
			continue
		}

		// Pick a random block from the bitmap.
		arr := bm.ToArray()
		blockNum := arr[rng.Intn(len(arr))]

		samples = append(samples, queryKey{
			histKey:  k[:52], // addr+slot
			blockNum: blockNum,
		})
	}
	cursor.Close()

	if len(samples) < 100 {
		t.Fatalf("Not enough samples: %d (need >=100)", len(samples))
	}
	if len(samples) > 2000 {
		samples = samples[:2000]
	}
	t.Logf("Collected %d sample queries (scanned %d entries)", len(samples), skip)

	// ===== Benchmark 1: Current MDBX History (Seek + Roaring decode) =====
	t.Log("\n=== Benchmark: MDBX History (Seek + Roaring) ===")

	found := 0
	t0 := time.Now()
	for _, q := range samples {
		// Direct history seek: key = addr(20)+slot(32)+shardId(8).
		// IndexChunkKey builds the same shape: addr(20)+slot(32)+blockNum(8).
		histCursor2, _ := tx.Cursor("StorageHistory")
		seekKey := make([]byte, 60)
		copy(seekKey[:20], q.histKey[:20])    // addr
		copy(seekKey[20:52], q.histKey[20:])  // slot
		binary.BigEndian.PutUint64(seekKey[52:], q.blockNum)

		k, v, err := histCursor2.Seek(seekKey)
		histCursor2.Close()
		if err != nil || k == nil {
			continue
		}
		if len(k) < 52 || !bytes.Equal(k[:52], q.histKey[:52]) {
			continue
		}
		// Decode Roaring bitmap.
		bm := roaring64.New()
		if _, err := bm.ReadFrom(bytes.NewReader(v)); err != nil {
			continue
		}
		csBlock, ok := bitmapdb.SeekInBitmap64(bm, q.blockNum)
		if !ok {
			continue
		}
		_ = csBlock
		found++
	}
	mdbxTime := time.Since(t0)
	t.Logf("  MDBX: %d queries, %d found, total=%v, avg=%v/query",
		len(samples), found, mdbxTime, mdbxTime/time.Duration(len(samples)))

	// ===== Benchmark 2: RecSplit + Adaptive Value =====
	// Build a RecSplit index from the same keys, then query it.
	t.Log("\n=== Building RecSplit index for benchmark ===")

	tmpDir := t.TempDir()
	idxPath := filepath.Join(tmpDir, "bench.idx")
	datPath := filepath.Join(tmpDir, "bench.dat")

	// Collect all unique (addr+slot) keys and their block number lists.
	type histEntry struct {
		key       [52]byte
		blocks    []uint64 // sorted block numbers
	}

	// Read up to 50K unique keys for the RecSplit index.
	var histEntries []histEntry
	histMap := make(map[[52]byte]int) // key → index in histEntries

	cursor2, _ := tx.Cursor("StorageHistory")
	for k, v, err := cursor2.First(); k != nil && len(histEntries) < 50000; k, v, err = cursor2.Next() {
		if err != nil || len(k) < 60 {
			continue
		}
		var hk [52]byte
		copy(hk[:], k[:52])

		bm := roaring64.New()
		if _, err := bm.ReadFrom(bytes.NewReader(v)); err != nil {
			continue
		}

		if idx, ok := histMap[hk]; ok {
			histEntries[idx].blocks = append(histEntries[idx].blocks, bm.ToArray()...)
		} else {
			histMap[hk] = len(histEntries)
			histEntries = append(histEntries, histEntry{
				key:    hk,
				blocks: bm.ToArray(),
			})
		}
	}
	cursor2.Close()
	t.Logf("  Indexed %d unique keys", len(histEntries))

	// Build RecSplit.
	logger := log2.New()
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:           len(histEntries),
		BucketSize:         2000,
		LeafSize:           8,
		Enums:              false,
		LessFalsePositives: true,
		IndexFile:          idxPath,
		BaseDataID:         0,
		TmpDir:             os.TempDir(),
	}, logger)
	if err != nil {
		t.Fatal(err)
	}

	// Build dat file: per entry [1B count][varint blockNums...]
	datBuf := make([]byte, 0, len(histEntries)*8)
	offsets := make([]uint64, len(histEntries))
	for i, e := range histEntries {
		offsets[i] = uint64(len(datBuf))
		if err := rs.AddKey(e.key[:], uint64(i)); err != nil {
			t.Fatal(err)
		}
		// Adaptive encoding.
		if len(e.blocks) <= 255 {
			datBuf = append(datBuf, byte(len(e.blocks)))
		} else {
			datBuf = append(datBuf, 0xFF)
			datBuf = binary.BigEndian.AppendUint32(datBuf, uint32(len(e.blocks)))
		}
		for _, b := range e.blocks {
			datBuf = appendVarint(datBuf, b)
		}
	}

	if err := rs.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(datPath, datBuf, 0644)

	// Open for querying.
	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	reader := recsplit.NewIndexReader(idx)

	t.Logf("  RecSplit built: idx=%d bytes, dat=%d bytes",
		mustFileSize(idxPath), len(datBuf))
	t.Logf("  Avg bytes/key: idx=%.1f dat=%.1f total=%.1f",
		float64(mustFileSize(idxPath))/float64(len(histEntries)),
		float64(len(datBuf))/float64(len(histEntries)),
		float64(mustFileSize(idxPath)+int64(len(datBuf)))/float64(len(histEntries)))

	// Benchmark RecSplit queries.
	t.Log("\n=== Benchmark: RecSplit + Adaptive Value ===")
	found2 := 0
	t1 := time.Now()
	for _, q := range samples {
		var hk [52]byte
		copy(hk[:20], q.histKey[:20])
		copy(hk[20:], q.histKey[20:52])

		ordinal, ok := reader.Lookup(hk[:])
		if !ok {
			continue
		}
		if ordinal >= uint64(len(histEntries)) {
			continue
		}

		// Read adaptive value from dat.
		off := offsets[ordinal]
		if int(off) >= len(datBuf) {
			continue
		}
		count := int(datBuf[off])
		pos := int(off) + 1
		if count == 0xFF {
			if pos+4 > len(datBuf) {
				continue
			}
			count = int(binary.BigEndian.Uint32(datBuf[pos:]))
			pos += 4
		}

		// Find block >= q.blockNum in the sorted list.
		foundBlock := false
		for j := 0; j < count; j++ {
			v, n := binary.Uvarint(datBuf[pos:])
			if n <= 0 {
				break
			}
			pos += n
			if v >= q.blockNum {
				foundBlock = true
				break
			}
		}
		if foundBlock {
			found2++
		}
	}
	rsTime := time.Since(t1)
	t.Logf("  RecSplit: %d queries, %d found, total=%v, avg=%v/query",
		len(samples), found2, rsTime, rsTime/time.Duration(len(samples)))

	// ===== Comparison =====
	t.Logf("\n=== Comparison ===")
	t.Logf("  MDBX:     avg=%v/query  found=%d/%d", mdbxTime/time.Duration(len(samples)), found, len(samples))
	t.Logf("  RecSplit:  avg=%v/query  found=%d/%d", rsTime/time.Duration(len(samples)), found2, len(samples))
	speedup := float64(mdbxTime) / float64(rsTime)
	t.Logf("  Speedup:  %.1fx", speedup)

	perKeyMDBX := float64(200) // ~200 bytes per key in MDBX
	perKeyRS := float64(mustFileSize(idxPath)+int64(len(datBuf))) / float64(len(histEntries))
	t.Logf("  Space: MDBX ~%.0f B/key, RecSplit %.1f B/key (%.0fx compression)",
		perKeyMDBX, perKeyRS, perKeyMDBX/perKeyRS)
}

func mustFileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}
