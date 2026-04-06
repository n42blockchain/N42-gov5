package cscompact

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/seg"
)

// TestV3AccountCSCompress compresses 10K blocks using Erigon V3 and reports ratio.
func TestV3AccountCSCompress(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tmpDir := t.TempDir()
	comp := NewErigonV3Compactor(db, tmpDir)

	start := uint64(10_000_000)
	end := start + 10_000

	if err := comp.CompressAccountCS(context.Background(), start, end); err != nil {
		t.Fatal(err)
	}

	segFile := filepath.Join(tmpDir, fmt.Sprintf("account_cs_v3_%d_%d.seg", start, end))
	fi, _ := os.Stat(segFile)

	// Get raw size for comparison.
	compactor := NewAccountCSCompactor(db, tmpDir)
	_, _, rawBytes, _ := compactor.readSegment(start, end)

	t.Logf("V3 AccountCS: raw=%d compressed=%d ratio=%.1f%% (%.1fx)",
		rawBytes, fi.Size(),
		float64(fi.Size())/float64(rawBytes)*100,
		float64(rawBytes)/float64(fi.Size()))
}

// TestV3AccountCSRoundtrip verifies compressed data matches original.
func TestV3AccountCSRoundtrip(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tmpDir := t.TempDir()
	comp := NewErigonV3Compactor(db, tmpDir)

	start := uint64(10_000_000)
	end := start + 10_000

	if err := comp.CompressAccountCS(context.Background(), start, end); err != nil {
		t.Fatal(err)
	}

	segFile := filepath.Join(tmpDir, fmt.Sprintf("account_cs_v3_%d_%d.seg", start, end))
	if err := VerifyAccountCS(context.Background(), db, segFile, start, end); err != nil {
		t.Fatal(err)
	}
	t.Log("V3 roundtrip OK")
}

// TestV3StorageCSCompress compresses StorageCS using Erigon V3.
func TestV3StorageCSCompress(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	tmpDir := t.TempDir()
	comp := NewErigonV3Compactor(db, tmpDir)

	start := uint64(10_000_000)
	end := start + 10_000

	if err := comp.CompressStorageCS(context.Background(), start, end); err != nil {
		t.Fatal(err)
	}

	segFile := filepath.Join(tmpDir, fmt.Sprintf("storage_cs_v3_%d_%d.seg", start, end))
	fi, _ := os.Stat(segFile)
	t.Logf("V3 StorageCS: %d bytes (%.1f MB)", fi.Size(), float64(fi.Size())/1e6)
}

// TestV3PagedZstdComparison compares Erigon V3's actual approach:
// Account History uses ValuesOnCompressedPage(64) + zstd (NOT per-word dict+Huffman).
// Storage History uses CompressNone (no compression at all).
// This simulates the real Erigon V3 behavior.
func TestV3PagedZstdComparison(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	ranges := []struct {
		name  string
		start uint64
	}{
		{"early (1M)", 1_000_000},
		{"mid (10M)", 10_000_000},
		{"late (15M)", 15_000_000},
	}

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	defer enc.Close()
	colEnc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer colEnc.Close()
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	for _, r := range ranges {
		end := r.start + 10_000
		tmpDir := t.TempDir()

		compactor := NewAccountCSCompactor(db, tmpDir)
		entries, perBlock, rawBytes, err := compactor.readSegment(r.start, end)
		if err != nil || len(entries) == 0 {
			t.Logf("%s: skip", r.name)
			continue
		}

		// --- Strategy 1: Erigon V3 actual = page-64 zstd ---
		// Group every 64 entries into a page, zstd each page.
		pageSize := 64
		totalPaged := 0
		for i := 0; i < len(entries); i += pageSize {
			end := i + pageSize
			if end > len(entries) {
				end = len(entries)
			}
			// Build page: concatenate raw entry bytes.
			var page []byte
			for _, e := range entries[i:end] {
				page = append(page, e.Address[:]...)
				if e.OldValue != nil {
					page = append(page, e.OldValue...)
				}
			}
			compressed := enc.EncodeAll(page, nil)
			totalPaged += len(compressed) + 4 // 4B length prefix per page
		}

		// --- Strategy 2: Our columnar + zstd ---
		colCompressed := encodeAccountCSSegment(entries, perBlock, colEnc)

		// --- Strategy 3: raw zstd (whole segment) ---
		var rawBuf []byte
		for _, e := range entries {
			rawBuf = append(rawBuf, e.Address[:]...)
			if e.OldValue != nil {
				rawBuf = append(rawBuf, e.OldValue...)
			}
		}
		rawZstd := colEnc.EncodeAll(rawBuf, nil)

		// --- Strategy 4: V3 per-word dict+Huffman (what we tested before) ---
		// This is NOT what Erigon V3 actually uses for changesets.
		v3Dir := fmt.Sprintf("%s/v3_%d", tmpDir, r.start)
		os.MkdirAll(v3Dir, 0755)
		v3Comp := NewErigonV3Compactor(db, v3Dir)
		v3Comp.CompressAccountCS(context.Background(), r.start, r.start+10_000)
		v3File := fmt.Sprintf("%s/account_cs_v3_%d_%d.seg", v3Dir, r.start, r.start+10_000)
		v3Fi, _ := os.Stat(v3File)
		v3Bytes := int64(0)
		if v3Fi != nil {
			v3Bytes = v3Fi.Size()
		}

		t.Logf("")
		t.Logf("=== %s (%d entries) ===", r.name, len(entries))
		t.Logf("  Raw MDBX:           %10d bytes (baseline)", rawBytes)
		t.Logf("  V3 page-64 zstd:    %10d bytes  ratio=%5.1f%%  ← Erigon V3 ACTUAL",
			totalPaged, float64(totalPaged)/float64(rawBytes)*100)
		t.Logf("  V3 per-word d+H:    %10d bytes  ratio=%5.1f%%  ← NOT what V3 uses",
			v3Bytes, float64(v3Bytes)/float64(rawBytes)*100)
		t.Logf("  Our columnar+zstd:  %10d bytes  ratio=%5.1f%%",
			len(colCompressed), float64(len(colCompressed))/float64(rawBytes)*100)
		t.Logf("  Our raw zstd:       %10d bytes  ratio=%5.1f%%",
			len(rawZstd), float64(len(rawZstd))/float64(rawBytes)*100)
	}
}

