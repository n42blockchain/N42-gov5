// reth-cs-cumulative reports cumulative unique active addresses (and unique
// (addr, slot) pairs) over the last N days, broken out as 1-day, 2-day, ...,
// N-day rolling windows ending at head.
//
// Reth layout:
//
//	AccountChangeSets  DUPSORT
//	  Key:   block(8B BE)
//	  Value: addr(20B) || compact-encoded old account
//
//	StorageChangeSets  DUPSORT
//	  Key:   block(8B BE) || addr(20B)
//	  Value: slot(32B)   || compact-encoded old value
//
// Algorithm:
//
//	The window is split into N day buckets, day_back = (endBlock - blk) / bucket.
//	day_back = 0 is the most recent day, day_back = N-1 is the oldest.
//	For each unique (addr) [or (addr, slot)] we record min(day_back) seen.
//	Because the cursor iterates blocks in increasing order, day_back decreases
//	monotonically, so simply overwriting map[key] = day_back yields the min.
//	Cumulative day d = #keys whose min_day_back < d.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"runtime"
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

func memMiB() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.Alloc) / (1024 * 1024)
}

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	headBlock := flag.Uint64("head", 24766147, "head block (inclusive)")
	days := flag.Int("days", 30, "number of days in window")
	bucketBlocks := flag.Uint64("bucket", 7200, "blocks per day (~12s/block)")
	skipAcct := flag.Bool("skip-acct", false, "skip AccountChangeSets pass")
	skipStor := flag.Bool("skip-stor", false, "skip StorageChangeSets pass")
	progressEvery := flag.Duration("progress", 15*time.Second, "progress log interval")
	flag.Parse()

	windowBlocks := uint64(*days) * (*bucketBlocks)
	startBlock := *headBlock - windowBlocks + 1
	endBlock := *headBlock

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

	// dayBackOf returns 0..days-1.
	dayBackOf := func(blk uint64) uint8 {
		d := (endBlock - blk) / *bucketBlocks
		if d >= uint64(*days) {
			return uint8(*days - 1)
		}
		return uint8(d)
	}

	acctMin := make(map[string]uint8) // addr (20B) -> min day_back
	storMin := make(map[string]uint8) // addr+slot (52B) -> min day_back

	// --- AccountChangeSets pass ---
	if !*skipAcct {
		fmt.Fprintf(os.Stderr, "scanning AccountChangeSets [%d, %d]...\n", startBlock, endBlock)
		cdup, err := tx.CursorDupSort(tblAcct)
		if err != nil {
			fmt.Fprintln(os.Stderr, "acct cursor:", err)
			os.Exit(1)
		}
		seekKey := make([]byte, 8)
		binary.BigEndian.PutUint64(seekKey, startBlock)
		startT := time.Now()
		lastReport := startT
		var rows uint64
		k, v, err := cdup.Seek(seekKey)
		for ; k != nil; k, v, err = cdup.Next() {
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
			if len(v) < 20 {
				continue
			}
			db := dayBackOf(blk)
			addr := string(v[:20])
			if cur, ok := acctMin[addr]; !ok || db < cur {
				acctMin[addr] = db
			}
			rows++
			if time.Since(lastReport) >= *progressEvery {
				lastReport = time.Now()
				elapsed := lastReport.Sub(startT).Seconds()
				fmt.Fprintf(os.Stderr,
					"  acct: rows=%d uniq=%d blk=%d (rel=%d/%d) rate=%.0f rows/s mem=%.0fMB\n",
					rows, len(acctMin), blk, blk-startBlock, windowBlocks,
					float64(rows)/elapsed, memMiB())
			}
		}
		cdup.Close()
		fmt.Fprintf(os.Stderr, "  acct done: rows=%d uniq=%d in %v\n",
			rows, len(acctMin), time.Since(startT))
	}

	// --- StorageChangeSets pass ---
	if !*skipStor {
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
		var rows uint64
		buf := make([]byte, 52)
		k, v, err := cdup.Seek(seekKey)
		for ; k != nil; k, v, err = cdup.Next() {
			if err != nil {
				fmt.Fprintln(os.Stderr, "stor iter:", err)
				break
			}
			if len(k) < 28 {
				continue
			}
			blk := binary.BigEndian.Uint64(k[:8])
			if blk > endBlock {
				break
			}
			if len(v) < 32 {
				continue
			}
			db := dayBackOf(blk)
			copy(buf[:20], k[8:28])
			copy(buf[20:], v[:32])
			key := string(buf)
			if cur, ok := storMin[key]; !ok || db < cur {
				storMin[key] = db
			}
			rows++
			if time.Since(lastReport) >= *progressEvery {
				lastReport = time.Now()
				elapsed := lastReport.Sub(startT).Seconds()
				fmt.Fprintf(os.Stderr,
					"  stor: rows=%d uniq=%d blk=%d (rel=%d/%d) rate=%.0f rows/s mem=%.0fMB\n",
					rows, len(storMin), blk, blk-startBlock, windowBlocks,
					float64(rows)/elapsed, memMiB())
			}
		}
		cdup.Close()
		fmt.Fprintf(os.Stderr, "  stor done: rows=%d uniq=%d in %v\n",
			rows, len(storMin), time.Since(startT))
	}

	// Build histogram of min_day_back, then prefix-sum to get cumulative.
	acctHist := make([]uint64, *days)
	storHist := make([]uint64, *days)
	for _, db := range acctMin {
		acctHist[db]++
	}
	for _, db := range storMin {
		storHist[db]++
	}

	fmt.Println()
	fmt.Printf("=== Cumulative unique active over last %d days (head=%d, bucket=%d blocks) ===\n",
		*days, *headBlock, *bucketBlocks)
	fmt.Printf("total unique addresses (acct) : %d\n", len(acctMin))
	fmt.Printf("total unique (addr,slot) pairs: %d\n", len(storMin))
	fmt.Println()
	fmt.Println("Columns:")
	fmt.Println("  cum_*       = unique seen in the last d days (cumulative)")
	fmt.Println("  delta_*     = newly seen on day_back=d-1 (= cum[d] - cum[d-1])")
	fmt.Println("  retain_*    = cum[d-1] / cum[d]  (fraction of the d-day audience that was already in the d-1 day window)")
	fmt.Println("  new_rate_*  = delta / cum[d]     (fraction of d-day audience that is new in the oldest day)")
	fmt.Println()
	fmt.Println(" cum_day  block_range_back            cum_acct      delta_acct  retain_acct  new_rate_acct        cum_stor      delta_stor  retain_stor  new_rate_stor")

	totalAcct := uint64(len(acctMin))
	totalStor := uint64(len(storMin))
	if totalAcct == 0 {
		totalAcct = 1
	}
	if totalStor == 0 {
		totalStor = 1
	}

	var acctCum, storCum uint64
	prevAcct, prevStor := uint64(0), uint64(0)
	for d := 0; d < *days; d++ {
		acctCum += acctHist[d]
		storCum += storHist[d]
		fromBlk := endBlock - uint64(d+1)*(*bucketBlocks) + 1
		toBlk := endBlock
		deltaAcct := acctCum - prevAcct
		deltaStor := storCum - prevStor
		var retainAcct, retainStor, newAcctR, newStorR float64
		if acctCum > 0 {
			retainAcct = float64(prevAcct) * 100 / float64(acctCum)
			newAcctR = float64(deltaAcct) * 100 / float64(acctCum)
		}
		if storCum > 0 {
			retainStor = float64(prevStor) * 100 / float64(storCum)
			newStorR = float64(deltaStor) * 100 / float64(storCum)
		}
		fmt.Printf("  %3d    %10d - %10d  %12d  %12d   %7.2f%%       %7.2f%%   %12d  %12d   %7.2f%%       %7.2f%%\n",
			d+1, fromBlk, toBlk,
			acctCum, deltaAcct, retainAcct, newAcctR,
			storCum, deltaStor, retainStor, newStorR)
		prevAcct = acctCum
		prevStor = storCum
	}

	fmt.Println()
	fmt.Printf("Final 30-day audience: acct=%d  stor=%d\n", totalAcct, totalStor)
}
