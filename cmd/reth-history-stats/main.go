// reth-history-stats scans reth's AccountsHistory or StoragesHistory tables
// and reports the distribution of modification counts per (addr) or (addr,slot).
//
// Reth layout:
//
//	AccountsHistory:  key = addr(20B) || highestBlock(8B BE)         value = IntegerList
//	StoragesHistory:  key = addr(20B) || slot(32B) || highestBlock   value = IntegerList
//
// IntegerList = Rust roaring crate's RoaringTreemap::serialize_into:
//
//	8B u64 LE: number of inner Roaring32 bitmaps
//	for each:
//	  4B u32 LE: high32 prefix
//	  Roaring32 portable serialization (cookie 0x303A or 0x303B)
//
// For chains with block heights < 2^32 there is always exactly 1 inner bitmap
// with high32 = 0, so the inner Roaring32 starts at offset 12 of the value.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func tableCfg(table string) func(kv.TableCfg) kv.TableCfg {
	return func(d kv.TableCfg) kv.TableCfg {
		d[table] = kv.TableCfgItem{}
		return d
	}
}

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	table := flag.String("table", "AccountsHistory", "AccountsHistory or StoragesHistory")
	prefixLen := flag.Int("prefix", 0, "0=auto (20 for accounts, 52 for storage)")
	limit := flag.Uint64("limit", 0, "0=full scan, else stop after this many MDBX rows")
	groupLimit := flag.Uint64("groups", 0, "0=full, else stop after this many groups")
	progressEvery := flag.Duration("progress", 5*time.Second, "progress log interval")
	flag.Parse()

	if *prefixLen == 0 {
		switch *table {
		case "AccountsHistory":
			*prefixLen = 20
		case "StoragesHistory":
			*prefixLen = 52
		default:
			fmt.Fprintln(os.Stderr, "specify -prefix for non-default tables")
			os.Exit(1)
		}
	}

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(tableCfg(*table)).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	c, err := tx.Cursor(*table)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()

	// Distribution buckets for modification counts.
	bucketEdges := []uint64{
		1, 2, 3, 5, 10, 20, 50, 100, 200, 500,
		1_000, 2_000, 5_000, 10_000, 50_000, 100_000, 1_000_000,
	}
	bucketCount := make([]uint64, len(bucketEdges)+1)
	totalCard := uint64(0)
	maxCard := uint64(0)
	var maxCardKey []byte

	var (
		curPrefix []byte
		curCard   uint64
		groups    uint64
		rows      uint64
		skipBad   uint64
		bm        = roaring.New()
		startT    = time.Now()
		lastT     = startT
	)

	finalize := func() {
		if curPrefix == nil {
			return
		}
		groups++
		totalCard += curCard
		if curCard > maxCard {
			maxCard = curCard
			maxCardKey = append(maxCardKey[:0], curPrefix...)
		}
		idx := len(bucketEdges)
		for i, e := range bucketEdges {
			if curCard <= e {
				idx = i
				break
			}
		}
		bucketCount[idx]++
		curCard = 0
	}

	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			break
		}
		rows++
		if len(k) < *prefixLen {
			skipBad++
			continue
		}
		if len(v) < 12 {
			skipBad++
			continue
		}
		prefix := k[:*prefixLen]

		if curPrefix == nil || !bytes.Equal(prefix, curPrefix) {
			finalize()
			curPrefix = append(curPrefix[:0], prefix...)
			if *groupLimit > 0 && groups >= *groupLimit {
				curPrefix = nil
				break
			}
		}

		// Parse RoaringTreemap header.
		numInner := binary.LittleEndian.Uint64(v[:8])
		if numInner != 1 {
			// Fallback: try roaring64 here? For now, count entries via a heuristic.
			// Should not happen for chains with blocks < 2^32.
			skipBad++
			continue
		}
		// v[8:12] = high32 (always 0 for our case), then Roaring32.
		bm.Clear()
		if _, err := bm.ReadFrom(bytes.NewReader(v[12:])); err != nil {
			skipBad++
			continue
		}
		curCard += bm.GetCardinality()

		if time.Since(lastT) >= *progressEvery {
			lastT = time.Now()
			elapsed := lastT.Sub(startT).Seconds()
			rate := float64(rows) / elapsed
			fmt.Fprintf(os.Stderr,
				"[%6.0fs] rows=%d groups=%d totalCard=%d skipBad=%d rate=%.0f rec/s\n",
				elapsed, rows, groups, totalCard, skipBad, rate)
		}
		if *limit > 0 && rows >= *limit {
			break
		}
	}
	finalize()

	elapsed := time.Since(startT)

	fmt.Println()
	fmt.Printf("=== %s ===\n", *table)
	fmt.Printf("rows scanned : %d\n", rows)
	fmt.Printf("groups       : %d\n", groups)
	fmt.Printf("total mods   : %d\n", totalCard)
	if groups > 0 {
		fmt.Printf("avg mods/grp : %.2f\n", float64(totalCard)/float64(groups))
	}
	fmt.Printf("max mods     : %d (key=%x)\n", maxCard, maxCardKey)
	fmt.Printf("skipped(bad) : %d\n", skipBad)
	fmt.Printf("elapsed      : %v\n", elapsed)
	fmt.Println()

	fmt.Println("Modification count distribution:")
	fmt.Println("  bucket               groups       % of total    cumulative")
	var prev uint64 = 0
	var cum uint64 = 0
	denom := groups
	if denom == 0 {
		denom = 1
	}
	for i, e := range bucketEdges {
		cum += bucketCount[i]
		pct := float64(bucketCount[i]) * 100 / float64(denom)
		cumPct := float64(cum) * 100 / float64(denom)
		var label string
		if i == 0 {
			label = "         = 1"
		} else {
			label = fmt.Sprintf("(%6d, %6d]", prev, e)
		}
		fmt.Printf("  %-20s %12d   %8.4f%%    %8.4f%%\n", label, bucketCount[i], pct, cumPct)
		prev = e
	}
	cum += bucketCount[len(bucketEdges)]
	pct := float64(bucketCount[len(bucketEdges)]) * 100 / float64(denom)
	cumPct := float64(cum) * 100 / float64(denom)
	fmt.Printf("  %-20s %12d   %8.4f%%    %8.4f%%\n",
		fmt.Sprintf(">%9d", bucketEdges[len(bucketEdges)-1]),
		bucketCount[len(bucketEdges)], pct, cumPct)
}