// TestV3vsColumnarComparison compares V3 per-word vs columnar (legacy test).
func TestV3vsColumnarComparison(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	ranges := []struct {
		name  string
		start uint64
	}{
		{"early (1M)", 1_000_000},
		{"mid (10M)", 10_000_000},
		{"late (15M)", 15_000_000},
	}

	for _, r := range ranges {
		end := r.start + 10_000
		tmpDir := t.TempDir()

		// Read raw data.
		compactor := NewAccountCSCompactor(db, tmpDir)
		entries, perBlock, rawBytes, err := compactor.readSegment(r.start, end)
		if err != nil || len(entries) == 0 {
			t.Logf("%s: skip", r.name)
			continue
		}

		// --- Columnar compression ---
		enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
		t0 := time.Now()
		colCompressed := encodeAccountCSSegment(entries, perBlock, enc)
		colTime := time.Since(t0)
		enc.Close()

		// Columnar decode speed.
		dec, _ := zstd.NewReader(nil)
		t1 := time.Now()
		colDecoded, _, _ := DecodeAccountCSSegment(colCompressed, dec)
		colDecTime := time.Since(t1)
		dec.Close()

		// --- V3 (Erigon) compression ---
		var memBefore runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&memBefore)

		v3Dir := filepath.Join(tmpDir, "v3")
		os.MkdirAll(v3Dir, 0755)
		v3Comp := NewErigonV3Compactor(db, v3Dir)

		t2 := time.Now()
		v3Comp.CompressAccountCS(context.Background(), r.start, end)
		v3EncTime := time.Since(t2)

		var memAfter runtime.MemStats
		runtime.ReadMemStats(&memAfter)

		v3File := filepath.Join(v3Dir, fmt.Sprintf("account_cs_v3_%d_%d.seg", r.start, end))
		v3Fi, _ := os.Stat(v3File)
		v3Bytes := v3Fi.Size()

		// V3 decode speed.
		t3 := time.Now()
		d, err := seg.NewDecompressor(v3File)
		if err != nil {
			t.Logf("%s: V3 decompressor error: %v", r.name, err)
			continue
		}
		g := d.MakeGetter()
		v3DecEntries := 0
		for g.HasNext() {
			g.Skip()
			v3DecEntries++
		}
		v3DecTime := time.Since(t3)
		d.Close()

		// --- Report ---
		v3Ratio := float64(v3Bytes) / float64(rawBytes) * 100
		colRatio := float64(len(colCompressed)) / float64(rawBytes) * 100

		t.Logf("")
		t.Logf("=== %s (%d entries) ===", r.name, len(entries))
		t.Logf("  Raw MDBX:      %10d bytes (%.1f MB)", rawBytes, float64(rawBytes)/1e6)
		t.Logf("  V3 (Erigon):   %10d bytes  ratio=%5.1f%%  (%4.1fx)  enc=%v  dec=%v (%d entries)",
			v3Bytes, v3Ratio, float64(rawBytes)/float64(v3Bytes), v3EncTime, v3DecTime, v3DecEntries)
		t.Logf("  Columnar+zstd: %10d bytes  ratio=%5.1f%%  (%4.1fx)  enc=%v  dec=%v (%d entries)",
			len(colCompressed), colRatio, float64(rawBytes)/float64(len(colCompressed)), colTime, colDecTime, len(colDecoded))
		t.Logf("  V3 mem delta:  %.1f MB", float64(memAfter.HeapAlloc-memBefore.HeapAlloc)/1e6)

		if v3Ratio < colRatio {
			t.Logf("  >> V3 wins by %.1f percentage points", colRatio-v3Ratio)
		} else {
			t.Logf("  >> Columnar wins by %.1f percentage points", v3Ratio-colRatio)
		}
	}
}
