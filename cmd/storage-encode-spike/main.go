// storage-encode-spike: measure storage VALUE distribution + estimate
// compression ratios for the cold-tier RFC v2 encoding schemes.
//
// Data source: storcs.cdat freezer (storage changesets). Each block
// carries the NEW value of every storage write, already TRIM-ENCODED
// with explicit length prefix — perfect for value-distribution work
// and far faster than scanning the live PlainState MDBX.
//
// Output goes to stderr (unbuffered) so progress streams to the log
// file in real time even when redirected.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func emit(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, format, a...)
}

func main() {
	var (
		dir       = flag.String("dir", "", "freezer dir containing storcs.cidx (e.g. D:\\N42-eth1177\\chain\\freezer)")
		blocks    = flag.Int("blocks", 5000, "blocks to sample from storcs")
		startBlk  = flag.Int("start", 0, "starting block (0 = scan from earliest)")
		topK      = flag.Int("topk", 4096, "dictionary size for scheme D/E")
		pageSize  = flag.Int("page", 64, "values per zstd page for scheme E")
		progEvery = flag.Int("prog", 500, "progress every N blocks")
	)
	flag.Parse()
	if *dir == "" {
		emit("usage: storage-encode-spike --dir <freezer> [--blocks N] [--start B] [--topk K] [--page P]\n")
		os.Exit(1)
	}

	fr, err := freezer.NewReadOnly(*dir)
	must(err, "open freezer")
	defer fr.Close()
	tbl, err := fr.EnsureTableCompressed(freezer.TableStorageChanges, "c")
	must(err, "open storcs table")
	maxItems := tbl.Items()
	emit("storcs.cidx items: %d\n", maxItems)

	if uint64(*startBlk) >= maxItems {
		emit("--start %d >= maxItems %d\n", *startBlk, maxItems)
		os.Exit(1)
	}
	end := uint64(*startBlk + *blocks)
	if end > maxItems {
		end = maxItems
	}
	emit("Scanning storcs blocks [%d, %d)\n", *startBlk, end)

	sizeHist := make([]uint64, 33)
	freq := make(map[string]uint64, 1<<20)
	var totalChanges int

	// --- Pass 1 ---
	emit("\nPass 1: build size histogram and value frequency...\n")
	t0 := time.Now()
	rawCtx := context.Background()
	_ = rawCtx
	for blk := uint64(*startBlk); blk < end; blk++ {
		data, err := tbl.Retrieve(blk)
		if err != nil {
			emit("  retrieve block %d err: %v (skip)\n", blk, err)
			continue
		}
		changes, err := ethel.DecodeStorageChanges(data)
		if err != nil {
			emit("  decode block %d err: %v (skip)\n", blk, err)
			continue
		}
		for _, c := range changes {
			// NewValue is already trimmed (leading-zero stripped).
			v := c.NewValue
			sl := len(v)
			if sl > 32 {
				sl = 32
			}
			sizeHist[sl]++
			freq[string(v)]++
			totalChanges++
		}
		if (blk-uint64(*startBlk)+1)%uint64(*progEvery) == 0 {
			elapsed := time.Since(t0).Seconds()
			done := blk - uint64(*startBlk) + 1
			emit("  pass1 block %d (%d done, %d changes, %.0f blk/s, freq=%d)\n",
				blk, done, totalChanges, float64(done)/elapsed, len(freq))
		}
	}
	t1 := time.Since(t0)
	emit("\nPass 1 done: %d blocks, %d storage changes in %s\n",
		*blocks, totalChanges, t1.Truncate(time.Second))
	emit("Unique values: %d (%.2f%% of changes)\n",
		len(freq), float64(len(freq))*100/float64(totalChanges))

	// --- Size histogram ---
	emit("\n=== Value size distribution (newValue from storcs) ===\n")
	var cum uint64
	N := uint64(totalChanges)
	for i := 0; i <= 32; i++ {
		c := sizeHist[i]
		if c == 0 {
			continue
		}
		cum += c
		emit("  %2d B: %10d (%6.2f%%)   cumulative %6.2f%%\n",
			i, c, float64(c)*100/float64(N), float64(cum)*100/float64(N))
	}
	avgSize := 0.0
	for i := 0; i <= 32; i++ {
		avgSize += float64(i) * float64(sizeHist[i])
	}
	avgSize /= float64(N)
	emit("Mean trimmed size: %.2f B\n", avgSize)

	// --- Top-K dict ---
	type fe struct {
		val   string
		count uint64
	}
	all := make([]fe, 0, len(freq))
	for k, c := range freq {
		all = append(all, fe{k, c})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].count > all[j].count })
	if len(all) >= 10 {
		emit("\n=== Top 10 most frequent values ===\n")
		for i := 0; i < 10 && i < len(all); i++ {
			pct := float64(all[i].count) * 100 / float64(N)
			emit("  #%2d  count=%10d (%5.2f%%)  bytes=%-2d  hex=0x%x\n",
				i+1, all[i].count, pct, len(all[i].val), []byte(all[i].val))
		}
	}
	dict := make(map[string]uint16, *topK)
	limit := *topK
	if limit > len(all) {
		limit = len(all)
	}
	for i := 0; i < limit; i++ {
		dict[all[i].val] = uint16(i)
	}
	var dictBytes int
	var dictCovered uint64
	for i := 0; i < limit; i++ {
		dictBytes += 1 + len(all[i].val)
		dictCovered += all[i].count
	}
	emit("\nDictionary: top-%d values, %d B on-disk, covers %.2f%% of samples\n",
		limit, dictBytes, float64(dictCovered)*100/float64(N))

	// --- Pass 2: encode under each scheme ---
	emit("\nPass 2: encode all entries under each scheme...\n")
	var bytesA, bytesB, bytesC, bytesD uint64
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	must(err, "zstd writer")
	defer enc.Close()
	var (
		pageBuf     []byte
		pageCount   int
		zstdTotal   uint64
		zstdPageHdr uint64
	)
	flushPage := func() {
		if len(pageBuf) == 0 {
			return
		}
		comp := enc.EncodeAll(pageBuf, nil)
		zstdTotal += uint64(len(comp))
		zstdPageHdr += 4
		pageBuf = pageBuf[:0]
		pageCount = 0
	}

	t0 = time.Now()
	for blk := uint64(*startBlk); blk < end; blk++ {
		data, err := tbl.Retrieve(blk)
		if err != nil {
			continue
		}
		changes, err := ethel.DecodeStorageChanges(data)
		if err != nil {
			continue
		}
		for _, c := range changes {
			v := c.NewValue
			bytesA += 32
			bytesB += uint64(1 + len(v))
			cBytes := taggedSize(v)
			bytesC += uint64(cBytes)
			var entry []byte
			if id, ok := dict[string(v)]; ok {
				bytesD += 3
				entry = []byte{0x30, byte(id >> 8), byte(id & 0xff)}
			} else {
				bytesD += uint64(cBytes)
				entry = taggedBytes(v)
			}
			pageBuf = append(pageBuf, entry...)
			pageCount++
			if pageCount >= *pageSize {
				flushPage()
			}
		}
	}
	flushPage()
	bytesE := zstdTotal + zstdPageHdr

	// --- Report ---
	emit("\n=== Per-scheme bytes/value (N=%d trimmed changes) ===\n", N)
	report := func(name string, bytes uint64, comment string) {
		emit("  %-50s %7.3f B/value   total %s   %s\n",
			name, float64(bytes)/float64(N), humanBytes(bytes), comment)
	}
	report("A. raw 32B (current Erigon storage val)", bytesA, "(baseline)")
	report("B. trimmed [len:1B][data:N]", bytesB,
		fmt.Sprintf("%.2fx vs A", float64(bytesA)/float64(bytesB)))
	report("C. tagged 4-class", bytesC,
		fmt.Sprintf("%.2fx vs A", float64(bytesA)/float64(bytesC)))
	report(fmt.Sprintf("D. tagged + top-%d dict", limit), bytesD,
		fmt.Sprintf("%.2fx vs A, +%d B dict header", float64(bytesA)/float64(bytesD), dictBytes))
	report(fmt.Sprintf("E. D + zstd-page-%d", *pageSize), bytesE,
		fmt.Sprintf("%.2fx vs A, %.2fx vs D", float64(bytesA)/float64(bytesE), float64(bytesD)/float64(bytesE)))

	// Cold tier projection
	const indexBitsPerLeaf = 11
	const leafPathBytesAvg = 4
	bytesPerLeafFixed := float64(indexBitsPerLeaf)/8 + leafPathBytesAvg
	emit("\n=== Cold-tier projection for 1B storage leaves ===\n")
	emit("  Index (BF 9b + RecSplit 2b)     %.2f B/leaf\n", float64(indexBitsPerLeaf)/8)
	emit("  MPT leaf path encoding (avg)    %.2f B/leaf\n", float64(leafPathBytesAvg))
	emit("  Value (scheme E)                %.2f B/leaf\n", float64(bytesE)/float64(N))
	totalPerLeaf := bytesPerLeafFixed + float64(bytesE)/float64(N)
	emit("  TOTAL per leaf                  %.2f B/leaf\n", totalPerLeaf)
	emit("  1 billion leaves                ~%.1f GB\n", totalPerLeaf*1e9/1e9)
	rawBaseline := (bytesPerLeafFixed + 32) * 1e9 / 1e9
	emit("  Raw-32B baseline (same index)   ~%.1f GB  (savings %.2fx)\n",
		rawBaseline, rawBaseline/totalPerLeaf)
}

func taggedSize(v []byte) int {
	switch L := len(v); {
	case L == 0:
		return 1
	case L == 1:
		return 1
	case L <= 15:
		return 1 + L
	default:
		return 1 + L
	}
}

func taggedBytes(v []byte) []byte {
	switch L := len(v); {
	case L == 0:
		return []byte{0x00}
	case L == 1:
		return []byte{v[0]}
	case L <= 15:
		return append([]byte{byte(0x10 | L)}, v...)
	default:
		return append([]byte{byte(0x20 | (L - 16))}, v...)
	}
}

func humanBytes(b uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/GB)
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/MB)
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/KB)
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func must(err error, what string) {
	if err != nil {
		emit("FATAL: %s: %v\n", what, err)
		os.Exit(1)
	}
}
