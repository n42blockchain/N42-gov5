// n42-dense-measure reads an existing V1 dense table and reports
// what its V2 (G2 plain-key-referencing) size would be, without
// actually rewriting. Useful for measuring G2 savings on the real
// data before committing to a V2 bootstrap re-run.
//
// Usage:
//
//	n42-dense-measure --dir D:\n42-mpt-dense\accounts-mptcache --table AccountsDense
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
)

func main() {
	dir := flag.String("dir", `D:\n42-mpt-dense\accounts-mptcache`, "MDBX dir with V1 dense table")
	table := flag.String("table", "AccountsDense", "table to measure")
	flag.Parse()

	db, err := mdbxkv.NewMDBX(log.New()).
		Path(*dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(2 * datasize.TB).Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[*table] = kv.TableCfgItem{}
			return d
		}).Open(context.Background())
	if err != nil {
		fatal("open: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fatal("begin: %v", err)
	}
	defer tx.Rollback()
	c, err := tx.Cursor(*table)
	if err != nil {
		fatal("cursor: %v", err)
	}
	defer c.Close()

	var (
		rows               int64
		v1KeyBytes         int64
		v1ValueBytes       int64
		v2ValueBytes       int64
		v1HashedLeafSlots  int64
		v1BranchHashSlots  int64
		v1InlineSlots      int64
		t0                 = time.Now()
		v2EncBuf           []byte
		lastLog            = time.Now()
	)

	for k, v, err := c.First(); err == nil && k != nil; k, v, err = c.Next() {
		rows++
		v1KeyBytes += int64(len(k))
		v1ValueBytes += int64(len(v))

		stateMask, treeMask, slots, derr := trie.UnmarshalTrieNodeDense(v)
		if derr != nil {
			fatal("UnmarshalTrieNodeDense row %d: %v", rows, derr)
		}

		// Count slot types for V1 attribution.
		for digit := 0; digit < 16; digit++ {
			if stateMask&(1<<digit) == 0 {
				continue
			}
			slot := slots[digit]
			if len(slot) == 33 && slot[0] == 0xa0 {
				if treeMask&(1<<digit) != 0 {
					v1BranchHashSlots++
				} else {
					v1HashedLeafSlots++
				}
			} else {
				v1InlineSlots++
			}
		}

		// Rebuild slotData (stride 33 per set bit) as MarshalV2 expects.
		// V1 slots[] aliases v's bytes — they include the slot's prefix byte.
		// Reconstruct hashStack-style: each present slot's bytes copied
		// into a contiguous block, padded to 33 bytes per slot.
		const stride = 33
		slotData := make([]byte, 0, 16*stride)
		for digit := 0; digit < 16; digit++ {
			if stateMask&(1<<digit) == 0 {
				continue
			}
			slot := slots[digit]
			pad := make([]byte, stride)
			copy(pad, slot)
			slotData = append(slotData, pad...)
		}
		v2EncBuf = trie.MarshalTrieNodeDenseV2(stateMask, treeMask, slotData, v2EncBuf[:0])
		v2ValueBytes += int64(len(v2EncBuf))

		if time.Since(lastLog) > 5*time.Second {
			fmt.Fprintf(os.Stderr, "  scanned %d rows (V1=%.2f GB → V2 so far=%.2f GB, %.1f%% saving)\n",
				rows,
				float64(v1ValueBytes)/1e9,
				float64(v2ValueBytes)/1e9,
				100*(1-float64(v2ValueBytes)/float64(v1ValueBytes)))
			lastLog = time.Now()
		}
	}

	fmt.Printf("\n=== n42-dense-measure: %s/%s ===\n", *dir, *table)
	fmt.Printf("  rows                  %d\n", rows)
	fmt.Printf("  keys                  %.2f GB\n", float64(v1KeyBytes)/1e9)
	fmt.Printf("  V1 values             %.2f GB\n", float64(v1ValueBytes)/1e9)
	fmt.Printf("  V2 values (predicted) %.2f GB\n", float64(v2ValueBytes)/1e9)
	if v1ValueBytes > 0 {
		fmt.Printf("  saving                %.1f%%  (%.2f GB)\n",
			100*(1-float64(v2ValueBytes)/float64(v1ValueBytes)),
			float64(v1ValueBytes-v2ValueBytes)/1e9)
	}
	totalSlots := v1HashedLeafSlots + v1BranchHashSlots + v1InlineSlots
	if totalSlots > 0 {
		fmt.Printf("  slot mix              hashed-leaf %.1f%%  branch-hash %.1f%%  inline %.1f%%\n",
			100*float64(v1HashedLeafSlots)/float64(totalSlots),
			100*float64(v1BranchHashSlots)/float64(totalSlots),
			100*float64(v1InlineSlots)/float64(totalSlots))
	}
	fmt.Printf("  elapsed               %s\n", time.Since(t0).Truncate(time.Second))
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
