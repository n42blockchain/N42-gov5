package internal

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// N42_FRAMES_WRITE_EARLY (default off; "1", "true" or "on" enables). The
// ordered writer waits for the block's frames write (~34 MB, 50-85 ms) before
// it commits, and that write only started at the hand-off. With the switch
// on, the write starts when the block is queued for the lag executor with
// its frames held, and the hand-off takes that write instead of starting a
// second one; a block with no early write behaves as before.
//
// The frame store is content-addressed (rows are keyed by frame hash), so
// the frames of a block that is never committed (refused later, replaced by
// a sibling, imported again) are harmless leftovers, the same ones a failed
// hand-off leaves today. Concurrent writes for different blocks are safe:
// the frame log serializes them under its mutex (modules/rawdb/frame_log.go
// write), the mdbx store under its single writer and the prune window under
// framePruneMu.

var framesWriteEarlyOn = envswitch.OptIn("N42_FRAMES_WRITE_EARLY", envswitch.Perf, "Opt-in: write a block's frames early.")

// SetFramesWriteEarlyForTest switches N42_FRAMES_WRITE_EARLY and returns the
// restore.
func SetFramesWriteEarlyForTest(on bool) func() {
	prev := framesWriteEarlyOn
	framesWriteEarlyOn = on
	return func() { framesWriteEarlyOn = prev }
}

// framesWriteFn writes the frames to the store (tests count and fail it).
var framesWriteFn = rawdb.WriteFramesToStore

// framesEarlyStarted and framesEarlyReused count the early writes started
// and the ones a hand-off took (since start).
var framesEarlyStarted, framesEarlyReused atomic.Uint64

// framesEarlyCap bounds the remembered writes.
const framesEarlyCap = 16

// framesEarlyMap remembers the frames writes started early, by block hash,
// FIFO-bounded. Evicting never cancels a write: the hand-off of an evicted
// block starts its own.
type framesEarlyMap struct {
	mu    sync.Mutex
	m     map[types.Hash]*framesWrite
	order []types.Hash
}

// onEnqueue is the queue's hook: with N42_FRAMES_WRITE_EARLY on, the frames
// go to the store now, not at the hand-off.
func (e *framesEarlyMap) onEnqueue(b *block.Block) {
	if framesWriteEarlyOn {
		e.start(b)
	}
}

// start begins b's frames write unless one is remembered already or b has
// no frames to write.
func (e *framesEarlyMap) start(b *block.Block) {
	if b == nil {
		return
	}
	h := b.Hash()
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.m[h]; ok {
		return
	}
	fw := startFramesWrite(b)
	if fw == nil {
		return
	}
	fw.early = true
	framesEarlyStarted.Add(1)
	if e.m == nil {
		e.m = make(map[types.Hash]*framesWrite, framesEarlyCap)
	}
	e.m[h] = fw
	e.order = append(e.order, h)
	for len(e.order) > framesEarlyCap {
		delete(e.m, e.order[0])
		e.order = e.order[1:]
	}
}

// take removes and returns b's remembered write, or nil.
func (e *framesEarlyMap) take(b *block.Block) *framesWrite {
	if b == nil {
		return nil
	}
	h := b.Hash()
	e.mu.Lock()
	defer e.mu.Unlock()
	fw, ok := e.m[h]
	if !ok {
		return nil
	}
	delete(e.m, h)
	for i, o := range e.order {
		if o == h {
			e.order = append(e.order[:i], e.order[i+1:]...)
			break
		}
	}
	framesEarlyReused.Add(1)
	return fw
}

// tsMs is t as wall clock milliseconds, 0 for the zero time.
func tsMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
