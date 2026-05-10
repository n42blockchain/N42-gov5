// reth-cs-timeseries reports per-day activity in reth's AccountChangeSets and
// StorageChangeSets over a recent block window.
//
// Reth layout:
//
//	AccountChangeSets  DUPSORT
//	  Key:   block(8B BE)
//	  Value: addr(20B) || compact-encoded old account
//	  Each duplicate value = one account modification at that block.
//
//	StorageChangeSets  DUPSORT
//	  Key:   block(8B BE) || addr(20B)
//	  Value: slot(32B) || compact-encoded old value
//	  Each duplicate value = one storage modification by that addr at that block.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	tblAcct = "AccountChangeSets"
	tblStor = "StorageChangeSets"
)

func tableCfg(d kv.TableCfg) kv.TableCfg {
	d[tblAcct] = kv.TableCfgItem{Flags: kv.DupSort}
	d[tblStor] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	headBlock := flag.Uint64("head", 24766147, "head block (inclusive)")
	windowBlocks := flag.Uint64("window", 216000, "window size in blocks (~30 days @ 12s)")
	bucketBlocks := flag.Uint64("bucket", 7200, "bucket size in blocks (~1 day @ 12s)")
	flag.Parse()

	startBlock := *headBlock - *windowBlocks + 1
	endBlock := *headBlock
	numBuckets := int((*windowBlocks + *bucketBlocks - 1) / *bucketBlocks)

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(tableCfg).
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

	type bucket struct {
		blocksWithMods int
		acctMods       uint64
		storMods       uint64
	}
	buckets := make([]bucket, numBuckets)
	bucketIdx := func(blk uint64) int {
		if blk < startBlock {
			return -1
		}
		i := int((blk - startBlock) / *bucketBlocks)
		if i >= numBuckets {
			return -1
		}
		return i
	}

	// --- AccountChangeSets pass ---
	{
		fmt.Fprintf(os.Stderr, "scanning AccountChangeSets [%d, %d]...\n", startBlock, endBlock)
		cdup, err := tx.CursorDupSort(tblAcct)
		if err != nil {
			fmt.Fprintln(os.Stderr, "acct cursor:", err)
			os.Exit(1)
		}
		seekKey := make([]byte, 8)
		binary.BigEndian.PutUint64(seekKey, startBlock)
		startT := time.Now()
		var visited uint64
		k, _, err := cdup.Seek(seekKey)
		for ; k != nil; k, _, err = cdup.NextNoDup() {
			if err != nil {
				fmt.Fprintln(os.Stderr, "acct iter:", err)
				break
			}
			if len(k) < 8 {
				continue
			}
			blk := binary.BigEndian.Uint64(k[:8])
			if blk > endBlock {
				break
			}
			bi := bucketIdx(blk)
			if bi < 0 {
				continue
			}
			n, err := cdup.CountDuplicates()
			if err != nil {
				continue
			}
			buckets[bi].blocksWithMods++
			buckets[bi].acctMods += n
			visited++
		}
		cdup.Close()
		fmt.Fprintf(os.Stderr, "  acct: %d blocks visited in %v\n", visited, time.Since(startT))
	}

	// --- StorageChangeSets pass ---
	{
		fmt.Fprintf(os.Stderr, "scanning StorageChangeSets [%d, %d]...\n", startBlock, endBlock)
		cdup, err := tx.CursorDupSort(tblStor)
		if err != nil {
			fmt.Fprintln(os.Stderr, "stor cursor:", err)
			os.Exit(1)
		}
		seekKey := make([]byte, 8)
		binary.BigEndian.PutUint64(seekKey, startBlock)
		startT := time.Now()
		lastReport := startT
		var keysVisited uint64
		k, _, err := cdup.Seek(seekKey)
		for ; k != nil; k, _, err = cdup.NextNoDup() {
			if err != nil {
				fmt.Fprintln(os.Stderr, "stor iter:", err)
				break
			}
			if len(k) < 8 {
				continue
			}
			blk := binary.BigEndian.Uint64(k[:8])
			if blk > endBlock {
				break
			}
			bi := bucketIdx(blk)
			if bi < 0 {
				continue
			}
			n, err := cdup.CountDuplicates()
			if err != nil {
				continue
			}
			buckets[bi].storMods += n
			keysVisited++
			if time.Since(lastReport) > 10*time.Second {
				lastReport = time.Now()
				elapsed := lastReport.Sub(startT).Seconds()
				fmt.Fprintf(os.Stderr,
					"  stor: keys=%d blk=%d (rel=%d/%d) rate=%.0f keys/s\n",
					keysVisited, blk, blk-startBlock, *windowBlocks,
					float64(keysVisited)/elapsed)
			}
		}
		cdup.Close()
		fmt.Fprintf(os.Stderr, "  stor: %d (block,addr) keys in %v\n", keysVisited, time.Since(startT))
	}

	// Totals
	var totalAcct, totalStor uint64
	for _, b := range buckets {
		totalAcct += b.acctMods
		totalStor += b.storMods
	}
	if totalAcct == 0 {
		totalAcct = 1
	}
	if totalStor == 0 {
		totalStor = 1
	}

	fmt.Printf("\n=== Activity over last %d blocks (head=%d, bucket=%d blocks) ===\n",
		*windowBlocks, *headBlock, *bucketBlocks)
	fmt.Printf("total AccountChangeSets mods : %d\n", totalAcct)
	fmt.Printf("total StorageChangeSets mods : %d\n", totalStor)
	fmt.Printf("avg per bucket: acct=%.0f  stor=%.0f\n",
		float64(totalAcct)/float64(numBuckets),
		float64(totalStor)/float64(numBuckets))
	fmt.Println()
	fmt.Println("bucket  block_range                   acct_mods       stor_mods    acct%    stor%")
	for i, b := range buckets {
		from := startBlock + uint64(i)*(*bucketBlocks)
		to := from + *bucketBlocks - 1
		if to > endBlock {
			to = endBlock
		}
		acctPct := float64(b.acctMods) * 100 / float64(totalAcct)
		storPct := float64(b.storMods) * 100 / float64(totalStor)
		fmt.Printf("  %3d  %10d - %10d  %12d   %12d   %5.2f%%   %5.2f%%\n",
			i, from, to, b.acctMods, b.storMods, acctPct, storPct)
	}
}
