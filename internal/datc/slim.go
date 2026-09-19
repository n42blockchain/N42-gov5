// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// slim.go — what an exact-ladder archive no longer needs.
//
// An archive directory serves two masters. The BUILD resumes from mdbx.dat's
// current-state tables (Hashed*/TrieOf*, hundreds of GB) and never reads the
// segments back; the READER needs the segments and, of mdbx.dat, only the few
// rows of DatcMeta. Once the storage tries read from ns (exactladder.go) and
// the account trie from its per-block level (accExactSlotsAt), the v2 storage
// node records in MDBX and the change-index segments are dead weight too.
//
//	slim          clears the dead MDBX tables of the build archive in place
//	              (the file shrinks only through a compacting copy: mdbx_copy -c)
//	serving-copy  makes a reader-only archive: the segments a reader uses,
//	              hard-linked (no bytes copied on the same filesystem), plus an
//	              MDBX holding DatcMeta alone — what a node that only serves
//	              eth_getProof needs, without the build state
package datc

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// deadTables are the MDBX tables no reader or builder of an exact-ladder
// archive uses: the v2 storage epoch records and the in-MDBX twins of tables
// that live in segments (empty in a --leaf-seg build anyway).
var deadTables = []string{tDatcStoNode, tDatcStoChg}

// readerShape reports how the archive is read: through the exact storage
// ladder (ns.ladders present) and through the exact account ladder.
func readerShape(dir string) (stoExact, accExact bool, err error) {
	db, err := openArchiveDB(dir, 2048)
	if err != nil {
		return false, false, err
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()
	q, _, err := loadQuerierCache(tx, dir, 0, 8)
	if err != nil {
		return false, false, err
	}
	defer q.Close()
	return q.exact != nil, q.accExact, nil
}

func runSlim(args []string) {
	fs := flag.NewFlagSet("slim", flag.ExitOnError)
	out := fs.String("out", "", "the build archive (its mdbx.dat is modified in place: back it up first, e.g. cp --reflink)")
	mapGB := fs.Int("map.gb", 4096, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}
	stoExact, _, err := readerShape(*out)
	if err != nil {
		die("%v", err)
	}
	if !stoExact {
		die("%s has no exact storage ladder (leafseg/ns.ladders): its storage proofs still read DatcStorNode", *out)
	}
	modulesInit()
	db, err := openDatcDB(log.New(), *out, *mapGB, 1)
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	for _, t := range deadTables {
		var before uint64
		_ = db.View(context.Background(), func(tx kv.Tx) error { before, _ = tx.BucketSize(t); return nil })
		if before == 0 {
			fmt.Printf("slim: %-14s already empty\n", t)
			continue
		}
		if err := db.Update(context.Background(), func(tx kv.RwTx) error { return tx.ClearBucket(t) }); err != nil {
			die("clear %s: %v", t, err)
		}
		fmt.Printf("slim: %-14s cleared, %.1f GB of pages returned to the free list\n", t, float64(before)/1e9)
	}
	fmt.Println("slim: the file keeps its size; the freed pages are reused by later builds.\n" +
		"      To shrink it: mdbx_copy -c <archive> <new dir>, check the copy, then swap mdbx.dat.")
}

func runServingCopy(args []string) {
	fs := flag.NewFlagSet("serving-copy", flag.ExitOnError)
	out := fs.String("out", "", "the archive to copy from")
	dst := fs.String("dst", "", "the reader-only archive to create (must not exist)")
	mode := fs.String("link", "hard", "how segments get there: hard (same filesystem, no bytes copied) | copy")
	_ = fs.Parse(args)
	if *out == "" || *dst == "" {
		die("--out and --dst required")
	}
	n, bytes, err := servingCopy(*out, *dst, *mode == "copy")
	if err != nil {
		die("serving-copy: %v", err)
	}
	fmt.Printf("serving-copy: %s — %d segment files, %.1f GB, plus a DatcMeta-only mdbx.dat\n", *dst, n, float64(bytes)/1e9)
}

// servingCopy builds the reader-only archive and returns the number and size
// of the segment files in it.
func servingCopy(out, dst string, copyBytes bool) (int, int64, error) {
	if _, err := os.Stat(dst); err == nil {
		return 0, 0, fmt.Errorf("%s already exists", dst)
	}
	stoExact, accExact, err := readerShape(out)
	if err != nil {
		return 0, 0, err
	}
	// The change index is read only by the epoch-record paths.
	skip := map[string]bool{}
	if stoExact {
		skip["cs"] = true
	}
	if accExact {
		skip["ca"] = true
	}
	if err := os.MkdirAll(filepath.Join(dst, leafSegDir), 0o755); err != nil {
		return 0, 0, err
	}
	names, err := filepath.Glob(filepath.Join(out, leafSegDir, "*"))
	if err != nil {
		return 0, 0, err
	}
	var n int
	var total int64
	for _, src := range names {
		base := filepath.Base(src)
		st, err := os.Stat(src)
		if err != nil || st.IsDir() || strings.HasSuffix(base, ".tmp") {
			continue
		}
		if i := strings.IndexByte(base, '.'); i > 0 && skip[base[:i]] && strings.HasSuffix(base, ".seg") {
			continue
		}
		to := filepath.Join(dst, leafSegDir, base)
		if copyBytes {
			err = copyFile(src, to)
		} else {
			err = os.Link(src, to)
		}
		if err != nil {
			return 0, 0, fmt.Errorf("%s: %w (across filesystems use --link copy)", base, err)
		}
		n++
		total += st.Size()
	}

	// The reader's MDBX: DatcMeta (head, ladders, format, depths) and the
	// per-contract depth rows of format 3; nothing else is ever read.
	srcDB, err := openArchiveDB(out, 2048)
	if err != nil {
		return 0, 0, err
	}
	defer srcDB.Close()
	modulesInit()
	dstDB, err := openDatcDB(log.New(), dst, 1, 1)
	if err != nil {
		return 0, 0, err
	}
	defer dstDB.Close()
	err = srcDB.View(context.Background(), func(stx kv.Tx) error {
		return dstDB.Update(context.Background(), func(dtx kv.RwTx) error {
			for _, table := range []string{tDatcMeta, tDatcStoDepth} {
				c, err := stx.Cursor(table)
				if err != nil {
					continue // a table an older archive does not have
				}
				for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
					if err != nil {
						c.Close()
						return err
					}
					if err := dtx.Put(table, k, v); err != nil {
						c.Close()
						return err
					}
				}
				c.Close()
			}
			return nil
		})
	})
	return n, total, err
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	outF, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(outF, in); err != nil {
		outF.Close()
		return err
	}
	return outF.Close()
}
