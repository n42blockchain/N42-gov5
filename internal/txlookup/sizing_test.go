package txlookup

import (
	"fmt"
	"os"
	"testing"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// TestTxDensityProfile scans the chain and reports tx count per block range,
// helping evaluate optimal segment sizes.
func TestTxDensityProfile(t *testing.T) {
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`
	if _, err := os.Stat(ancientPath); err != nil {
		t.Skip("Geth ancient not found")
	}

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	frozen := f.Frozen()
	if frozen > 20_000_000 {
		frozen = 20_000_000
	}

	// Sample every 100K blocks to estimate tx density.
	type rangeInfo struct {
		startBlock uint64
		txCount    uint64
	}

	step := uint64(500_000)
	var ranges []rangeInfo

	for start := uint64(0); start < frozen; start += step {
		end := start + step
		if end > frozen {
			end = frozen
		}
		txCount := uint64(0)
		// Sample: count txs in first 1000 blocks of each range.
		sampleEnd := start + 1000
		if sampleEnd > end {
			sampleEnd = end
		}
		for b := start; b < sampleEnd; b++ {
			bodyData, err := f.Ancient(freezer.TableBodies, b)
			if err != nil {
				continue
			}
			body, err := ethel.DecodeGethBody(bodyData)
			if err != nil {
				continue
			}
			txCount += uint64(len(body.Transactions))
		}
		// Extrapolate to full range.
		blocksInRange := end - start
		estimatedTx := txCount * blocksInRange / 1000
		ranges = append(ranges, rangeInfo{start, estimatedTx})
	}

	// Print tx density per 500K range.
	t.Log("=== TX Density per 500K blocks ===")
	totalTx := uint64(0)
	for _, r := range ranges {
		totalTx += r.txCount
		t.Logf("  blocks %7d-%7d: ~%10d txs", r.startBlock, r.startBlock+step, r.txCount)
	}
	t.Logf("  Total estimated: %d txs", totalTx)

	// Evaluate segment sizes for different granularities.
	t.Log("")
	t.Log("=== Segment Size Evaluation ===")
	for _, segSize := range []uint64{500_000, 1_000_000, 2_000_000} {
		t.Logf("\n--- %dK blocks per segment ---", segSize/1000)
		segCount := 0
		maxSegBytes := uint64(0)
		totalBytes := uint64(0)

		for segStart := uint64(0); segStart < frozen; segStart += segSize {
			// Sum tx count for this segment from ranges.
			txInSeg := uint64(0)
			for _, r := range ranges {
				if r.startBlock >= segStart && r.startBlock < segStart+segSize {
					txInSeg += r.txCount
				}
			}
			// V2: ~5.5 bytes/key (idx) + ~1 byte/block (EF dat)
			segBytes := txInSeg*6 + segSize*2
			if segBytes > maxSegBytes {
				maxSegBytes = segBytes
			}
			totalBytes += segBytes
			segCount++
		}

		t.Logf("  Segments: %d", segCount)
		t.Logf("  Files: %d (.idx + .dat)", segCount*2)
		t.Logf("  Max segment: %s", fmtSize(maxSegBytes))
		t.Logf("  Total: %s", fmtSize(totalBytes))
		t.Logf("  Avg segment: %s", fmtSize(totalBytes/uint64(segCount)))
	}

	// Evaluate 2GB-based sizing.
	t.Log("\n--- 2GB file size limit ---")
	const maxFileSize = 2_000_000_000
	segCount := 0
	segStart := uint64(0)
	segTx := uint64(0)
	for _, r := range ranges {
		segTx += r.txCount
		if segTx*10 >= maxFileSize {
			t.Logf("  Segment %d: blocks %d-%d, ~%d txs, ~%s",
				segCount, segStart, r.startBlock+step, segTx, fmtSize(segTx*10))
			segCount++
			segStart = r.startBlock + step
			segTx = 0
		}
	}
	if segTx > 0 {
		t.Logf("  Segment %d: blocks %d-end, ~%d txs, ~%s",
			segCount, segStart, segTx, fmtSize(segTx*10))
		segCount++
	}
	t.Logf("  Total segments: %d", segCount)
}

func fmtSize(b uint64) string {
	if b >= 1_000_000_000 {
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	}
	return fmt.Sprintf("%.0f MB", float64(b)/1e6)
}
