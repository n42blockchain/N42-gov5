package cscompact

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// TestSubSegmentSizing measures cold-read latency at different segment sizes
// to find the optimal tradeoff between compression ratio and random access speed.
//
// Current problem: 1M blocks/segment → cold read 200ms-13s (too slow for random access)
// Goal: find segment size where cold read < 50ms while maintaining good compression
func TestSubSegmentSizing(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	compactor := NewAccountCSCompactor(db, t.TempDir())

	// Read a dense block range for testing.
	start := uint64(10_000_000)
	allEntries, allPerBlock, rawBytes, err := compactor.readSegment(start, start+100_000)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Total: %d entries, %d raw bytes for 100K blocks", len(allEntries), rawBytes)

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	// Test different sub-segment sizes.
	for _, subSegBlocks := range []int{1000, 5000, 8192, 10000, 50000, 100000} {
		totalCompressed := 0
		segCount := 0
		var maxDecodeTime time.Duration
		var totalDecodeTime time.Duration

		blockOffset := 0
		for blockOffset < len(allPerBlock) {
			endBlock := blockOffset + subSegBlocks
			if endBlock > len(allPerBlock) {
				endBlock = len(allPerBlock)
			}

			// Slice entries for this sub-segment.
			entryStart := 0
			for i := 0; i < blockOffset; i++ {
				entryStart += int(allPerBlock[i])
			}
			entryEnd := entryStart
			subPerBlock := allPerBlock[blockOffset:endBlock]
			for _, c := range subPerBlock {
				entryEnd += int(c)
			}
			if entryEnd > len(allEntries) {
				entryEnd = len(allEntries)
			}
			subEntries := allEntries[entryStart:entryEnd]

			// Compress.
			compressed := encodeAccountCSSegment(subEntries, subPerBlock, enc)
			totalCompressed += len(compressed)

			// Decompress and measure time.
			t0 := time.Now()
			_, _, err := DecodeAccountCSSegment(compressed, dec)
			elapsed := time.Since(t0)
			if err != nil {
				t.Fatalf("decode error at sub-segment %d: %v", segCount, err)
			}

			if elapsed > maxDecodeTime {
				maxDecodeTime = elapsed
			}
			totalDecodeTime += elapsed
			segCount++

			blockOffset = endBlock
		}

		ratio := float64(totalCompressed) / float64(rawBytes) * 100
		avgDecode := totalDecodeTime / time.Duration(segCount)
		t.Logf("subSeg=%5dblk: segments=%3d  ratio=%5.1f%%  avgDecode=%8v  maxDecode=%8v  totalSize=%s",
			subSegBlocks, segCount, ratio, avgDecode, maxDecodeTime,
			fmtBytes(totalCompressed))
	}
}

// TestSubSegmentReadLatency measures actual ReadBlock latency with pre-built
// compressed data at different segment sizes.
func TestSubSegmentReadLatency(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	compactor := NewAccountCSCompactor(db, t.TempDir())

	start := uint64(10_000_000)
	entries, perBlock, _, err := compactor.readSegment(start, start+50_000)
	if err != nil {
		t.Fatal(err)
	}

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	// Build segments of different sizes and measure ReadBlock latency.
	for _, subSegBlocks := range []int{1000, 5000, 10000} {
		tmpDir := t.TempDir()

		// Build sub-segments as if they were separate files.
		var segments []struct {
			compressed []byte
			perBlock   []uint32
		}

		blockOffset := 0
		for blockOffset < len(perBlock) {
			endBlock := blockOffset + subSegBlocks
			if endBlock > len(perBlock) {
				endBlock = len(perBlock)
			}

			entryStart := 0
			for i := 0; i < blockOffset; i++ {
				entryStart += int(perBlock[i])
			}
			entryEnd := entryStart
			subPerBlock := perBlock[blockOffset:endBlock]
			for _, c := range subPerBlock {
				entryEnd += int(c)
			}
			if entryEnd > len(entries) {
				entryEnd = len(entries)
			}

			compressed := encodeAccountCSSegment(entries[entryStart:entryEnd], subPerBlock, enc)
			segments = append(segments, struct {
				compressed []byte
				perBlock   []uint32
			}{compressed, subPerBlock})

			blockOffset = endBlock
		}

		// Measure ReadBlock latency: pick random blocks.
		testBlocks := []int{0, 500, 1000, 5000, 10000, 25000, 49999}
		for _, relBlock := range testBlocks {
			if relBlock >= len(perBlock) {
				continue
			}
			segIdx := relBlock / subSegBlocks
			if segIdx >= len(segments) {
				continue
			}

			t0 := time.Now()
			decoded, _, _ := DecodeAccountCSSegment(segments[segIdx].compressed, dec)
			decodeTime := time.Since(t0)

			// Find entries for this specific block.
			localBlock := relBlock % subSegBlocks
			entryOffset := 0
			for i := 0; i < localBlock && i < len(segments[segIdx].perBlock); i++ {
				entryOffset += int(segments[segIdx].perBlock[i])
			}

			blockEntries := 0
			if localBlock < len(segments[segIdx].perBlock) {
				blockEntries = int(segments[segIdx].perBlock[localBlock])
			}

			_ = decoded
			_ = tmpDir
			t.Logf("  subSeg=%5d  block=%7d  decode=%8v  entries=%d",
				subSegBlocks, start+uint64(relBlock), decodeTime, blockEntries)
		}
	}
}

