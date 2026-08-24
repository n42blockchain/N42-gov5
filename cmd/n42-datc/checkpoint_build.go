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
// Output: <out>/ckpt/<table>.<block>.ckpt — a FRAMED zstd file (v2) of the
// key-sorted live-key-set at that block (floor version ≤ block, non-deleted).
// Values are read from the leaf segments at query time (floor ≤ N), so the
// checkpoint stores keys only. Keys are emitted in scan (key) order = sorted.
//
// v2 framing (mirrors the leafseg layout): the reader must NOT have to
// materialize the whole set — the largest storage checkpoint is ~68 GB raw
// (947M × 72B keys), and the v1 single-stream format forced ReadFile +
// DecodeAll of exactly that, which OOMed the 128 GB build host. Frames are
// ~256 KiB raw each; a footer indexes (compLen, rows, firstKey) per frame so
// a prefix query touches only the bracketing frames.
//
//	[8B magic] [frame 0: zstd(keys)] ... [footer] [8B BE footLen] [8B magic]
//	footer: uvarint keyLen, uvarint frameCount,
//	        per frame: uvarint compLen, uvarint rows, firstKey[keyLen]

package main

import (
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

const (
	ckptDir      = "ckpt"
	ckptMagic    = "DATCCK2\n" // header AND trailing magic of the v2 framed format
	ckptFrameRaw = 256 << 10   // target uncompressed bytes per frame
)

// ckptWriter streams one checkpoint file in the v2 framed format.
type ckptWriter struct {
	f       *os.File
	enc     *zstd.Encoder // shared, EncodeAll-only
	keyLen  int
	buf     []byte // pending raw keys for the current frame
	scratch []byte // reused compression output buffer
	foot    []byte // accumulated per-frame footer entries
	frames  uint64
	n       int64 // total keys written
}

func newCkptWriter(path string, keyLen int, enc *zstd.Encoder) (*ckptWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write([]byte(ckptMagic)); err != nil {
		f.Close()
		return nil, err
	}
	return &ckptWriter{f: f, enc: enc, keyLen: keyLen}, nil
}

func (w *ckptWriter) add(key []byte) error {
	w.buf = append(w.buf, key...)
	w.n++
	if len(w.buf) >= ckptFrameRaw {
		return w.flushFrame()
	}
	return nil
}

func (w *ckptWriter) flushFrame() error {
	if len(w.buf) == 0 {
		return nil
	}
	comp := w.enc.EncodeAll(w.buf, w.scratch[:0])
	if _, err := w.f.Write(comp); err != nil {
		return err
	}
	var tmp [binary.MaxVarintLen64]byte
	w.foot = append(w.foot, tmp[:binary.PutUvarint(tmp[:], uint64(len(comp)))]...)
	w.foot = append(w.foot, tmp[:binary.PutUvarint(tmp[:], uint64(len(w.buf)/w.keyLen))]...)
	w.foot = append(w.foot, w.buf[:w.keyLen]...)
	w.frames++
	w.scratch = comp
	w.buf = w.buf[:0]
	return nil
}

// close flushes the last frame, writes footer + tail, and returns the file size.
func (w *ckptWriter) close() (int64, error) {
	if err := w.flushFrame(); err != nil {
		return 0, err
	}
	var tmp [binary.MaxVarintLen64]byte
	foot := append([]byte{}, tmp[:binary.PutUvarint(tmp[:], uint64(w.keyLen))]...)
	foot = append(foot, tmp[:binary.PutUvarint(tmp[:], w.frames)]...)
	foot = append(foot, w.foot...)
	if _, err := w.f.Write(foot); err != nil {
		return 0, err
	}
	var tail [16]byte
	binary.BigEndian.PutUint64(tail[:8], uint64(len(foot)))
	copy(tail[8:], ckptMagic)
	if _, err := w.f.Write(tail[:]); err != nil {
		return 0, err
	}
	fi, serr := w.f.Stat()
	var sz int64
	if fi != nil {
		sz = fi.Size()
	}
	if cerr := w.f.Close(); cerr != nil {
		return sz, cerr
	}
	return sz, serr
}

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

	// One output writer per checkpoint block; the frame encoder is shared.
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		die("zstd writer: %v", err)
	}
	defer enc.Close()
	wtrs := make([]*ckptWriter, len(blocks))
	for i, b := range blocks {
		path := filepath.Join(outDir, ckptDir, fmt.Sprintf("%d.%d.ckpt", tab, b))
		w, err := newCkptWriter(path, keyLen, enc)
		if err != nil {
			die("create %s: %v", path, err)
		}
		wtrs[i] = w
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
				if err := wtrs[i].add(curKey); err != nil {
					die("write ckpt: %v", err)
				}
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
		sz, e := wtrs[i].close()
		if e != nil {
			die("close ckpt: %v", e)
		}
		fmt.Printf("table=%d ckpt block=%-9d live_keys=%-12d frames=%-8d file=%.1f MB\n",
			tab, b, wtrs[i].n, wtrs[i].frames, float64(sz)/(1<<20))
	}
	fmt.Printf("table=%d: scanned %d leaf entries\n", tab, scanned)
}
