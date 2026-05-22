// n42-storage-trie-measure scans a reth StoragesTrie table and
// reports actual sizes under several alternative encodings:
//
//   - reth v1 (65-byte fixed StoredNibblesSubKey) — baseline
//   - reth v2 (33-byte fixed PackedStoredNibblesSubKey) — RB-5a
//   - variable-length packed (ceil(N/2) + 1 byte length) — RB-5d
//     projection (would need custom DupSort comparator)
//   - "no subkey" — schema change that absorbs subkey into key
//
// Reports per-row average, total bytes, and saving vs baseline.
// Reads reth tables READ-ONLY.
//
// Usage:
//
//	n42-storage-trie-measure --reth D:\reth2k\db [--limit N]
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
)

const rethStoragesTrieTable = "StoragesTrie"

func main() {
	rethDB := flag.String("reth", `D:\reth2k\db`, "reth env dir")
	mapsizeGB := flag.Int("mapsize-gb", 4096, "reth env mapsize")
	limit := flag.Int64("limit", 0, "limit rows scanned (0 = all)")
	flag.Parse()

	if _, err := os.Stat(*rethDB); err != nil {
		fmt.Fprintf(os.Stderr, "reth db %s not present: %v\n", *rethDB, err)
		os.Exit(1)
	}

	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(*rethDB).Label(kv.ChainDB).PageSize(4096).
		MapSize(datasize.ByteSize(*mapsizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[rethStoragesTrieTable] = kv.TableCfgItem{Flags: kv.DupSort}
			return d
		}).Open(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open env: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	c, _ := tx.CursorDupSort(rethStoragesTrieTable)
	defer c.Close()

	var (
		rows           uint64
		totalRowBytes  uint64
		totalKeyBytes  uint64
		totalValBytes  uint64
		totalBranchLen uint64
		totalNibCount  uint64
		distNib        [65]uint64

		// Per-scheme projected value-section size.
		v1Total uint64 // 65 + branchLen
		v2Total uint64 // 33 + branchLen (RB-5a)
		vlTotal uint64 // ceil(N/2)+1 + branchLen (RB-5d)
		nsTotal uint64 // branchLen only (subkey absorbed into key)
	)

	t0 := time.Now()
	lastLog := t0
	fmt.Println("scanning reth StoragesTrie...")
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			fmt.Fprintf(os.Stderr, "Next: %v\n", err)
			os.Exit(1)
		}
		if len(v) < 65 {
			continue
		}
		rows++
		totalKeyBytes += uint64(len(k))
		totalValBytes += uint64(len(v))
		totalRowBytes += uint64(len(k) + len(v))

		// reth v1 layout: [subkey 65B][BranchNodeCompact].
		// length byte at offset 64.
		nibLen := int(v[64])
		if nibLen > 64 {
			nibLen = 64
		}
		distNib[nibLen]++
		totalNibCount += uint64(nibLen)

		// branchLen = total value bytes - 65-byte subkey.
		branchLen := uint64(len(v) - 65)
		totalBranchLen += branchLen

		v1Total += 65 + branchLen
		v2Total += 33 + branchLen
		vlTotal += uint64((nibLen+1)/2+1) + branchLen
		nsTotal += branchLen

		if *limit > 0 && int64(rows) >= *limit {
			break
		}
		if rows%5_000_000 == 0 && time.Since(lastLog) > 10*time.Second {
			fmt.Printf("  scanned %d rows (%.2f GB val) in %s\n",
				rows, float64(totalValBytes)/1024/1024/1024, time.Since(t0).Truncate(time.Second))
			lastLog = time.Now()
		}
	}
	elapsed := time.Since(t0)

	fmt.Println()
	fmt.Printf("=== scan complete in %s ===\n", elapsed.Truncate(time.Second))
	fmt.Printf("rows scanned : %d\n", rows)
	fmt.Printf("avg nibLen   : %.2f\n", float64(totalNibCount)/float64(rows))
	fmt.Printf("avg branchLen: %.1f B\n", float64(totalBranchLen)/float64(rows))
	fmt.Println()
	fmt.Println("--- nibble depth distribution (count by length) ---")
	cum := uint64(0)
	for d, n := range distNib {
		if n == 0 {
			continue
		}
		cum += n
		fmt.Printf("  len=%2d  %12d rows  (cum %5.1f%%)\n",
			d, n, 100*float64(cum)/float64(rows))
	}

	fmt.Println()
	fmt.Println("--- value-section size projection per scheme ---")
	fmt.Printf("scheme               total           avg/row    vs reth v1\n")
	fmt.Printf("reth v1 (65B sub)    %9.2f GB    %5.1f B      baseline\n",
		float64(v1Total)/1024/1024/1024, float64(v1Total)/float64(rows))
	fmt.Printf("reth v2 (33B sub)    %9.2f GB    %5.1f B      %+6.2f GB (%+5.1f%%)\n",
		float64(v2Total)/1024/1024/1024, float64(v2Total)/float64(rows),
		(float64(v2Total)-float64(v1Total))/1024/1024/1024,
		100*(float64(v2Total)-float64(v1Total))/float64(v1Total))
	fmt.Printf("RB-5d var-len pack   %9.2f GB    %5.1f B      %+6.2f GB (%+5.1f%%)\n",
		float64(vlTotal)/1024/1024/1024, float64(vlTotal)/float64(rows),
		(float64(vlTotal)-float64(v1Total))/1024/1024/1024,
		100*(float64(vlTotal)-float64(v1Total))/float64(v1Total))
	fmt.Printf("schema-change (no sub-echo) %4.2f GB    %5.1f B      %+6.2f GB (%+5.1f%%)\n",
		float64(nsTotal)/1024/1024/1024, float64(nsTotal)/float64(rows),
		(float64(nsTotal)-float64(v1Total))/1024/1024/1024,
		100*(float64(nsTotal)-float64(v1Total))/float64(v1Total))
	fmt.Println()
	fmt.Println("(note: schema-change moves subkey into MDBX key — saves 100% subkey cost")
	fmt.Println("       but requires migrating from DupSort to plain table + composite key)")
}
