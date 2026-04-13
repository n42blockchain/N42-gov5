// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// measure-leaves reads a sample block range from a leaves freezer table,
// decodes each per-block leaves journal, and classifies bytes into
// "value bytes" (that would be added to acctcs/storcs if leaves is deleted
// and replaced by an extended acctcs/storcs carrying new values inline)
// versus "framing overhead" (addresses/slot keys/length prefixes that are
// already present in acctcs/storcs and thus pure duplication).
//
// Output projects to a user-specified full range (default 10,074,880) and
// reports the predicted raw-byte savings from deleting leaves and extending
// acctcs/storcs in-place. Compressed-size savings are estimated by scaling
// with the leaves compression ratio (measured from the sample itself).
//
// Usage:
//
//	measure-leaves --leaves d:/n42-eth037/chain/freezer --start 0 --end 100000

package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	var (
		leavesDir  = flag.String("leaves", "", "Path to leaves freezer dir")
		startBlock = flag.Uint64("start", 0, "Start block (inclusive)")
		endBlock   = flag.Uint64("end", 100000, "End block (exclusive)")
		stride     = flag.Uint64("stride", 1, "Sample every N blocks")
		totalProj  = flag.Uint64("project", 10074880, "Total blocks to project savings to")
	)
	flag.Parse()

	if *leavesDir == "" {
		flag.Usage()
		os.Exit(2)
	}

	if err := run(*leavesDir, *startBlock, *endBlock, *stride, *totalProj); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(leavesDir string, start, end, stride, totalProj uint64) error {
	t0 := time.Now()

	tbl, err := freezer.NewFreezerTableCompressedReadOnly(leavesDir, "leaves", "c")
	if err != nil {
		return fmt.Errorf("open leaves: %w", err)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)
	tbl.SetCompressed(true)

	totalItems := tbl.Items()
	if end > totalItems {
		end = totalItems
	}
	if start >= end {
		return fmt.Errorf("invalid range [%d, %d)", start, end)
	}

	var (
		samplesRead       uint64
		leavesRawDecoded  int64 // sum of len(leaves blob) after zstd decompress (decoded size)
		accountEntries    int64
		accountValueBytes int64
		storageEntries    int64
		storageValueBytes int64
		wipeEntries       int64
	)

	for b := start; b < end; b += stride {
		data, err := tbl.Retrieve(b)
		if err != nil {
			return fmt.Errorf("retrieve block %d: %w", b, err)
		}
		if len(data) == 0 {
			continue
		}
		// len(data) here is the decoded (decompressed) per-block blob length,
		// because FreezerTable.Retrieve already decompresses via its zstd
		// decoder before returning.
		leavesRawDecoded += int64(len(data))
		samplesRead++

		accs, stos, wipes, err := ethel.DecodeLeavesJournal(data)
		if err != nil {
			return fmt.Errorf("decode block %d: %w", b, err)
		}
		for _, a := range accs {
			accountEntries++
			accountValueBytes += int64(len(a.Value))
		}
		for _, s := range stos {
			storageEntries++
			storageValueBytes += int64(len(s.Value))
		}
		wipeEntries += int64(len(wipes))
	}

	if samplesRead == 0 {
		return fmt.Errorf("no samples read in range [%d, %d)", start, end)
	}

	// --- Measure the equivalent acctcs / storcs / leaves on-disk sizes for
	// the same range. FreezerTable doesn't expose per-range compressed size
	// directly, so we approximate by inspecting the cdat files' byte
	// ranges covered by the sampled blocks' cidx offsets. For a rough
	// projection we instead use the overall table sizes (stat via os).
	leavesOnDisk, _ := tableOnDiskBytes(leavesDir, "leaves")
	acctcsOnDisk, _ := tableOnDiskBytes(leavesDir, "acctcs")
	storcsOnDisk, _ := tableOnDiskBytes(leavesDir, "storcs")

	// Compression ratio of leaves (compressed on disk / decoded raw).
	// Since we only measured decoded raw for `samplesRead` blocks, scale
	// by total blocks to compare with full on-disk size.
	sampledBlocks := samplesRead
	compressionRatio := 0.0
	if totalItems > 0 && leavesRawDecoded > 0 {
		projDecoded := float64(leavesRawDecoded) * float64(totalItems) / float64(sampledBlocks)
		if projDecoded > 0 {
			compressionRatio = float64(leavesOnDisk) / projDecoded
		}
	}

	// Projection to totalProj blocks.
	proj := func(n int64) int64 {
		return int64(float64(n) * float64(totalProj) / float64(sampledBlocks))
	}

	projAccountValue := proj(accountValueBytes)
	projStorageValue := proj(storageValueBytes)
	projLeavesRaw := proj(leavesRawDecoded)

	// Bytes that we'd add to acctcs when extending each entry with a
	// (newLen:1 + newVal) field.
	projAcctcsGrowthRaw := projAccountValue + int64(proj(accountEntries)) // +1 byte per entry for newLen prefix
	// Bytes that we'd add to storcs similarly.
	projStorcsGrowthRaw := projStorageValue + int64(proj(storageEntries))

	// Compressed projection — assume extended tables compress at roughly
	// the same ratio as leaves currently does (both are mixed addr/slot/v2
	// content passed through the same zstd pipeline).
	compressedOf := func(raw int64) int64 {
		if compressionRatio == 0 {
			return raw
		}
		return int64(float64(raw) * compressionRatio)
	}

	fmt.Printf("=== Sample: blocks [%d, %d) stride=%d  samples=%d  elapsed=%s\n",
		start, end, stride, samplesRead, time.Since(t0).Truncate(time.Millisecond))
	fmt.Println()
	fmt.Printf("Raw decoded leaves (sum of per-block blobs after zstd decompress):\n")
	fmt.Printf("  leaves raw decoded: %s\n", fmtSize(leavesRawDecoded))
	fmt.Printf("  accounts: %d entries, value bytes=%s (avg %.1f B/entry)\n",
		accountEntries, fmtSize(accountValueBytes), floatDiv(accountValueBytes, accountEntries))
	fmt.Printf("  storage:  %d entries, value bytes=%s (avg %.1f B/entry)\n",
		storageEntries, fmtSize(storageValueBytes), floatDiv(storageValueBytes, storageEntries))
	fmt.Printf("  wipes:    %d entries\n", wipeEntries)
	fmt.Println()

	// Framing = decoded total - value bytes - wipes(20 per addr)
	wipeBytes := int64(wipeEntries) * 20
	framingBytes := leavesRawDecoded - accountValueBytes - storageValueBytes - wipeBytes
	framingPct := float64(framingBytes) / float64(leavesRawDecoded) * 100
	fmt.Printf("Leaves composition (raw decoded):\n")
	fmt.Printf("  value bytes (all):      %s  (%.1f%%)\n",
		fmtSize(accountValueBytes+storageValueBytes),
		float64(accountValueBytes+storageValueBytes)/float64(leavesRawDecoded)*100)
	fmt.Printf("  framing (addr+key+len): %s  (%.1f%%)  ← duplicated in acctcs/storcs\n",
		fmtSize(framingBytes), framingPct)
	fmt.Printf("  wipe addresses:         %s\n", fmtSize(wipeBytes))
	fmt.Println()

	fmt.Printf("=== Current on-disk (compressed)\n")
	fmt.Printf("  leaves: %s\n", fmtSize(leavesOnDisk))
	fmt.Printf("  acctcs: %s\n", fmtSize(acctcsOnDisk))
	fmt.Printf("  storcs: %s\n", fmtSize(storcsOnDisk))
	fmt.Printf("  total:  %s\n", fmtSize(leavesOnDisk+acctcsOnDisk+storcsOnDisk))
	fmt.Printf("  leaves compression ratio (compressed / raw decoded): %.3f\n", compressionRatio)
	fmt.Println()

	fmt.Printf("=== Projection to %d blocks\n", totalProj)
	fmt.Printf("Scenario: delete leaves, extend acctcs/storcs entries with new-value field\n")
	fmt.Printf("\n")
	fmt.Printf("Raw (decoded) delta:\n")
	fmt.Printf("  acctcs +new_value bytes:  %s\n", fmtSize(projAcctcsGrowthRaw))
	fmt.Printf("  storcs +new_value bytes:  %s\n", fmtSize(projStorcsGrowthRaw))
	fmt.Printf("  leaves removed:          -%s\n", fmtSize(projLeavesRaw))
	netRaw := projAcctcsGrowthRaw + projStorcsGrowthRaw - projLeavesRaw
	fmt.Printf("  net raw delta:            %s\n", fmtSize(netRaw))
	fmt.Println()

	fmt.Printf("Compressed (estimated at leaves compression ratio %.3f):\n", compressionRatio)
	fmt.Printf("  acctcs +new_value:        %s\n", fmtSize(compressedOf(projAcctcsGrowthRaw)))
	fmt.Printf("  storcs +new_value:        %s\n", fmtSize(compressedOf(projStorcsGrowthRaw)))
	fmt.Printf("  leaves removed:          -%s\n", fmtSize(leavesOnDisk))
	netCompressed := compressedOf(projAcctcsGrowthRaw) + compressedOf(projStorcsGrowthRaw) - leavesOnDisk
	fmt.Printf("  net compressed delta:     %s  (%.1f%% of current total)\n",
		fmtSize(netCompressed),
		float64(netCompressed)/float64(leavesOnDisk+acctcsOnDisk+storcsOnDisk)*100)

	if netCompressed < 0 {
		saving := -netCompressed
		savingPct := float64(saving) / float64(leavesOnDisk+acctcsOnDisk+storcsOnDisk) * 100
		fmt.Printf("\n  >>> projected SAVINGS: %s (%.1f%%) <<<\n", fmtSize(saving), savingPct)
	} else {
		fmt.Printf("\n  >>> projected COST: +%s (no savings) <<<\n", fmtSize(netCompressed))
	}

	return nil
}

