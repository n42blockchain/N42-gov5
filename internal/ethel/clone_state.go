// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// clone_state.go — fast one-time copy of a PlainState datadir into a
// fresh MDBX. Used to escape a stuck MapSize cap without rerunning the
// ~1h+ changeset replay.
//
// Background: MDBX geometry (MapSize) is fixed at env creation via
// SetGeometry, and Accede inherits whatever value is baked into the
// existing file. If a datadir was first created with a small MapSize
// (e.g. verify-cs-root uses 64 GB), every later rebuild-state / ethexec
// run on that datadir stays capped at 64 GB regardless of the code's
// MapSize(2*TB) line. The cap only takes effect via SetGeometry on a
// new file.
//
// CloneState copies Account + Storage + Code + DbInfo from a source
// datadir into a freshly-created target datadir (2 TB MapSize). The
// target ends up with the same PlainState progress marker, so a
// subsequent `ethexec rebuild-state --persist-trie` auto-resumes with
// zero replay and goes straight into BootstrapHPHBatched.

package ethel

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// CloneStateOptions configures the clone run.
type CloneStateOptions struct {
	// BatchN is the number of puts per target-tx commit. Defaults to 500k.
	BatchN int
}

// CloneState copies the PlainState-relevant tables (Account, Storage,
// Code) and the progress marker (DbInfo/ethel_progress) from src to dst.
// Neither db is opened here; caller controls lifecycle, MapSize, etc.
// dst must be writable; src can be read-only.
func CloneState(ctx context.Context, src kv.RoDB, dst kv.RwDB, opts CloneStateOptions) error {
	batchN := opts.BatchN
	if batchN <= 0 {
		batchN = 500_000
	}
	t0 := time.Now()

	for _, tbl := range []string{modules.Account, modules.Storage, modules.Code} {
		count, err := copyTableBatched(ctx, src, dst, tbl, batchN)
		if err != nil {
			return fmt.Errorf("copy %s: %w", tbl, err)
		}
		log.Info("CloneState: table copied",
			"table", tbl, "entries", count,
			"elapsed", time.Since(t0).Truncate(time.Second))
	}

	if err := copyDbInfo(ctx, src, dst); err != nil {
		return fmt.Errorf("copy DbInfo: %w", err)
	}

	log.Info("CloneState: complete", "elapsed", time.Since(t0).Truncate(time.Second))
	return nil
}

// copyTableBatched streams all rows of a table from src to dst,
// committing the target every batchN puts. Source iteration uses a
// fresh RoTx per batch (so MDBX can advance its MVCC snapshot; the
// source is immutable during clone so different snapshots see the
// same content).
func copyTableBatched(ctx context.Context, src kv.RoDB, dst kv.RwDB, table string, batchN int) (int, error) {
	var lastKey []byte
	total := 0
	t0 := time.Now()
	for {
		srcTx, err := src.BeginRo(ctx)
		if err != nil {
			return total, err
		}
		srcCur, err := srcTx.Cursor(table)
		if err != nil {
			srcTx.Rollback()
			return total, err
		}

		dstTx, err := dst.BeginRw(ctx)
		if err != nil {
			srcCur.Close()
			srcTx.Rollback()
			return total, err
		}

		var k, v []byte
		if lastKey == nil {
			k, v, err = srcCur.First()
		} else {
			k, v, err = srcCur.Seek(lastKey)
			if err == nil && k != nil && bytes.Equal(k, lastKey) {
				k, v, err = srcCur.Next()
			}
		}
		if err != nil {
			srcCur.Close()
			srcTx.Rollback()
			dstTx.Rollback()
			return total, err
		}

		count := 0
		for ; k != nil && count < batchN; k, v, err = srcCur.Next() {
			if err != nil {
				srcCur.Close()
				srcTx.Rollback()
				dstTx.Rollback()
				return total, err
			}
			if err := dstTx.Put(table, k, v); err != nil {
				srcCur.Close()
				srcTx.Rollback()
				dstTx.Rollback()
				return total, err
			}
			lastKey = append(lastKey[:0], k...)
			count++
		}

		srcCur.Close()
		srcTx.Rollback() // source is read-only

		if err := dstTx.Commit(); err != nil {
			return total, fmt.Errorf("dst commit: %w", err)
		}

		total += count
		log.Info("copyTableBatched progress",
			"table", table, "entries", total, "batch_size", count,
			"elapsed", time.Since(t0).Truncate(time.Second))
		if count < batchN {
			break
		}
	}
	return total, nil
}

// copyDbInfo transfers DbInfo entries (including ethel_progress) so the
// target datadir can auto-resume. DbInfo is tiny — single tx is fine.
func copyDbInfo(ctx context.Context, src kv.RoDB, dst kv.RwDB) error {
	srcTx, err := src.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer srcTx.Rollback()

	dstTx, err := dst.BeginRw(ctx)
	if err != nil {
		return err
	}
	defer dstTx.Rollback()

	srcCur, err := srcTx.Cursor("DbInfo")
	if err != nil {
		return err
	}
	defer srcCur.Close()

	copied := 0
	for k, v, err := srcCur.First(); k != nil; k, v, err = srcCur.Next() {
		if err != nil {
			return err
		}
		if err := dstTx.Put("DbInfo", k, v); err != nil {
			return err
		}
		copied++
	}
	if err := dstTx.Commit(); err != nil {
		return err
	}
	log.Info("CloneState: DbInfo copied", "entries", copied)
	return nil
}
