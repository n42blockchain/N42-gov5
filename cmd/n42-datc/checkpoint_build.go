// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// n42-datc checkpoint-build — materializes the live-key-set at a set of early
// checkpoint blocks, from the existing leaf segments (no rebuild). At an early
// block the state is tiny (block 100K ≈ 4K keys) but the tree is shallow, so a
// fold there scans ~100M future keys under a shallow prefix. A checkpoint lets
// the fold read only the keys that EXIST at N (checkpoint ∪ bounded delta),
// killing the minutes-long early-block folds.
//
// Output: <out>/ckpt/<table>.<block>.ckpt — a zstd-compressed, key-sorted list
// of the keys live at that block (floor version ≤ block, non-deleted). Values
// are read from the leaf segments at query time (floor ≤ N), so the checkpoint
// stores keys only. Keys are emitted in scan (key) order = already sorted.

package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/c2h5oh/datasize"
	"github.com/klauspost/compress/zstd"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

const ckptDir = "ckpt"

func runCheckpointBuild(args []string) {
	fs := flag.NewFlagSet("checkpoint-build", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (leafseg/)")
	table := fs.Int("table", -1, "0=leafA(account) 1=leafS(storage); -1 = both")
	blocksCSV := fs.String("blocks", "100000,250000,500000,1000000,1500000,2000000,3000000,4000000", "comma-separated checkpoint block heights")
	keyLenFlag := fs.Int("keylen", 0, "key length before the 8-byte block suffix (0=auto)")
	mapGB := fs.Int("map.gb", 512, "MDBX map GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}

	var blocks []uint64
	for _, s := range strings.Split(*blocksCSV, ",") {
		v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
		if err != nil {
			die("bad --blocks entry %q: %v", s, err)
		}
		blocks = append(blocks, v)
	}

	tables := []int{segTabLeafA, segTabLeafS}
	if *table >= 0 {
		tables = []int{*table}
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()

	if err := os.MkdirAll(filepath.Join(*out, ckptDir), 0o755); err != nil {
		die("mkdir ckpt: %v", err)
	}

	for _, tab := range tables {
		keyLen := *keyLenFlag
		if keyLen == 0 {
			if tab == segTabLeafA {
				keyLen = 32
			} else {
				keyLen = 72
			}
		}
		buildCheckpointsForTable(*out, tab, keyLen, blocks)
	}
}

// buildCheckpointsForTable scans the leaf segments once and, for each checkpoint
// block, writes the keys live at that block. A key is live at C iff its floor
// version ≤ C has a non-empty (non-deleted) value.
func buildCheckpointsForTable(outDir string, tab, keyLen int, blocks []uint64) {
	cache := newFrameLRU()
	seg, ok, err := openLeafSegSet(outDir, tab, cache)
	if err != nil || !ok {
		die("open leafseg table %d: %v (ok=%v) — finalize-leaves first?", tab, err, ok)
	}
	c := seg.Cursor()

	// One output writer per checkpoint block.
	type wtr struct {
		f   *os.File
		bw  *bufio.Writer
		enc *zstd.Encoder
		n   int64
	}
	wtrs := make([]*wtr, len(blocks))
	for i, b := range blocks {
		path := filepath.Join(outDir, ckptDir, fmt.Sprintf("%d.%d.ckpt", tab, b))
		f, err := os.Create(path)
		if err != nil {
			die("create %s: %v", path, err)
		}
		bw := bufio.NewWriterSize(f, 1<<20)
		enc, err := zstd.NewWriter(bw, zstd.WithEncoderLevel(zstd.SpeedDefault))
		if err != nil {
			die("zstd writer: %v", err)
		}
		wtrs[i] = &wtr{f: f, bw: bw, enc: enc}
	}

	// Per-key state: EVER-APPEARED semantics — a key belongs to checkpoint C iff
	// its FIRST version block ≤ C (created by C), regardless of later deletion.
	// This makes the checkpoint at the smallest C ≥ N a COMPLETE candidate set
	// for "keys existing at N" (a key live at N was created ≤ N ≤ C); the query
	// reads each candidate's floor ≤ N and drops empties. Live-at-C would miss
	// keys created-then-deleted inside a checkpoint interval but live at N.
	var curKey []byte
	var curFirstBlk uint64
	var scanned int64
	flushKey := func() {
		if curKey == nil {
			return
		}
		for i, cb := range blocks {
			if curFirstBlk <= cb {
				if _, err := wtrs[i].enc.Write(curKey); err != nil {
					die("write ckpt: %v", err)
				}
				wtrs[i].n++
			}
		}
	}

	k, v, err := c.Seek(nil)
	for k != nil && err == nil {
		scanned++
		if len(k) < keyLen+8 {
			k, v, err = c.Next()
			continue
		}
		key := k[:keyLen]
		blk := binary.BigEndian.Uint64(k[keyLen : keyLen+8])
		if curKey == nil || string(key) != string(curKey) {
			flushKey()
			curKey = append(curKey[:0], key...)
			curFirstBlk = blk // first entry of a key = its min block (sorted key,block)
		}
		_ = v
		k, v, err = c.Next()
	}
	flushKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan error (partial): %v\n", err)
	}

	for i, b := range blocks {
		if e := wtrs[i].enc.Close(); e != nil {
			die("close enc: %v", e)
		}
		if e := wtrs[i].bw.Flush(); e != nil {
			die("flush: %v", e)
		}
		fi, _ := wtrs[i].f.Stat()
		wtrs[i].f.Close()
		var szMB float64
		if fi != nil {
			szMB = float64(fi.Size()) / (1 << 20)
		}
		fmt.Printf("table=%d ckpt block=%-9d live_keys=%-12d file=%.1f MB\n", tab, b, wtrs[i].n, szMB)
	}
	fmt.Printf("table=%d: scanned %d leaf entries\n", tab, scanned)
}
