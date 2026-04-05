package ethel

import (
	"fmt"
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// TestBatchSizeComparison reads existing journal data and compares
// compression ratio between batch-64 and batch-128.
func TestBatchSizeComparison(t *testing.T) {
	ancientPath := `d:\N42\verify_test\ancient`
	if _, err := os.Stat(ancientPath); err != nil {
		t.Skip("verify_test data not found")
	}

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	tbl := f.Table("leaves_journal")
	if tbl == nil {
		t.Fatal("leaves_journal not found")
	}
	tbl.ForceBatchSize(freezer.BatchSize)

	maxBlocks := tbl.Items()
	if maxBlocks > 100000 {
		maxBlocks = 100000 // sample first 100K
	}

	// Read all raw entries.
	entries := make([][]byte, maxBlocks)
	var totalRaw int64
	for i := uint64(0); i < maxBlocks; i++ {
		data, err := tbl.Retrieve(i)
		if err != nil {
			t.Fatalf("retrieve %d: %v", i, err)
		}
		entries[i] = data
		totalRaw += int64(len(data))
	}

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()

	for _, bs := range []int{32, 64, 128, 256} {
		var totalCompressed int64
		var batches int
		for start := 0; start < len(entries); start += bs {
			end := start + bs
			if end > len(entries) {
				end = len(entries)
			}
			batch := entries[start:end]
			encoded := freezer.EncodeBatch(batch, enc)
			totalCompressed += int64(len(encoded))
			batches++
		}

		ratio := float64(totalCompressed) / float64(totalRaw) * 100
		avgBatch := float64(totalCompressed) / float64(batches)
		t.Logf("batch=%3d: compressed=%8d raw=%8d ratio=%.1f%% avgBatch=%.0fB batches=%d",
			bs, totalCompressed, totalRaw, ratio, avgBatch, batches)
	}

	// Memory estimate: each batch is decompressed on read.
	// Larger batch = more data decompressed per random access.
	t.Log("")
	t.Log("Memory per random read:")
	for _, bs := range []int{32, 64, 128, 256} {
		avgEntrySize := float64(totalRaw) / float64(maxBlocks)
		memPerRead := float64(bs) * avgEntrySize
		t.Logf("  batch=%3d: ~%.0f KB decompressed per random access", bs, memPerRead/1024)
	}
}

// TestBatchSizeReceipts compares batch sizes for receipts table.
func TestBatchSizeReceipts(t *testing.T) {
	ancientPath := `d:\N42\verify_test\ancient`
	if _, err := os.Stat(ancientPath); err != nil {
		t.Skip("verify_test data not found")
	}

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	tbl := f.Table("receipts")
	if tbl == nil {
		t.Skip("receipts table not found")
	}
	tbl.ForceBatchSize(freezer.BatchSize)

	maxBlocks := tbl.Items()
	if maxBlocks > 100000 {
		maxBlocks = 100000
	}
	if maxBlocks == 0 {
		t.Skip("receipts table empty")
	}

	entries := make([][]byte, maxBlocks)
	var totalRaw int64
	for i := uint64(0); i < maxBlocks; i++ {
		data, _ := tbl.Retrieve(i)
		entries[i] = data
		totalRaw += int64(len(data))
	}

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()

	t.Logf("Receipts: %d entries, %d bytes raw", maxBlocks, totalRaw)
	for _, bs := range []int{32, 64, 128, 256} {
		var totalCompressed int64
		for start := 0; start < len(entries); start += bs {
			end := start + bs
			if end > len(entries) {
				end = len(entries)
			}
			encoded := freezer.EncodeBatch(entries[start:end], enc)
			totalCompressed += int64(len(encoded))
		}
		ratio := float64(totalCompressed) / float64(totalRaw) * 100
		fmt.Printf("  receipts batch=%3d: ratio=%.1f%%\n", bs, ratio)
		t.Logf("  batch=%3d: compressed=%d ratio=%.1f%%", bs, totalCompressed, ratio)
	}
}
