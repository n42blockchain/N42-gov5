// n42-chaindata-compact does a manual env-copy with compaction
// for MDBX environments where the upstream mdbx-go binding has
// disabled the native env_copy2 API.
//
// Strategy: open source RO, open destination RW (fresh), iterate
// each named table via cursor and AppendDup (or Append for
// non-DupSort), commit at the end. The result is a fresh env
// containing only the named tables' live data, with no free-list
// padding or pre-allocated growth. File size matches actual
// content.
//
// Atomicity: copy goes into <dst-tmp>; on success we exit 0 and
// the operator does the swap manually with:
//
//	(stop any process holding src)
//	mv <src> <src>.bak
//	mv <dst-tmp> <src>
//	(start process)
//	(once verified) rm -rf <src>.bak
//
// The tool deliberately does NOT auto-swap so the operator
// controls the destructive moment.
//
// Usage:
//
//	n42-chaindata-compact --src D:\n42-chaindata --dst D:\n42-chaindata.compact \
//	  --tables AccountsTrie,StoragesTrie,Meta
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	src := flag.String("src", "", "source MDBX env (read-only)")
	dst := flag.String("dst", "", "destination MDBX env (created fresh; must not exist)")
	tablesCSV := flag.String("tables", "AccountsTrie,StoragesTrie,Meta", "comma-separated tables to copy")
	srcMapGB := flag.Int("src-mapsize-gb", 1024, "source env mapsize cap (GB)")
	dstMapGB := flag.Int("dst-mapsize-gb", 64, "destination env mapsize (GB) — must fit copied data + ~10%")
	commitEvery := flag.Int("commit-every", 5_000_000, "commit dst tx every N rows (per table)")
	dupTables := flag.String("dup-tables", "StoragesTrie", "comma-separated tables that use DupSort (uses AppendDup fast-path)")
	flag.Parse()

	if *src == "" || *dst == "" {
		die("--src and --dst are required")
	}
	if _, err := os.Stat(*src); err != nil {
		die("src %s not accessible: %v", *src, err)
	}
	if _, err := os.Stat(*dst); err == nil {
		die("dst %s already exists; refusing to overwrite", *dst)
	}
	if err := os.MkdirAll(*dst, 0o755); err != nil {
		die("mkdir dst: %v", err)
	}

	tables := splitCSV(*tablesCSV)
	dups := splitCSVSet(*dupTables)

	logger := log.New()
	ctx := context.Background()

	tableCfg := func(d kv.TableCfg) kv.TableCfg {
		for _, t := range tables {
			cfg := kv.TableCfgItem{}
			if _, ok := dups[t]; ok {
				cfg.Flags = kv.DupSort
			}
			d[t] = cfg
		}
		return d
	}

	srcDB, err := mdbxkv.NewMDBX(logger).
		Path(*src).Label(kv.ChainDB).PageSize(4096).
		MapSize(datasize.ByteSize(*srcMapGB) * datasize.GB).
		Readonly().
		WithTableCfg(tableCfg).Open(ctx)
	if err != nil {
		die("open src: %v", err)
	}
	defer srcDB.Close()

	dstDB, err := mdbxkv.NewMDBX(logger).
		Path(*dst).Label(kv.ChainDB).PageSize(4096).
		MapSize(datasize.ByteSize(*dstMapGB) * datasize.GB).
		WithTableCfg(tableCfg).Open(ctx)
	if err != nil {
		die("open dst: %v", err)
	}
	defer dstDB.Close()

	t0 := time.Now()
	var grandRows uint64
	var grandBytes uint64
	for _, t := range tables {
		isDup := false
		if _, ok := dups[t]; ok {
			isDup = true
		}
		fmt.Printf("=== copying %s (dupsort=%v) ===\n", t, isDup)
		ts := time.Now()
		rows, bytes, err := copyTable(ctx, srcDB, dstDB, t, isDup, *commitEvery)
		if err != nil {
			die("copy %s: %v", t, err)
		}
		grandRows += rows
		grandBytes += bytes
		rate := float64(rows) / time.Since(ts).Seconds()
		fmt.Printf("    %s: rows=%d bytes=%.2f GB elapsed=%s (%.0f rows/s)\n",
			t, rows, float64(bytes)/1024/1024/1024,
			time.Since(ts).Truncate(time.Second), rate)
	}

	fmt.Println()
	fmt.Printf("=== compact complete in %s ===\n", time.Since(t0).Truncate(time.Second))
	fmt.Printf("total rows  : %d\n", grandRows)
	fmt.Printf("total bytes : %.2f GB\n", float64(grandBytes)/1024/1024/1024)
	fmt.Printf("dst path    : %s\n", *dst)
	fmt.Println()
	fmt.Println("NEXT STEPS (operator):")
	fmt.Printf("  1. ls -lh %s/mdbx.dat   # confirm size\n", *dst)
	fmt.Println("  2. (stop any process holding src)")
	fmt.Printf("  3. mv %s %s.bak\n", *src, *src)
	fmt.Printf("  4. mv %s %s\n", *dst, *src)
	fmt.Println("  5. (start process, verify)")
	fmt.Printf("  6. rm -rf %s.bak   # only once verified\n", *src)
}

