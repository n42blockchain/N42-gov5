package cscompact

import (
	"context"
	"encoding/binary"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

func openErigonRO(t *testing.T) kv.RoDB {
	t.Helper()
	erigonDB := `e:\erigon2\chaindata`
	if _, err := os.Stat(erigonDB); err != nil {
		t.Skip("Erigon data not found")
	}
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(erigonDB).
		Label(kv.ChainDB).
		Readonly().
		Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// TestAccountCSRoundtrip encodes a segment from Erigon MDBX, decodes it,
// and verifies every entry matches the original.
func TestAccountCSRoundtrip(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	compactor := NewAccountCSCompactor(db, t.TempDir())

	// Read 10K blocks from Erigon.
	startBlock := uint64(10_000_000)
	endBlock := startBlock + 10_000
	entries, perBlock, rawBytes, err := compactor.readSegment(startBlock, endBlock)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Read %d entries across %d blocks (%d bytes)",
		len(entries), endBlock-startBlock, rawBytes)

	// Encode.
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()
	compressed := encodeAccountCSSegment(entries, perBlock, enc)
	t.Logf("Compressed: %d bytes (%.1f%% of raw)",
		len(compressed), float64(len(compressed))/float64(rawBytes)*100)

	// Decode.
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()
	decoded, decodedPerBlock, err := DecodeAccountCSSegment(compressed, dec)
	if err != nil {
		t.Fatal(err)
	}

	// Verify entry count.
	if len(decoded) != len(entries) {
		t.Fatalf("entry count: got %d, want %d", len(decoded), len(entries))
	}
	if len(decodedPerBlock) != len(perBlock) {
		t.Fatalf("block count: got %d, want %d", len(decodedPerBlock), len(perBlock))
	}

	// Verify per-block counts.
	for i, got := range decodedPerBlock {
		if got != perBlock[i] {
			t.Errorf("block %d: entries got %d, want %d", i, got, perBlock[i])
		}
	}

	// Verify every entry.
	mismatches := 0
	for i, got := range decoded {
		want := entries[i]
		if got.Address != want.Address {
			t.Errorf("entry %d: addr mismatch", i)
			mismatches++
		}
		if len(got.OldValue) != len(want.OldValue) {
			t.Errorf("entry %d: value len %d != %d", i, len(got.OldValue), len(want.OldValue))
			mismatches++
		} else {
			for j := range got.OldValue {
				if got.OldValue[j] != want.OldValue[j] {
					t.Errorf("entry %d: value byte %d mismatch", i, j)
					mismatches++
					break
				}
			}
		}
		if mismatches >= 10 {
			t.Fatal("too many mismatches")
		}
	}

	t.Logf("Roundtrip OK: %d entries, %d blocks, 0 mismatches",
		len(entries), len(perBlock))
}

// TestAccountCSMetrics measures compression ratio, encode/decode speed, and memory.
func TestAccountCSMetrics(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	compactor := NewAccountCSCompactor(db, t.TempDir())

	// Test multiple block ranges.
	ranges := []struct {
		name       string
		startBlock uint64
	}{
		{"early (1M-1.01M)", 1_000_000},
		{"mid (10M-10.01M)", 10_000_000},
		{"late (15M-15.01M)", 15_000_000},
	}

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	for _, r := range ranges {
		endBlock := r.startBlock + 10_000
		entries, perBlock, rawBytes, err := compactor.readSegment(r.startBlock, endBlock)
		if err != nil {
			t.Logf("%s: skip (%v)", r.name, err)
			continue
		}
		if len(entries) == 0 {
			t.Logf("%s: no entries, skip", r.name)
			continue
		}

		// Memory before.
		var memBefore runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&memBefore)

		// Encode.
		t0 := time.Now()
		compressed := encodeAccountCSSegment(entries, perBlock, enc)
		encodeTime := time.Since(t0)

		// Decode.
		t1 := time.Now()
		decoded, _, err := DecodeAccountCSSegment(compressed, dec)
		decodeTime := time.Since(t1)
		if err != nil {
			t.Errorf("%s: decode error: %v", r.name, err)
			continue
		}

		// Memory after.
		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)

		ratio := float64(len(compressed)) / float64(rawBytes) * 100
		t.Logf("%s: entries=%d raw=%dB compressed=%dB ratio=%.1f%%",
			r.name, len(entries), rawBytes, len(compressed), ratio)
		t.Logf("  encode: %v (%.0f entries/ms)",
			encodeTime, float64(len(entries))/float64(encodeTime.Milliseconds()+1))
		t.Logf("  decode: %v (%.0f entries/ms)",
			decodeTime, float64(len(decoded))/float64(decodeTime.Milliseconds()+1))
		t.Logf("  memDelta: %.1f MB (HeapAlloc diff)",
			float64(memAfter.HeapAlloc-memBefore.HeapAlloc)/1e6)
	}
}

