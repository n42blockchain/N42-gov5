// n42-witness-size measures real per-block witness sizes from a witness freezer
// (e.g. d:\N42-eth1177\chain\freezer) to size the MPT-anchor cadence: the MPT
// proof amortized over K blocks should be small vs the per-block witness.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	dir := flag.String("dir", `D:/N42-eth1177/chain/freezer`, "freezer dir with witness.* segments")
	sample := flag.Int("sample", 2000, "number of most-recent blocks to sample (decompressed Retrieve)")
	flag.Parse()

	tbl, err := freezer.NewFreezerTableCompressedReadOnly(*dir, freezer.TableBlockWitness, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open witness table:", err)
		os.Exit(1)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)

	items := tbl.Items()
	if items == 0 {
		fmt.Fprintln(os.Stderr, "witness table empty")
		os.Exit(1)
	}

	// On-disk compressed total (sum of witness.*.cdat).
	var cdatTotal int64
	if entries, e := filepath.Glob(filepath.Join(*dir, "witness.*.cdat")); e == nil {
		for _, f := range entries {
			if st, se := os.Stat(f); se == nil {
				cdatTotal += st.Size()
			}
		}
	}

	// Sample the most-recent blocks: decompressed witness size per block.
	n := uint64(*sample)
	if n > items {
		n = items
	}
	lo := items - n
	sizes := make([]int, 0, n)
	var sum, nonEmpty int
	var maxSz int
	minSz := int(^uint(0) >> 1)
	for b := lo; b < items; b++ {
		w, err := tbl.Retrieve(b)
		if err != nil {
			continue
		}
		l := len(w)
		sizes = append(sizes, l)
		sum += l
		if l > 0 {
			nonEmpty++
		}
		if l > maxSz {
			maxSz = l
		}
		if l < minSz {
			minSz = l
		}
	}
	sort.Ints(sizes)
	median := 0
	if len(sizes) > 0 {
		median = sizes[len(sizes)/2]
	}
	avg := 0
	if len(sizes) > 0 {
		avg = sum / len(sizes)
	}

	fmt.Printf("=== witness sizes (%s) ===\n", *dir)
	fmt.Printf("total blocks      : %d\n", items)
	fmt.Printf("on-disk compressed: %.1f GB total -> %.0f B/block global avg\n",
		float64(cdatTotal)/1e9, float64(cdatTotal)/float64(items))
	fmt.Printf("recent sample     : %d blocks [%d..%d] (decompressed)\n", len(sizes), lo, items-1)
	fmt.Printf("  decompressed/blk: avg %d B  median %d B  min %d B  max %d B  (nonEmpty %d)\n",
		avg, median, minSz, maxSz, nonEmpty)

	// Cadence: MPT anchor (compact, real head ~1.27 MB) amortized over K blocks
	// should be small vs the per-block witness. Report K for several overheads.
	const mptCompact = 1270413.0
	fmt.Printf("MPT anchor compact: %.2f MB (real head block)\n", mptCompact/1e6)
	if avg > 0 {
		fmt.Printf("amortized MPT/blk at K: ")
		for _, k := range []int{100, 500, 1000, 2000, 5000} {
			perBlk := mptCompact / float64(k)
			fmt.Printf("K=%d %.0fB(%.0f%% of witness)  ", k, perBlk, 100*perBlk/float64(avg))
		}
		fmt.Println()
	}
}