// copyTable walks src.<table> and writes every (k, v) into
// dst.<table> using AppendDup (DupSort) or Append (plain). Both
// are O(1) per row when source is in sorted order — which it is
// when we walk via cursor.First/Next on the same key/value
// schema.
//
// Progress is printed every progressEvery rows so an external
// monitor can detect a stall.
const progressEvery uint64 = 500_000

func copyTable(ctx context.Context, srcDB, dstDB kv.RwDB, table string, dup bool, commitEvery int) (uint64, uint64, error) {
	srcTx, err := srcDB.BeginRo(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("src begin: %w", err)
	}
	defer srcTx.Rollback()

	dstTx, err := dstDB.BeginRw(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("dst begin: %w", err)
	}

	var (
		rows  uint64
		bytes uint64
	)
	commit := func() error {
		if cerr := dstTx.Commit(); cerr != nil {
			return cerr
		}
		var berr error
		dstTx, berr = dstDB.BeginRw(ctx)
		return berr
	}

	if dup {
		sc, err := srcTx.CursorDupSort(table)
		if err != nil {
			return rows, bytes, fmt.Errorf("src cursor: %w", err)
		}
		defer sc.Close()
		dc, err := dstTx.RwCursorDupSort(table)
		if err != nil {
			return rows, bytes, fmt.Errorf("dst cursor: %w", err)
		}
		ts := time.Now()
		for k, v, err := sc.First(); k != nil; k, v, err = sc.Next() {
			if err != nil {
				return rows, bytes, fmt.Errorf("src iter: %w", err)
			}
			if aerr := dc.AppendDup(k, v); aerr != nil {
				if perr := dc.Put(k, v); perr != nil {
					return rows, bytes, fmt.Errorf("dst put: %w", perr)
				}
			}
			rows++
			bytes += uint64(len(k) + len(v))
			if rows%progressEvery == 0 {
				rate := float64(rows) / time.Since(ts).Seconds()
				fmt.Printf("    %s dup: rows=%d (%.0fK/s) bytes=%.2f GB elapsed=%s\n",
					table, rows, rate/1000, float64(bytes)/1024/1024/1024, time.Since(ts).Truncate(time.Second))
			}
			if commitEvery > 0 && rows%uint64(commitEvery) == 0 {
				dc.Close()
				if cerr := commit(); cerr != nil {
					return rows, bytes, fmt.Errorf("commit at row %d: %w", rows, cerr)
				}
				dc, err = dstTx.RwCursorDupSort(table)
				if err != nil {
					return rows, bytes, fmt.Errorf("dst re-cursor: %w", err)
				}
			}
		}
		dc.Close()
	} else {
		sc, err := srcTx.Cursor(table)
		if err != nil {
			return rows, bytes, fmt.Errorf("src cursor: %w", err)
		}
		defer sc.Close()
		dc, err := dstTx.RwCursor(table)
		if err != nil {
			return rows, bytes, fmt.Errorf("dst cursor: %w", err)
		}
		ts := time.Now()
		for k, v, err := sc.First(); k != nil; k, v, err = sc.Next() {
			if err != nil {
				return rows, bytes, fmt.Errorf("src iter: %w", err)
			}
			if aerr := dc.Append(k, v); aerr != nil {
				if perr := dc.Put(k, v); perr != nil {
					return rows, bytes, fmt.Errorf("dst put: %w", perr)
				}
			}
			rows++
			bytes += uint64(len(k) + len(v))
			if rows%progressEvery == 0 {
				rate := float64(rows) / time.Since(ts).Seconds()
				fmt.Printf("    %s plain: rows=%d (%.0fK/s) bytes=%.2f GB elapsed=%s\n",
					table, rows, rate/1000, float64(bytes)/1024/1024/1024, time.Since(ts).Truncate(time.Second))
			}
			if commitEvery > 0 && rows%uint64(commitEvery) == 0 {
				dc.Close()
				if cerr := commit(); cerr != nil {
					return rows, bytes, fmt.Errorf("commit at row %d: %w", rows, cerr)
				}
				dc, err = dstTx.RwCursor(table)
				if err != nil {
					return rows, bytes, fmt.Errorf("dst re-cursor: %w", err)
				}
			}
		}
		dc.Close()
	}

	if cerr := dstTx.Commit(); cerr != nil {
		return rows, bytes, fmt.Errorf("final commit: %w", cerr)
	}
	return rows, bytes, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func splitCSVSet(s string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, t := range splitCSV(s) {
		out[t] = struct{}{}
	}
	return out
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
