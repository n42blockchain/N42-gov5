package internal

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// e1FramesAheadOn is N42_E1_LIGHT_FRAMES_AHEAD=1 (default off). A light
// node's import wrote each block's frames (~20 MB at 173k transfers) to
// the frame store in its own durable transaction inside the block's write:
// ~70-90 ms a block, longer than the block interval at 2x blocks, so the
// lag queue ran 15-30 blocks (1-2 s) behind and frames in those blocks
// held the frame store budget (QS run 45). With the flag, one writer
// goroutine stores the frames of every queued block ahead of its import,
// several blocks per transaction, and the import only waits for its own
// block's frames before writing the block (WriteBlockFramesStored). A
// block is still written only after its frames are durable.
var e1FramesAheadOn = envswitch.Is1("N42_E1_LIGHT_FRAMES_AHEAD", envswitch.Perf, "A light node's import writes each block's frames ahead of the block.")

// SetE1FramesAheadForTest overrides N42_E1_LIGHT_FRAMES_AHEAD.
func SetE1FramesAheadForTest(on bool) func() {
	prev := e1FramesAheadOn
	e1FramesAheadOn = on
	return func() { e1FramesAheadOn = prev }
}

// e1FramesAheadMaxBlocks bounds the blocks one ahead transaction stores.
const e1FramesAheadMaxBlocks = 8

// e1AheadWrite is one block's frames on the ahead writer.
type e1AheadWrite struct {
	n      uint64
	hashes []types.Hash
	raws   [][]byte
	done   chan struct{}
	err    error
}

type e1FramesAhead struct {
	mu      sync.Mutex
	entries map[types.Hash]*e1AheadWrite
	work    []*e1AheadWrite
	kick    chan struct{}
	once    sync.Once

	batches, blocks atomic.Uint64
	lastLog         atomic.Int64
}

// queue hands every queued block above committedNum whose frames are in
// memory to the writer, once each, and forgets entries at or below it.
func (a *e1FramesAhead) queue(bc *BlockChain, list []*lagQueued, committedNum uint64) {
	if rawdb.FrameStore() == nil {
		return
	}
	a.once.Do(func() {
		a.entries = make(map[types.Hash]*e1AheadWrite)
		a.kick = make(chan struct{}, 1)
		ctx := bc.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		go a.loop(ctx)
	})
	added := false
	a.mu.Lock()
	for h, e := range a.entries {
		if e.n <= committedNum {
			delete(a.entries, h)
		}
	}
	for _, q := range list {
		b := q.blk
		n := b.Number64().Uint64()
		if n <= committedNum {
			continue
		}
		if _, ok := a.entries[b.Hash()]; ok {
			continue
		}
		body, ok := b.Body().(*block.Body)
		if !ok || !body.IsFrameBody() || len(body.FrameData) != len(body.FrameHashes) || len(body.FrameData) == 0 {
			continue
		}
		e := &e1AheadWrite{n: n, hashes: body.FrameHashes, raws: body.FrameData, done: make(chan struct{})}
		a.entries[b.Hash()] = e
		a.work = append(a.work, e)
		added = true
	}
	a.mu.Unlock()
	if added {
		select {
		case a.kick <- struct{}{}:
		default:
		}
	}
}

// take returns hash's ahead write and forgets it, or nil.
func (a *e1FramesAhead) take(hash types.Hash) *e1AheadWrite {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.entries[hash]
	if e != nil {
		delete(a.entries, hash)
	}
	return e
}

func (a *e1FramesAhead) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			a.failAll(ctx.Err())
			return
		case <-a.kick:
		}
		for {
			a.mu.Lock()
			k := len(a.work)
			if k > e1FramesAheadMaxBlocks {
				k = e1FramesAheadMaxBlocks
			}
			batch := append([]*e1AheadWrite(nil), a.work[:k]...)
			a.work = a.work[k:]
			a.mu.Unlock()
			if len(batch) == 0 {
				break
			}
			a.write(ctx, batch)
		}
	}
}

func (a *e1FramesAhead) write(ctx context.Context, batch []*e1AheadWrite) {
	t0 := time.Now()
	var hashes []types.Hash
	var raws [][]byte
	for _, e := range batch {
		hashes = append(hashes, e.hashes...)
		raws = append(raws, e.raws...)
	}
	err := rawdb.WriteFramesToStore(ctx, hashes, raws)
	for _, e := range batch {
		e.err = err
		close(e.done)
	}
	a.batches.Add(1)
	a.blocks.Add(uint64(len(batch)))
	if err != nil {
		log.Warn("e1 light: ahead frames write failed; the imports write them", "blocks", len(batch), "err", err)
		return
	}
	if now := time.Now().UnixNano(); now-a.lastLog.Load() > int64(10*time.Second) {
		a.lastLog.Store(now)
		log.Info("e1 light: frames written ahead", "blocks", len(batch), "frames", len(hashes),
			"tookUs", time.Since(t0).Microseconds(), "batches", a.batches.Load(), "totalBlocks", a.blocks.Load())
	}
}

func (a *e1FramesAhead) failAll(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.work {
		e.err = err
		close(e.done)
	}
	a.work = nil
}
