// n42-trie-branch-anatomy: dissect reth's AccountsTrie / StoragesTrie
// branch nodes byte-by-byte to test whether HPH ReplaceKeysInValues
// can buy anything reth's encoding hasn't already captured.
//
// reth BranchNodeCompact wire format:
//
//	state_mask  u16 LE  (which of 16 nibbles have a child)
//	tree_mask   u16 LE  (which children are themselves trie roots)
//	hash_mask   u16 LE  (which children we stored explicit hashes for)
//	hashes      [B256; popcount(hash_mask)]
//	root_hash   Option<B256>  (Some if leaf-only branch, None otherwise)
//
// Key observation: branch values contain ONLY hashes — no plaintext
// account addresses, no slot keys. The plain values live separately in
// PlainAccountState / PlainStorageState. This is structurally what
// Erigon's HPH "ReplaceKeysInValues" optimization buys for Erigon
// commitment branches: replace 20/52-byte plain keys with 8-byte file
// offsets into the values table. reth has already separated; nothing
// left to replace.
//
// This tool samples N branch values, decodes the masks, and reports:
//   - average hashes per branch (popcount(hash_mask))
//   - average value bytes
//   - theoretical lower bound bytes (if branches contained 0 padding)
//
// If reth's actual bytes ≈ theoretical lower bound, then no further
// trivial schema-level compression is possible.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"math/bits"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func tableCfg(table string) func(kv.TableCfg) kv.TableCfg {
	return func(d kv.TableCfg) kv.TableCfg {
		d[table] = kv.TableCfgItem{}
		return d
	}
}

func main() {
	dbPath := flag.String("db", `D:\reth2k\db`, "reth MDBX dir (readonly)")
	table := flag.String("table", "AccountsTrie", "AccountsTrie | StoragesTrie")
	samples := flag.Int("samples", 200_000, "branch nodes to analyse")
	mapSizeGB := flag.Int("mapsize-gb", 4096, "DB mapsize cap")
	flag.Parse()

	logger := log.New()
	t0 := time.Now()
	db, err := mdbxkv.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(*mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(tableCfg(*table)).
		Open(context.Background())
	if err != nil {
		fatal("open: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fatal("tx: %v", err)
	}
	defer tx.Rollback()

	mtx := tx.(*mdbxkv.MdbxTx)
	st, err := mtx.BucketStat(*table)
	if err != nil {
		fatal("stat: %v", err)
	}
	totalEntries := st.Entries

	c, err := tx.Cursor(*table)
	if err != nil {
		fatal("cursor: %v", err)
	}
	defer c.Close()

	var (
		n               int
		totalKeyBytes   uint64
		totalValBytes   uint64
		totalHashes     uint64
		totalRootHashes uint64
		minBranchBytes  uint64
		hist            [17]uint64 // children count distribution
	)
	for k, v, err := c.First(); err == nil && k != nil && n < *samples; k, v, err = c.Next() {
		totalKeyBytes += uint64(len(k))
		totalValBytes += uint64(len(v))

		// Parse: state_mask (2B LE) + tree_mask (2B LE) + hash_mask (2B LE)
		if len(v) < 6 {
			continue
		}
		hashMask := binary.LittleEndian.Uint16(v[4:6])
		hashes := bits.OnesCount16(hashMask)
		totalHashes += uint64(hashes)

		// Bytes after masks should be 32 × hashes + optional 32 root.
		want := 6 + 32*hashes
		if len(v) >= want+32 && len(v) == want+32 {
			totalRootHashes++
		}

		// Theoretical lower bound: 6 (masks) + 32 × hashes + (32 if root present).
		theoretical := 6 + uint64(hashes)*32
		if len(v) >= want+32 && len(v) == want+32 {
			theoretical += 32
		}
		minBranchBytes += theoretical

		state := binary.LittleEndian.Uint16(v[0:2])
		hist[bits.OnesCount16(state)]++
		n++
	}

	fmt.Printf("samples              %d (of %d total)\n", n, totalEntries)
	fmt.Printf("avg key bytes        %.2f\n", float64(totalKeyBytes)/float64(n))
	fmt.Printf("avg value bytes      %.2f\n", float64(totalValBytes)/float64(n))
	fmt.Printf("avg hashes/branch    %.2f\n", float64(totalHashes)/float64(n))
	fmt.Printf("branches w/ root     %.1f%%\n", 100*float64(totalRootHashes)/float64(n))
	fmt.Printf("theoretical bytes    %.2f (lower bound: masks + hashes)\n",
		float64(minBranchBytes)/float64(n))
	fmt.Printf("encoding overhead    %.2f bytes/branch (val - theoretical)\n",
		float64(totalValBytes-minBranchBytes)/float64(n))
	overheadPct := 100 * float64(totalValBytes-minBranchBytes) / float64(totalValBytes)
	fmt.Printf("overhead %%           %.2f%%\n", overheadPct)

	fmt.Println()
	fmt.Println("children-count distribution (state_mask popcount):")
	for i := 0; i < 17; i++ {
		if hist[i] > 0 {
			fmt.Printf("  %2d children:  %d  (%.2f%%)\n", i, hist[i], 100*float64(hist[i])/float64(n))
		}
	}

	fmt.Println()
	fmt.Println("=== HPH ReplaceKeysInValues applicability ===")
	fmt.Println("reth branch values contain ONLY hashes + masks (no plaintext")
	fmt.Println("addresses or slots). HPH's RKV optimization replaces plain")
	fmt.Println("keys with file offsets — but there are no plain keys here to")
	fmt.Println("replace. reth already separated keys from trie structure.")
	fmt.Printf("Total table  =  %.2f GB (%d entries × %.0f B avg)\n",
		float64(totalEntries)*float64(totalValBytes)/float64(n)/1e9,
		totalEntries, float64(totalValBytes)/float64(n))
	fmt.Printf("Theoretical floor (perfect schema) = %.2f GB  (%.1f%% saved)\n",
		float64(totalEntries)*float64(minBranchBytes)/float64(n)/1e9, overheadPct)
	fmt.Println()
	fmt.Printf("Duration: %s\n", time.Since(t0).Truncate(time.Millisecond))
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
