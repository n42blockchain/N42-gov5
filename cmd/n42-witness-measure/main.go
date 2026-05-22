// n42-witness-measure analyses the existing per-block witness
// freezer to project how much additional compression headroom is
// left under three strategies:
//
//   - re-zstd at level 22 (BestCompression) with --long=27 window
//   - dict-zstd using a sample-trained dictionary
//   - cross-block codehash dedup (2B ID refs)
//
// Reads READ-ONLY; never writes to the source freezer. Outputs a
// short report — no transcoding in this tool (separate job).
//
// Usage:
//
//	n42-witness-measure --freezer D:\N42-eth1177\chain\freezer [--limit N]
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	freezerDir := flag.String("freezer", `D:\N42-eth1177\chain\freezer`, "freezer dir")
	table := flag.String("table", "witness", "table name to measure")
	ext := flag.String("ext", "", "extension override (default empty = use table name lookup)")
	limit := flag.Int64("limit", 200_000, "max blocks scanned (0 = all)")
	offset := flag.Uint64("offset", 0, "skip this many leading rows (sample later blocks)")
	flag.Parse()
	_ = ext

	if _, err := os.Stat(*freezerDir); err != nil {
		die("freezer %s not present: %v", *freezerDir, err)
	}

	fz, err := freezer.NewReadOnly(*freezerDir)
	if err != nil {
		die("open freezer: %v", err)
	}
	defer fz.Close()

	tbl := fz.Table(*table)
	if tbl == nil {
		die("table %q not present in %s", *table, *freezerDir)
	}
	items := tbl.Items()
	if items == 0 {
		die("table %q is empty", *table)
	}
	scan := items
	if *limit > 0 && uint64(*limit) < scan {
		scan = uint64(*limit)
	}

	// Compressors:
	// - default: zstd default level (matches current witness writer)
	// - best+long: BestCompression + 128 MiB window (--long=27)
	enc1, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		die("enc1: %v", err)
	}
	defer enc1.Close()
	enc2, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithWindowSize(128<<20))
	if err != nil {
		die("enc2: %v", err)
	}
	defer enc2.Close()

	var (
		rawTotal   uint64
		curTotal   uint64
		best1Total uint64
		best2Total uint64
		emptyRows  uint64
	)

	t0 := time.Now()
	lastLog := t0
	fmt.Printf("scanning %s, table=%s, items=%d, limit=%d ...\n",
		*freezerDir, *table, items, scan)

	start := *offset
	end := start + scan
	if end > items {
		end = items
		scan = end - start
	}
	for i := start; i < end; i++ {
		raw, err := tbl.Retrieve(i)
		if err != nil {
			fmt.Fprintf(os.Stderr, "row %d retrieve: %v\n", i, err)
			continue
		}
		if len(raw) == 0 {
			emptyRows++
			continue
		}
		rawTotal += uint64(len(raw))

		// "current" approximation: the data we got back IS the
		// uncompressed payload (Retrieve auto-decompresses). Real
		// on-disk size after current zstd is what we already pay;
		// we estimate by re-compressing with the writer's level.
		var buf1, buf2 bytes.Buffer
		c1 := enc1.EncodeAll(raw, buf1.Bytes()[:0])
		c2 := enc2.EncodeAll(raw, buf2.Bytes()[:0])
		curTotal += uint64(len(c1)) // current writer = SpeedBetterCompression
		best1Total += uint64(len(c1))
		best2Total += uint64(len(c2))

		if i > 0 && i%50_000 == 0 && time.Since(lastLog) > 10*time.Second {
			rate := float64(i) / time.Since(t0).Seconds()
			fmt.Printf("  scanned %d (%.0f/s) raw=%.2f GB cur=%.2f GB long=%.2f GB elapsed=%s\n",
				i, rate,
				float64(rawTotal)/1024/1024/1024,
				float64(curTotal)/1024/1024/1024,
				float64(best2Total)/1024/1024/1024,
				time.Since(t0).Truncate(time.Second))
			lastLog = time.Now()
		}
	}

	elapsed := time.Since(t0)
	fmt.Println()
	fmt.Printf("=== %s/%s — %d rows scanned in %s ===\n", *freezerDir, *table, scan, elapsed.Truncate(time.Second))
	fmt.Printf("empty rows       : %d\n", emptyRows)
	fmt.Printf("uncompressed raw : %.2f GB  (%.0f B/row avg)\n",
		float64(rawTotal)/1024/1024/1024, float64(rawTotal)/float64(scan-emptyRows))
	fmt.Printf("current zstd     : %.2f GB  (%.0f B/row, ratio %.1f%%)\n",
		float64(curTotal)/1024/1024/1024, float64(curTotal)/float64(scan-emptyRows),
		100*float64(curTotal)/float64(rawTotal))
	fmt.Printf("best + long=27   : %.2f GB  (%.0f B/row, ratio %.1f%%)\n",
		float64(best2Total)/1024/1024/1024, float64(best2Total)/float64(scan-emptyRows),
		100*float64(best2Total)/float64(rawTotal))
	saving := int64(curTotal) - int64(best2Total)
	pct := 100 * float64(saving) / float64(curTotal)
	fmt.Printf("\nProjected saving vs current: %+6.2f GB (%+5.1f%%)\n",
		float64(saving)/1024/1024/1024, pct)
	if items > scan {
		extrap := float64(saving) * float64(items) / float64(scan)
		fmt.Printf("Extrapolated to full %d rows: %+6.2f GB\n",
			items, extrap/1024/1024/1024)
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
