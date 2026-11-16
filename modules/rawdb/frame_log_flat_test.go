// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"testing"
	"time"

	log2 "github.com/n42blockchain/N42/lib/log/v3"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/modules"
)

// frameIndexRows counts the frame hash rows of the index (without the
// position row).
func frameIndexRows(t testing.TB, db kv.RwDB) int {
	t.Helper()
	n := 0
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		return tx.ForEach(modules.FrameLogIndex, nil, func(k, v []byte) error {
			if len(k) == types.HashLength {
				n++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// With N42_FRAME_INDEX_FLAT and a segment retention the index rows of the
// dropped segments are deleted, the retained frames read back, the dropped
// ones read as not held (as without the switch), a frame written again into
// a newer segment keeps its row, and a reopen still recovers.
func TestFrameIndexFlatPrunesDroppedSegments(t *testing.T) {
	prev := frameLogSegmentBytes
	frameLogSegmentBytes = 2000
	defer func() { frameLogSegmentBytes = prev }()
	rows := map[bool]int{}
	for _, flat := range []bool{false, true} {
		restoreFlat := SetFrameIndexFlatForTest(flat)
		db := newFrameLogTestStore(t)
		restore := SetFrameStore(db)
		dir := t.TempDir()
		closeLog, err := OpenFrameLog(dir, 2)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 40; i++ {
			writeLogFrames(t, i, i+1) // ~5 a segment
		}
		// Frame 0 again, now in the newest segment: its row must survive
		// the prune of segment 0.
		writeLogFrames(t, 0, 1)
		for i := 40; i < 45; i++ {
			writeLogFrames(t, i, i+1)
		}
		settleFrameLog(t)
		mustReadFrames(t, db, 0, 1, true)
		mustReadFrames(t, db, 1, 30, false)
		mustReadFrames(t, db, 43, 45, true)
		rows[flat] = frameIndexRows(t, db)
		closeLog()
		closeLog, err = OpenFrameLog(dir, 2)
		if err != nil {
			t.Fatal(err)
		}
		writeLogFrames(t, 45, 47)
		mustReadFrames(t, db, 43, 47, true)
		mustReadFrames(t, db, 0, 1, true)
		closeLog()
		restore()
		restoreFlat()
	}
	if rows[false] < 45 || rows[true] >= rows[false] || rows[true] > 20 {
		t.Fatalf("index rows: off %d, flat %d", rows[false], rows[true])
	}
}

// TestFrameIndexAgeBench (N42_FRAME_INDEX_BENCH=1, optional
// N42_FRAME_INDEX_BENCH_FRAMES) writes blocks of 652 frames through a frame
// log with a 3-segment retention, ~30 blocks a segment like the fleet's
// 1 GiB segments, and prints the index update per write after 1k, 100k and
// 2M frames, with N42_FRAME_INDEX_FLAT off and on.
func TestFrameIndexAgeBench(t *testing.T) {
	if os.Getenv("N42_FRAME_INDEX_BENCH") != "1" {
		t.Skip("N42_FRAME_INDEX_BENCH=1 runs it")
	}
	total := 2_000_000
	if v, err := strconv.Atoi(os.Getenv("N42_FRAME_INDEX_BENCH_FRAMES")); err == nil && v > 0 {
		total = v
	}
	const perBlock, dataLen, window = 652, 32, 30
	prev := frameLogSegmentBytes
	frameLogSegmentBytes = int64(30 * perBlock * (frameLogHeaderSize + dataLen))
	defer func() { frameLogSegmentBytes = prev }()
	for _, flat := range []bool{false, true} {
		restoreFlat := SetFrameIndexFlatForTest(flat)
		dir := t.TempDir()
		db := mdbx.NewMDBX(log2.New()).Path(dir+"/db").
			WithTableCfg(func(c kv.TableCfg) kv.TableCfg { return FrameLogIndexTables(FrameStoreTables(c)) }).MustOpen()
		restore := SetFrameStore(db)
		closeLog, err := OpenFrameLog(dir+"/log", 3)
		if err != nil {
			t.Fatal(err)
		}
		rng := rand.New(rand.NewSource(42))
		data := make([]byte, dataLen)
		hs := make([]types.Hash, perBlock)
		raws := make([][]byte, perBlock)
		for i := range raws {
			raws[i] = data
		}
		checkpoints := []int{1000, 100_000, total}
		written, ci := 0, 0
		var sum, put, prune, commit time.Duration
		measured := 0
		for ci < len(checkpoints) {
			for i := range hs {
				rng.Read(hs[i][:])
			}
			var times FrameWriteTimes
			if err := WriteFramesToStoreTimed(context.Background(), hs, raws, &times); err != nil {
				t.Fatal(err)
			}
			written += perBlock
			if written >= checkpoints[ci] {
				sum += times.Index
				put += times.IdxPut - times.IdxPrune
				prune += times.IdxPrune
				commit += times.IdxCommit
				if measured++; measured == window {
					n := time.Duration(window)
					fmt.Printf("flat=%v frames=%8d rows=%8d index/write %8.3f ms (put %.3f prune %.3f commit %.3f)\n",
						flat, written, frameIndexRows(t, db), float64(sum/n)/1e6, float64(put/n)/1e6, float64(prune/n)/1e6, float64(commit/n)/1e6)
					sum, put, prune, commit, measured = 0, 0, 0, 0, 0
					ci++
				}
			}
		}
		closeLog()
		restore()
		db.Close()
		restoreFlat()
	}
}