func fmtBytes(b int) string {
	if b >= 1_000_000_000 {
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	}
	if b >= 1_000_000 {
		return fmt.Sprintf("%.1f MB", float64(b)/1e6)
	}
	return fmt.Sprintf("%.0f KB", float64(b)/1e3)
}

// TestCompressionStrategiesComparison compares all strategies on same data.
func TestCompressionStrategiesComparison(t *testing.T) {
	if _, err := os.Stat(`e:\erigon2\chaindata`); err != nil {
		t.Skip("Erigon data not found")
	}

	db := openErigonRO(t)
	defer db.Close()

	compactor := NewAccountCSCompactor(db, t.TempDir())

	start := uint64(10_000_000)
	entries, perBlock, rawBytes, _ := compactor.readSegment(start, start+10_000)

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()
	dec, _ := zstd.NewReader(nil)
	defer dec.Close()

	// Strategy A: raw zstd (no columnar).
	rawBuf := make([]byte, 0, rawBytes)
	for _, e := range entries {
		rawBuf = append(rawBuf, e.Address[:]...)
		if e.OldValue != nil {
			rawBuf = append(rawBuf, e.OldValue...)
		}
	}
	rawZstd := enc.EncodeAll(rawBuf, nil)

	// Strategy B: columnar + zstd (current).
	colZstd := encodeAccountCSSegment(entries, perBlock, enc)

	// Strategy C: V3 seg (per-word, done separately).

	t.Logf("=== 10K blocks, %d entries, %d raw bytes ===", len(entries), rawBytes)
	t.Logf("A: raw zstd only:   %8d bytes  ratio=%5.1f%%  (%.1fx)",
		len(rawZstd), float64(len(rawZstd))/float64(rawBytes)*100,
		float64(rawBytes)/float64(len(rawZstd)))
	t.Logf("B: columnar+zstd:   %8d bytes  ratio=%5.1f%%  (%.1fx)",
		len(colZstd), float64(len(colZstd))/float64(rawBytes)*100,
		float64(rawBytes)/float64(len(colZstd)))
	t.Logf("C: V3 per-word:     (run TestV3vsColumnarComparison for this)")

	// Sub-segment comparison.
	t.Logf("")
	t.Logf("=== Sub-segment sizing (tradeoff: ratio vs latency) ===")
	for _, subBlocks := range []int{500, 1000, 2000, 5000, 8192, 10000} {
		totalComp := 0
		segCount := 0
		blockOffset := 0
		for blockOffset < len(perBlock) {
			endBlock := blockOffset + subBlocks
			if endBlock > len(perBlock) {
				endBlock = len(perBlock)
			}
			entryStart := 0
			for i := 0; i < blockOffset; i++ {
				entryStart += int(perBlock[i])
			}
			entryEnd := entryStart
			subPB := perBlock[blockOffset:endBlock]
			for _, c := range subPB {
				entryEnd += int(c)
			}
			if entryEnd > len(entries) {
				entryEnd = len(entries)
			}
			comp := encodeAccountCSSegment(entries[entryStart:entryEnd], subPB, enc)
			totalComp += len(comp)
			segCount++
			blockOffset = endBlock
		}
		ratio := float64(totalComp) / float64(rawBytes) * 100
		t.Logf("  subSeg=%5d: segs=%3d ratio=%5.1f%% size=%s",
			subBlocks, segCount, ratio, fmtBytes(totalComp))
	}
}
