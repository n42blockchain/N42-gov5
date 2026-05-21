// n42-dense-measure reads an existing V1 dense table and reports
// what its V2 (G2 plain-key-referencing) size would be. With --write
// it also TRANSCODES the V1 entries to a V2 table in the same env
// (e.g. AccountsDense → AccountsDenseV2), so no re-bootstrap from
// reth is needed.
//
// Usage:
//
//	# measure only (read-only)
//	n42-dense-measure --dir D:\n42-mpt-dense\accounts-mptcache --table AccountsDense
//
//	# transcode V1 → V2 in place (writes new table alongside V1)
//	n42-dense-measure --dir D:\n42-mpt-dense\accounts-mptcache `
//	                   --table AccountsDense --write --dst-table AccountsDenseV2
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
	table := flag.String("table", "AccountsDense", "source V1 table to read")
	write := flag.Bool("write", false, "transcode mode: ALSO write V2 entries to --dst-table")
	dstTable := flag.String("dst-table", "AccountsDenseV2", "destination V2 table (used with --write)")
	flag.Parse()

	// Open the env. RW when --write, RO otherwise. Single-writer
	// MDBX so this conflicts with any in-progress bootstrap on the
	// same dir — caller's responsibility.
	mdbxBuilder := mdbxkv.NewMDBX(log.New()).
		Path(*dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(2 * datasize.TB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[*table] = kv.TableCfgItem{}
			if *write {
				d[*dstTable] = kv.TableCfgItem{}
			}
			return d
		})
	if !*write {
		mdbxBuilder = mdbxBuilder.Readonly()
	}
	db, err := mdbxBuilder.Open(context.Background())
	if err != nil {
		fatal("open: %v", err)
	}
	defer db.Close()

	// Source read tx.
	rtx, err := db.BeginRo(context.Background())
	if err != nil {
		fatal("begin ro: %v", err)
	}
	defer rtx.Rollback()
	c, err := rtx.Cursor(*table)
	if err != nil {
		fatal("cursor: %v", err)
	}
	defer c.Close()

	// Destination write tx — only when --write.
	var (
		wtx     kv.RwTx
		dstCur  kv.RwCursor
	)
	if *write {
		wtx, err = db.BeginRw(context.Background())
		if err != nil {
			fatal("begin rw: %v", err)
		}
		// Truncate any prior content for idempotent re-run.
		if err := wtx.ClearBucket(*dstTable); err != nil {
			wtx.Rollback()
			fatal("clear %s: %v", *dstTable, err)
		}
		dstCur, err = wtx.RwCursor(*dstTable)
		if err != nil {
			wtx.Rollback()
			fatal("dst cursor: %v", err)
		}
	}

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

		if *write {
			// Source iteration is sorted by nibble path → V2 encoding
			// uses the same key → cursor.Append OK.
			keyCopy := make([]byte, len(k))
			copy(keyCopy, k)
			valCopy := make([]byte, len(v2EncBuf))
			copy(valCopy, v2EncBuf)
			if err := dstCur.Append(keyCopy, valCopy); err != nil {
				dstCur.Close()
				wtx.Rollback()
				fatal("dst Append row %d: %v", rows, err)
			}
		}

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

	if *write {
		dstCur.Close()
		if err := wtx.Commit(); err != nil {
			fatal("commit dst tx: %v", err)
		}
		fmt.Printf("  ✓ V2 written to %s/%s (%d rows)\n", *dir, *dstTable, rows)
	}
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
