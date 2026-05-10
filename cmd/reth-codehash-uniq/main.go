// reth-codehash-uniq scans reth's PlainAccountState, extracts the codeHash
// from each contract account (V2 fieldBits bit 3 set), and reports:
//
//	- total unique codeHashes
//	- top-K coverage (skew analysis)
//	- bytes saved by replacing 32B hash with N-byte dictionary id
//
// Reth Account compact layout (analogous to N42 V2):
//
//	[fieldFlags 1B] [nonce uvarint] [balLen 1B + balance N] [bytecode_hash 32B if present]
//
// We try both reth's compact and N42 V2 — first byte is fieldBits in either
// case; bit 3 (=8) means codeHash present and is the LAST 32B of the value.
// If len(v) >= 32 and last 32 bytes are non-zero we treat as codeHash.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const tblAcct = "PlainAccountState"

func tableCfg(d kv.TableCfg) kv.TableCfg {
	d[tblAcct] = kv.TableCfgItem{}
	return d
}

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	progress := flag.Duration("progress", 15*time.Second, "progress interval")
	flag.Parse()

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

	c, err := tx.Cursor(tblAcct)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()

	// codeHash → reference count
	hashCount := make(map[[32]byte]uint64, 4_000_000)
	var totalAccts, contractAccts uint64

	startT := time.Now()
	lastT := startT
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			break
		}
		_ = k
		totalAccts++
		if len(v) < 1 {
			continue
		}
		// Heuristic: if value ends with 32-byte non-zero block, treat as codeHash.
		// Both reth's compact and N42 V2 put codeHash last when present.
		if len(v) >= 32 {
			tail := v[len(v)-32:]
			// Skip "all zero" — not a real codeHash (means absent).
			allZero := true
			for _, b := range tail {
				if b != 0 {
					allZero = false
					break
				}
			}
			if !allZero {
				// Sanity: contract value length is typically >= 33 (header + at least 1B + 32B hash)
				if len(v) >= 33 {
					var h [32]byte
					copy(h[:], tail)
					hashCount[h]++
					contractAccts++
				}
			}
		}

		if time.Since(lastT) >= *progress {
			lastT = time.Now()
			elapsed := lastT.Sub(startT).Seconds()
			fmt.Fprintf(os.Stderr,
				"  scanned=%d contracts=%d uniq_hashes=%d rate=%.0f rec/s\n",
				totalAccts, contractAccts, len(hashCount),
				float64(totalAccts)/elapsed)
		}
	}
	fmt.Fprintf(os.Stderr, "scan done in %v\n", time.Since(startT))

	uniqHashes := uint64(len(hashCount))

	// Build sorted ref-count list for top-K analysis.
	counts := make([]uint64, 0, uniqHashes)
	for _, c := range hashCount {
		counts = append(counts, c)
	}
	sort.Slice(counts, func(i, j int) bool { return counts[i] > counts[j] })

	fmt.Println()
	fmt.Println("=== PlainAccountState codeHash distribution ===")
	fmt.Printf("total accounts        : %d\n", totalAccts)
	fmt.Printf("contract accounts     : %d  (%.2f%%)\n", contractAccts,
		float64(contractAccts)*100/float64(totalAccts))
	fmt.Printf("unique codeHashes     : %d\n", uniqHashes)
	fmt.Printf("avg refs/hash         : %.2f\n",
		float64(contractAccts)/float64(uniqHashes))
	if len(counts) > 0 {
		fmt.Printf("max refs (single hash): %d\n", counts[0])
	}

	// Top-K coverage
	fmt.Println("\nTop-K coverage:")
	checkpoints := []uint64{1, 10, 100, 1000, 10_000, 100_000, 1_000_000}
	var cumRefs uint64
	cpIdx := 0
	for i, c := range counts {
		cumRefs += c
		if cpIdx < len(checkpoints) && uint64(i+1) == checkpoints[cpIdx] {
			fmt.Printf("  top %-9d  hashes  cover %12d refs  (%6.2f%% of contracts)\n",
				checkpoints[cpIdx], cumRefs,
				float64(cumRefs)*100/float64(contractAccts))
			cpIdx++
		}
	}
	for ; cpIdx < len(checkpoints); cpIdx++ {
		fmt.Printf("  top %-9d  hashes  cover %12d refs  (%6.2f%% of contracts)\n",
			checkpoints[cpIdx], cumRefs,
			float64(cumRefs)*100/float64(contractAccts))
	}

	// Storage savings (replacing 32B hash with N-byte id)
	fmt.Println("\nStorage savings if codeHash → dict-id:")
	for _, idBytes := range []int{2, 3, 4} {
		dictHeader := uniqHashes * 32                    // hash table itself
		perAcctSave := uint64(32 - idBytes)             // bytes saved per contract entry
		gainedFromAcct := contractAccts * perAcctSave
		net := int64(gainedFromAcct) - int64(dictHeader)
		fmt.Printf("  id=%dB:  dict header = %.1f MiB,  acct save = %.2f GiB,  net = %.2f GiB  (covers %d ids)\n",
			idBytes,
			float64(dictHeader)/(1<<20),
			float64(gainedFromAcct)/(1<<30),
			float64(net)/(1<<30),
			uint64(1)<<(idBytes*8))
	}
}
