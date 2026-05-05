// receipts-probe: time how long it takes to read N receipts from a
// compressed freezer table. Helps diagnose if the bloom-recompute
// path is the bottleneck in witness-replay's prewarm.
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
	dir := flag.String("dir", "", "freezer dir with receipts.cdat")
	start := flag.Uint64("start", 0, "start block")
	count := flag.Uint64("count", 1000, "blocks to read")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: receipts-probe --dir <freezer> [--start N] [--count M]")
		os.Exit(1)
	}

	tbl, err := freezer.NewFreezerTableCompressedReadOnly(*dir, freezer.TableReceipts, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer tbl.Close()
	tbl.ForceBatchSize(freezer.BatchSize)

	fmt.Printf("table items=%d, reading [%d, %d)\n", tbl.Items(), *start, *start+*count)

	t0 := time.Now()
	totalBytes := 0
	totalDecoded := 0
	for n := *start; n < *start+*count; n++ {
		data, err := tbl.Retrieve(n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "retrieve %d: %v\n", n, err)
			os.Exit(1)
		}
		totalBytes += len(data)
		if len(data) > 0 {
			r, err := ethel.DecodeReceiptsCompact(data)
			if err != nil {
				fmt.Fprintf(os.Stderr, "decode %d: %v\n", n, err)
				continue
			}
			totalDecoded += len(r)
		}
	}
	d := time.Since(t0)
	fmt.Printf("done: %d blocks in %s (%.0f blk/s), %d bytes raw, %d receipts decoded\n",
		*count, d.Truncate(time.Millisecond), float64(*count)/d.Seconds(), totalBytes, totalDecoded)
}