// tableOnDiskBytes sums all .cdat file sizes for a table in a freezer dir.
func tableOnDiskBytes(dir, table string) (int64, error) {
	var total int64
	f, err := os.Open(dir)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	entries, err := f.Readdirnames(-1)
	if err != nil {
		return 0, err
	}
	prefix := table + "."
	for _, name := range entries {
		if len(name) < len(prefix)+5 || name[:len(prefix)] != prefix {
			continue
		}
		// match leaves.NNNN.cdat
		if !hasSuffix(name, ".cdat") {
			continue
		}
		fi, err := os.Stat(dir + "/" + name)
		if err != nil {
			continue
		}
		total += fi.Size()
	}
	return total, nil
}

func hasSuffix(s, suf string) bool {
	return len(s) >= len(suf) && s[len(s)-len(suf):] == suf
}

func fmtSize(b int64) string {
	neg := b < 0
	if neg {
		b = -b
	}
	var out string
	switch {
	case b >= 1<<30:
		out = fmt.Sprintf("%.2f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		out = fmt.Sprintf("%.2f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		out = fmt.Sprintf("%.2f KB", float64(b)/(1<<10))
	default:
		out = fmt.Sprintf("%d B", b)
	}
	if neg {
		return "-" + out
	}
	return out
}

func floatDiv(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}
