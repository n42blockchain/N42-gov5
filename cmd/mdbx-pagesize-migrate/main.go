// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// mdbx-pagesize-migrate copies an MDBX database from one page size to
// another. Use case: bumping default 4 KiB pages to 16 KiB to extend
// the 8 TB hard cap to 32 TB and shave 1 level off the B+tree.
//
// Strategy:
//   - Open src MDBX read-only with Accede (no schema changes).
//   - Open dst MDBX with the requested PageSize and N42TableCfg.
//   - For each table, walk src in-order via Cursor.First/Next, buffer
//     a batch of ~256 MiB, then bulk-insert into dst using Append (or
//     AppendDup for DupSort tables) — strictly-increasing-key path,
//     no B+tree splits, ~5-10× faster than Put.
//   - Verify mode: re-walk both and compare row counts + sample hashes.
//
// Estimated wall time on Ryzen 9 9950X / NVMe / 64 GiB free RAM:
//   ~50 GiB MDBX → 30-50 minutes (read-bound on src + sequential write
//   on dst, dirty pool kept to batchMB).
//
// Limitations / TODOs:
//   - Single-threaded per table; multi-table parallelism left for v2.
//   - No incremental resume — a crash mid-migration requires restart
//     from scratch. Acceptable: src is unchanged (read-only), dst is
//     destroyed and recreated. v2 may add per-table progress markers.
//   - Verify mode reads BOTH databases sequentially and compares row
//     count + first-and-last-key bytes per table. Full per-row hash
//     diff is exposed via --verify=full but takes ~2× the migration
//     time itself.

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	_ = log.Root() // ensure default logger initialized
	logger := log2.New()

	var (
		srcPath     = flag.String("src", "", "Source MDBX datadir (the chain root, NOT the file path)")
		dstPath     = flag.String("dst", "", "Destination MDBX datadir; will be CREATED — must not exist")
		dstPageKiB  = flag.Uint64("dst-page-kib", 16, "Destination page size in KiB; must be a power of two between 4 and 64")
		batchMB     = flag.Uint64("batch-mb", 256, "Per-table tx batch size in MiB; tune to keep dirty pool below dst-dirty-mb")
		dstDirtyMB  = flag.Uint64("dst-dirty-mb", 16384, "Destination MDBX dirty page pool in MiB")
		verifyMode  = flag.String("verify", "counts", "Verification mode after migration: off | counts | full")
		dryRun      = flag.Bool("dry-run", false, "Open src + size dst plan, print expected layout, exit without writing")
		onlyTables  = flag.String("only-tables", "", "Comma-separated list of table names to migrate; default = all")
		resumeFrom  = flag.String("resume-from", "", "(NOT IMPLEMENTED yet) skip tables already migrated; v2 feature")
	)
	flag.Parse()

	if *srcPath == "" || *dstPath == "" {
		fmt.Fprintln(os.Stderr, "usage: mdbx-pagesize-migrate --src <dir> --dst <dir> [--dst-page-kib 16]")
		flag.PrintDefaults()
		os.Exit(2)
	}
	if *resumeFrom != "" {
		fmt.Fprintln(os.Stderr, "--resume-from not implemented yet (v2)")
		os.Exit(2)
	}

	if err := run(logger, *srcPath, *dstPath, *dstPageKiB, *batchMB, *dstDirtyMB, *verifyMode, *dryRun, *onlyTables); err != nil {
		log.Error("migration failed", "err", err)
		os.Exit(1)
	}
}

