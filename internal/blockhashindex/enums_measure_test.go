package blockhashindex

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
)

// TestEnumsSizeCompare measures the on-disk RecSplit (.ri) size per key for the
// blockhash case (dense offsets 0..N-1, inserted in order) under Enums=false vs
// Enums=true, both with LessFalsePositives off. Run:
//
//	go test -run TestEnumsSizeCompare -v ./internal/blockhashindex/
func TestEnumsSizeCompare(t *testing.T) {
	const n = 200000
	keys := make([][32]byte, n)
	for i := 0; i < n; i++ {
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], uint64(i)*2654435761+1)
		keys[i] = sha256.Sum256(b[:])
	}

	// build returns the .ri size for N keys whose stored offset is capped at
	// maxOff (simulating a segment of maxOff+1 blocks → bytesPerRec width).
	build := func(enums bool, maxOff uint64) int64 {
		dir := t.TempDir()
		idxPath := filepath.Join(dir, "m.ri")
		rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
			KeyCount:           n,
			BucketSize:         2000,
			LeafSize:           8,
			Enums:              enums,
			LessFalsePositives: false,
			IndexFile:          idxPath,
			BaseDataID:         0,
			TmpDir:             dir,
		}, log2.New())
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < n; i++ {
			if err := rs.AddKey(keys[i][:], uint64(i)&maxOff); err != nil {
				t.Fatal(err)
			}
		}
		if err := rs.Build(context.Background()); err != nil {
			t.Fatal(err)
		}
		rs.Close()
		fi, err := os.Stat(idxPath)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}

	t.Logf("N=%d, LFP off — Enums:false vs true:", n)
	noEnum := build(false, 0xFFFFFFFF)
	withEnum := build(true, 0xFFFFFFFF)
	t.Logf("  Enums=false: %.2f bit/key   Enums=true: %.2f bit/key   (true smaller? %v)",
		float64(noEnum)*8/n, float64(withEnum)*8/n, withEnum < noEnum)

	t.Logf("Segment-size lever (Enums:false): maxOffset → bytesPerRec → bit/key:")
	for _, c := range []struct {
		seg    string
		maxOff uint64
	}{
		{"1M-block  (3B offset)", 0xFFFFF},  // relBlock < ~1M  → 20 bit → 3 B
		{"64K-block (2B offset)", 0xFFFF},   // relBlock < 65536 → 16 bit → 2 B
		{"256-block (1B offset)", 0xFF},     // relBlock < 256   → 8 bit  → 1 B
	} {
		sz := build(false, c.maxOff)
		t.Logf("  %s: %.2f bit/key = %.2f B/key  → 25M ≈ %.0f MB",
			c.seg, float64(sz)*8/n, float64(sz)/n, float64(sz)/n*25e6/1e6)
	}
}
