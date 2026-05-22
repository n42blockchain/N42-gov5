// n42-storage-trie-transcode reads reth's StoragesTrie (v1: 65-byte
// StoredNibblesSubKey) and writes the same data into a destination
// MDBX env under the v2 (33-byte PackedStoredNibblesSubKey) format.
//
// Source is opened READ-ONLY. Destination is a separate env so the
// original reth data is never mutated (per "reth absolutely
// read-only" project rule).
//
// Per-row transform:
//
//	source value: [65-byte v1 subkey][BranchNodeCompact]
//	dest value:   [33-byte v2 subkey][BranchNodeCompact]
//
// The BranchNodeCompact bytes pass through verbatim; only the
// subkey echo changes. Each row's saving = 32 bytes.
//
// Usage:
//
//	n42-storage-trie-transcode --src D:\reth2k\db --dst D:\n42-storage-trie-v2
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/mptproof"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	srcTable = "StoragesTrie"  // reth's table name
	dstTable = "StoragesTrieV2" // packed-33B variant
)

func main() {
	srcDB := flag.String("src", `D:\reth2k\db`, "reth env dir (read-only)")
	dstDB := flag.String("dst", `D:\n42-storage-trie-v2`, "destination env dir for v2 format")
	srcMapGB := flag.Int("src-mapsize-gb", 4096, "source env mapsize cap (GB)")
	dstMapGB := flag.Int("dst-mapsize-gb", 64, "destination env mapsize (GB)")
	commitEvery := flag.Int("commit-every", 1_000_000, "commit dst tx every N rows")
	limit := flag.Int64("limit", 0, "limit rows scanned (0 = all)")
	flag.Parse()

	if _, err := os.Stat(*srcDB); err != nil {
		fmt.Fprintf(os.Stderr, "src %s not present: %v\n", *srcDB, err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*dstDB, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "mkdir dst: %v\n", err)
		os.Exit(1)
	}

	logger := log.New()
	ctx := context.Background()

	src, err := mdbxkv.NewMDBX(logger).
		Path(*srcDB).Label(kv.ChainDB).PageSize(4096).
		MapSize(datasize.ByteSize(*srcMapGB) * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[srcTable] = kv.TableCfgItem{Flags: kv.DupSort}
			return d
		}).Open(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open src: %v\n", err)
		os.Exit(1)
	}
	defer src.Close()

	dst, err := mdbxkv.NewMDBX(logger).
		Path(*dstDB).Label(kv.ChainDB).PageSize(4096).
		MapSize(datasize.ByteSize(*dstMapGB) * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[dstTable] = kv.TableCfgItem{Flags: kv.DupSort}
			return d
		}).Open(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open dst: %v\n", err)
		os.Exit(1)
	}
	defer dst.Close()

	roTx, err := src.BeginRo(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "src BeginRo: %v\n", err)
		os.Exit(1)
	}
	defer roTx.Rollback()
	c, err := roTx.CursorDupSort(srcTable)
	if err != nil {
		fmt.Fprintf(os.Stderr, "src Cursor: %v\n", err)
		os.Exit(1)
	}
	defer c.Close()

	rwTx, err := dst.BeginRw(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dst BeginRw: %v\n", err)
		os.Exit(1)
	}
	dstCursor, err := rwTx.RwCursorDupSort(dstTable)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dst Cursor: %v\n", err)
		os.Exit(1)
	}

	t0 := time.Now()
	lastLog := t0
	var rows, srcBytes, dstBytes, skipped uint64

	// Pre-allocated dst value buffer to avoid per-row allocation.
	dstVal := make([]byte, 0, 4096)
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			fmt.Fprintf(os.Stderr, "Next: %v\n", err)
			os.Exit(1)
		}
		if len(v) < 65 {
			skipped++
			continue
		}
		// Decode the 65-byte v1 subkey: bytes [0..length) are the
		// nibble payload, byte 64 is the length.
		nibLen := int(v[64])
		if nibLen > 64 {
			skipped++
			continue
		}
		// Build nibble slice and pack.
		nibSlice := make([]byte, nibLen)
		copy(nibSlice, v[:nibLen])
		// Validate nibble values 0..15.
		for i, n := range nibSlice {
			if n > 15 {
				fmt.Fprintf(os.Stderr, "row %d: nibble[%d]=%d invalid; skipping\n", rows, i, n)
				skipped++
				goto next
			}
		}
		{
			packed, perr := mptproof.PackNibblesV2(nibSlice)
			if perr != nil {
				fmt.Fprintf(os.Stderr, "row %d: pack: %v\n", rows, perr)
				skipped++
				goto next
			}
			// New value = packed-33 + BranchNodeCompact (v[65:]).
			dstVal = dstVal[:0]
			dstVal = append(dstVal, packed...)
			dstVal = append(dstVal, v[65:]...)
			// AppendDup: only valid if we're walking dst in same
			// sort order as src. Src is cursor.First()/Next() which
			// gives ascending. Dst with same DupSort gives ascending
			// too. AppendDup is the fast path.
			if aerr := dstCursor.AppendDup(k, dstVal); aerr != nil {
				// Fallback to Put for safety.
				if perr := dstCursor.Put(k, dstVal); perr != nil {
					fmt.Fprintf(os.Stderr, "row %d: Put: %v\n", rows, perr)
					os.Exit(1)
				}
			}
			rows++
			srcBytes += uint64(len(k) + len(v))
			dstBytes += uint64(len(k) + len(dstVal))
		}

		if rows > 0 && rows%uint64(*commitEvery) == 0 {
			dstCursor.Close()
			if cerr := rwTx.Commit(); cerr != nil {
				fmt.Fprintf(os.Stderr, "commit: %v\n", cerr)
				os.Exit(1)
			}
			rwTx, err = dst.BeginRw(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "BeginRw: %v\n", err)
				os.Exit(1)
			}
			dstCursor, err = rwTx.RwCursorDupSort(dstTable)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Cursor: %v\n", err)
				os.Exit(1)
			}
		}
		if rows%5_000_000 == 0 && time.Since(lastLog) > 10*time.Second {
			rate := float64(rows) / time.Since(t0).Seconds()
			fmt.Printf("  rows=%d (%.0f K/s)  src=%.2f GB  dst=%.2f GB  saving=%.2f GB  elapsed=%s\n",
				rows, rate/1000,
				float64(srcBytes)/1024/1024/1024,
				float64(dstBytes)/1024/1024/1024,
				float64(srcBytes-dstBytes)/1024/1024/1024,
				time.Since(t0).Truncate(time.Second))
			lastLog = time.Now()
		}
	next:
		if *limit > 0 && int64(rows) >= *limit {
			break
		}
	}

	dstCursor.Close()
	if cerr := rwTx.Commit(); cerr != nil {
		fmt.Fprintf(os.Stderr, "final commit: %v\n", cerr)
		os.Exit(1)
	}

	elapsed := time.Since(t0)
	fmt.Println()
	fmt.Printf("=== transcode complete in %s ===\n", elapsed.Truncate(time.Second))
	fmt.Printf("rows transcoded : %d\n", rows)
	fmt.Printf("rows skipped    : %d\n", skipped)
	fmt.Printf("src logical size: %.2f GB\n", float64(srcBytes)/1024/1024/1024)
	fmt.Printf("dst logical size: %.2f GB\n", float64(dstBytes)/1024/1024/1024)
	fmt.Printf("saved (logical) : %.2f GB (%.1f%%)\n",
		float64(srcBytes-dstBytes)/1024/1024/1024,
		100*float64(srcBytes-dstBytes)/float64(srcBytes))
}