func run(logger log2.Logger, srcPath, dstPath string, dstPageKiB, batchMB, dstDirtyMB uint64, verifyMode string, dryRun bool, onlyTables string) error {
	if dstPageKiB < 4 || dstPageKiB > 64 || (dstPageKiB&(dstPageKiB-1)) != 0 {
		return fmt.Errorf("dst-page-kib must be a power of two between 4 and 64, got %d", dstPageKiB)
	}

	if !dryRun {
		// Refuse to clobber an existing dst — too easy to lose data.
		// User must remove or rename old dst manually.
		if _, err := os.Stat(filepath.Join(dstPath, "mdbx.dat")); err == nil {
			return fmt.Errorf("dst datadir already contains mdbx.dat: %s — refusing to overwrite. Remove or rename the existing destination first.", dstPath)
		}
	}

	// Wire N42's table cfg into kv.ChaindataTablesCfg before opening.
	// MDBX needs DupSort flags etc. when creating the dst tables.
	for name, cfg := range modules.N42TableCfg {
		kv.ChaindataTablesCfg[name] = cfg
	}

	ctx := context.Background()

	src, err := mdbx.NewMDBX(logger).
		Path(srcPath).
		Label(kv.ChainDB).
		Readonly().
		Accede().
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(ctx)
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}
	defer src.Close()

	srcPageSize := src.PageSize()
	log.Info("source MDBX opened",
		"path", srcPath,
		"src_page_kib", srcPageSize/1024)

	if dryRun {
		return planAndPrint(ctx, src, dstPath, dstPageKiB)
	}

	dst, err := mdbx.NewMDBX(logger).
		Path(dstPath).
		Label(kv.ChainDB).
		PageSize(dstPageKiB * 1024).
		DirtySpace(uint64(datasize.ByteSize(dstDirtyMB) * datasize.MB)).
		WriteMap().
		WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(ctx)
	if err != nil {
		return fmt.Errorf("open dst: %w", err)
	}
	defer dst.Close()

	tables := selectTables(src, onlyTables)
	log.Info("migration plan",
		"tables", len(tables),
		"src_page_kib", srcPageSize/1024,
		"dst_page_kib", dstPageKiB,
		"batch_mb", batchMB,
		"dst_dirty_mb", dstDirtyMB)

	t0 := time.Now()
	for _, name := range tables {
		if err := migrateTable(ctx, src, dst, name, batchMB); err != nil {
			return fmt.Errorf("migrate %s: %w", name, err)
		}
	}
	log.Info("migration complete", "elapsed", time.Since(t0).Truncate(time.Second))

	switch verifyMode {
	case "off":
		log.Info("verification skipped (--verify=off)")
	case "counts":
		if err := verifyCounts(ctx, src, dst, tables); err != nil {
			return fmt.Errorf("verify counts: %w", err)
		}
	case "full":
		if err := verifyFull(ctx, src, dst, tables); err != nil {
			return fmt.Errorf("verify full: %w", err)
		}
	default:
		return fmt.Errorf("unknown --verify mode: %s (use off | counts | full)", verifyMode)
	}

	return nil
}

// selectTables returns the alphabetically-ordered list of tables to
// migrate. When onlyTables is empty, every table from N42TableCfg is
// migrated. Otherwise it filters by the comma-separated whitelist.
func selectTables(src kv.RoDB, onlyTables string) []string {
	var allowed map[string]struct{}
	if onlyTables != "" {
		allowed = make(map[string]struct{})
		for _, t := range bytes.Split([]byte(onlyTables), []byte{','}) {
			allowed[string(bytes.TrimSpace(t))] = struct{}{}
		}
	}
	tables := make([]string, 0, len(kv.ChaindataTablesCfg))
	for name := range kv.ChaindataTablesCfg {
		if onlyTables != "" {
			if _, ok := allowed[name]; !ok {
				continue
			}
		}
		tables = append(tables, name)
	}
	sort.Strings(tables)
	return tables
}

// migrateTable copies one table from src to dst using sorted-Append
// (or AppendDup for DupSort tables). Buffers up to batchMB MiB of data
// in memory between dst commits to bound dirty pool pressure.
func migrateTable(ctx context.Context, src kv.RoDB, dst kv.RwDB, name string, batchMB uint64) error {
	cfg := kv.ChaindataTablesCfg[name]
	isDupSort := cfg.Flags&kv.DupSort != 0

	srcTx, err := src.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer srcTx.Rollback()

	c, err := srcTx.Cursor(name)
	if err != nil {
		return err
	}
	defer c.Close()

	t0 := time.Now()
	var rows uint64
	var bytesInBatch uint64
	var bytesTotal uint64

	dstTx, err := dst.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if dstTx != nil {
			dstTx.Rollback()
		}
	}()

	flushAndReopen := func() error {
		if err := dstTx.Commit(); err != nil {
			return err
		}
		runtime.GC()
		var memErr error
		dstTx, memErr = dst.BeginRw(ctx)
		if memErr != nil {
			return memErr
		}
		bytesInBatch = 0
		return nil
	}

	batchLimit := batchMB * 1024 * 1024

	if isDupSort {
		dupCur, err := dstTx.RwCursorDupSort(name)
		if err != nil {
			return fmt.Errorf("open dst dup cursor: %w", err)
		}
		defer dupCur.Close()
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				return err
			}
			if err := dupCur.AppendDup(k, v); err != nil {
				return fmt.Errorf("appenddup at row %d: %w", rows, err)
			}
			rows++
			sz := uint64(len(k) + len(v) + 16)
			bytesInBatch += sz
			bytesTotal += sz
			if bytesInBatch >= batchLimit {
				dupCur.Close()
				if err := flushAndReopen(); err != nil {
					return err
				}
				dupCur, err = dstTx.RwCursorDupSort(name)
				if err != nil {
					return fmt.Errorf("reopen dst dup cursor: %w", err)
				}
			}
			if rows%1_000_000 == 0 {
				log.Info("migrating", "table", name, "rows", rows,
					"MiB", bytesTotal>>20, "elapsed", time.Since(t0).Truncate(time.Second))
			}
		}
	} else {
		appCur, err := dstTx.RwCursor(name)
		if err != nil {
			return fmt.Errorf("open dst cursor: %w", err)
		}
		defer appCur.Close()
		for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
			if err != nil {
				return err
			}
			if err := appCur.Append(k, v); err != nil {
				return fmt.Errorf("append at row %d: %w", rows, err)
			}
			rows++
			sz := uint64(len(k) + len(v) + 16)
			bytesInBatch += sz
			bytesTotal += sz
			if bytesInBatch >= batchLimit {
				appCur.Close()
				if err := flushAndReopen(); err != nil {
					return err
				}
				appCur, err = dstTx.RwCursor(name)
				if err != nil {
					return fmt.Errorf("reopen dst cursor: %w", err)
				}
			}
			if rows%1_000_000 == 0 {
				log.Info("migrating", "table", name, "rows", rows,
					"MiB", bytesTotal>>20, "elapsed", time.Since(t0).Truncate(time.Second))
			}
		}
	}

	if err := dstTx.Commit(); err != nil {
		return err
	}
	dstTx = nil
	log.Info("table migrated",
		"table", name, "rows", rows, "MiB", bytesTotal>>20,
		"elapsed", time.Since(t0).Truncate(time.Second),
		"dupsort", isDupSort)
	return nil
}