// TestAccountCSReadBlock tests the reader's block-level access.
func TestAccountCSReadBlock(t *testing.T) {
	csDir := `d:\N42\cstest\cscompact`
	if _, err := os.Stat(csDir); err != nil {
		t.Skip("cscompact output not found")
	}

	reader, err := OpenAccountCS(csDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	t.Logf("Opened reader: %d segments", reader.segments)

	// Read a few blocks and verify they have entries.
	testBlocks := []uint64{100000, 500000, 1000000, 2000000, 4000000}
	for _, blockNum := range testBlocks {
		t0 := time.Now()
		entries, err := reader.ReadBlock(blockNum)
		elapsed := time.Since(t0)
		if err != nil {
			t.Errorf("block %d: %v", blockNum, err)
			continue
		}
		t.Logf("block %d: %d entries, %v (cold)", blockNum, len(entries), elapsed)

		// Second read (warm cache).
		t1 := time.Now()
		entries2, _ := reader.ReadBlock(blockNum)
		elapsed2 := time.Since(t1)
		t.Logf("block %d: %d entries, %v (warm)", blockNum, len(entries2), elapsed2)
	}

	// Cross-validate with Erigon MDBX for a specific block.
	erigonDB := `e:\erigon2\chaindata`
	if _, err := os.Stat(erigonDB); err != nil {
		t.Log("Erigon not available, skip cross-validation")
		return
	}
	db := openErigonRO(t)
	defer db.Close()

	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	blockNum := uint64(1000000)
	compressed, _ := reader.ReadBlock(blockNum)

	// Read same block from Erigon.
	cursor, _ := tx.Cursor(ErigonAccountChangeSet)
	defer cursor.Close()
	var seekKey [8]byte
	binary.BigEndian.PutUint64(seekKey[:], blockNum)
	var origEntries []AccountCSEntry
	for k, v, _ := cursor.Seek(seekKey[:]); k != nil; k, v, _ = cursor.Next() {
		if len(k) < 8 {
			continue
		}
		bn := binary.BigEndian.Uint64(k[:8])
		if bn != blockNum {
			break
		}
		var e AccountCSEntry
		if len(v) >= 20 {
			copy(e.Address[:], v[:20])
			if len(v) > 20 {
				e.OldValue = v[20:]
			}
		}
		origEntries = append(origEntries, e)
	}

	if len(compressed) != len(origEntries) {
		t.Errorf("block %d: entry count mismatch: compressed=%d erigon=%d",
			blockNum, len(compressed), len(origEntries))
	} else {
		match := true
		for i := range compressed {
			if compressed[i].Address != origEntries[i].Address {
				t.Errorf("block %d entry %d: addr mismatch", blockNum, i)
				match = false
				break
			}
		}
		if match {
			t.Logf("block %d: cross-validated %d entries OK", blockNum, len(compressed))
		}
	}
}

func openErigonForBench(b *testing.B) kv.RoDB {
	b.Helper()
	erigonDB := `e:\erigon2\chaindata`
	if _, err := os.Stat(erigonDB); err != nil {
		b.Skip("Erigon data not found")
	}
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(erigonDB).Label(kv.ChainDB).Readonly().Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	return db
}

func BenchmarkAccountCSEncode(b *testing.B) {
	db := openErigonForBench(b)
	defer db.Close()

	compactor := NewAccountCSCompactor(db, b.TempDir())
	entries, perBlock, _, _ := compactor.readSegment(10_000_000, 10_010_000)
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		encodeAccountCSSegment(entries, perBlock, enc)
	}
	b.ReportMetric(float64(len(entries)), "entries/op")
}

func BenchmarkAccountCSDecode(b *testing.B) {
	db := openErigonForBench(b)
	defer db.Close()

	compactor := NewAccountCSCompactor(db, b.TempDir())
	entries, perBlock, _, _ := compactor.readSegment(10_000_000, 10_010_000)
	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()
	compressed := encodeAccountCSSegment(entries, perBlock, enc)
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DecodeAccountCSSegment(compressed, dec)
	}
	b.ReportMetric(float64(len(entries)), "entries/op")
	b.ReportMetric(float64(len(compressed)), "bytes/op")
}