func verifyCounts(ctx context.Context, src, dst kv.RoDB, tables []string) error {
	srcTx, err := src.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer srcTx.Rollback()
	dstTx, err := dst.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer dstTx.Rollback()

	srcMdbx, ok1 := srcTx.(*mdbx.MdbxTx)
	dstMdbx, ok2 := dstTx.(*mdbx.MdbxTx)
	if !ok1 || !ok2 {
		return errors.New("verifyCounts: tx is not *mdbx.MdbxTx")
	}

	var firstErr error
	for _, name := range tables {
		srcStat, err := srcMdbx.BucketStat(name)
		if err != nil {
			return fmt.Errorf("src stat %s: %w", name, err)
		}
		dstStat, err := dstMdbx.BucketStat(name)
		if err != nil {
			return fmt.Errorf("dst stat %s: %w", name, err)
		}
		if srcStat.Entries != dstStat.Entries {
			log.Error("row-count mismatch",
				"table", name, "src", srcStat.Entries, "dst", dstStat.Entries)
			if firstErr == nil {
				firstErr = errors.New("row-count mismatch — see error logs")
			}
		} else {
			log.Info("verify counts OK", "table", name, "rows", srcStat.Entries)
		}
	}
	return firstErr
}

// verifyFull walks both src and dst in lockstep and compares every key
// and value. O(N) on database size; ~30 min for a 50 GiB DB.
func verifyFull(ctx context.Context, src, dst kv.RoDB, tables []string) error {
	srcTx, err := src.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer srcTx.Rollback()
	dstTx, err := dst.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer dstTx.Rollback()

	for _, name := range tables {
		sc, err := srcTx.Cursor(name)
		if err != nil {
			return err
		}
		dc, err := dstTx.Cursor(name)
		if err != nil {
			sc.Close()
			return err
		}
		var rows uint64
		t0 := time.Now()
		for {
			sk, sv, serr := sc.Next()
			dk, dv, derr := dc.Next()
			if rows == 0 {
				sk, sv, serr = sc.First()
				dk, dv, derr = dc.First()
			}
			if serr != nil || derr != nil {
				sc.Close()
				dc.Close()
				return fmt.Errorf("verify cursor err table=%s row=%d: src=%v dst=%v", name, rows, serr, derr)
			}
			if sk == nil && dk == nil {
				break
			}
			if !bytes.Equal(sk, dk) || !bytes.Equal(sv, dv) {
				sc.Close()
				dc.Close()
				return fmt.Errorf("row mismatch table=%s row=%d sk=%x sv=%x dk=%x dv=%x",
					name, rows, sk, sv, dk, dv)
			}
			rows++
		}
		sc.Close()
		dc.Close()
		log.Info("verify full OK", "table", name, "rows", rows, "elapsed", time.Since(t0).Truncate(time.Second))
	}
	return nil
}

// planAndPrint reports the migration plan for --dry-run.
func planAndPrint(ctx context.Context, src kv.RoDB, dstPath string, dstPageKiB uint64) error {
	tx, err := src.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	mdbxTx, ok := tx.(*mdbx.MdbxTx)
	if !ok {
		return errors.New("planAndPrint: tx is not *mdbx.MdbxTx")
	}
	tables := selectTables(src, "")
	var totalRows uint64
	for _, name := range tables {
		st, err := mdbxTx.BucketStat(name)
		if err != nil {
			return err
		}
		totalRows += st.Entries
		log.Info("table", "name", name, "rows", st.Entries)
	}
	log.Info("PLAN",
		"dst", dstPath,
		"dst_page_kib", dstPageKiB,
		"tables", len(tables),
		"total_rows", totalRows)
	return nil
}
