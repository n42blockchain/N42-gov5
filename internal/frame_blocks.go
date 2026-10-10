// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Frame-referenced blocks (docs/QS_MULTI_MILLION_DESIGN.md, P1b).
//
// With N42_FRAME_BLOCKS=1 the leader fills blocks with whole gateway frames
// from the frame store and the body carries only their hashes. Every node
// resolves the hashes to frames (frame store, then the Frames table, then a
// FRQ1 pull from peers), decodes them into the block's transaction list and
// executes it under the skip rule (frame_exec.go). Blocks without frame
// hashes are untouched by anything here.

// EnvFrameBlocks turns frame-referenced block production on (leader side).
const EnvFrameBlocks = "N42_FRAME_BLOCKS"

// EnvFramePullTimeout bounds the FRQ1 pull for missing frames (ms).
const EnvFramePullTimeout = "N42_FRAME_PULL_TIMEOUT_MS"

// ErrFramesUnavailable: a frame body names a frame this node holds nowhere
// and could not pull. The block is not imported, so it is not voted for.
var ErrFramesUnavailable = errors.New("frame body: frames unavailable")

var (
	frameBlocksOnce sync.Once
	frameBlocksOn   atomic.Bool
)

// FrameBlocksEnabled reports N42_FRAME_BLOCKS=1 (read once).
func FrameBlocksEnabled() bool {
	frameBlocksOnce.Do(func() { frameBlocksOn.Store(os.Getenv(EnvFrameBlocks) == "1") })
	return frameBlocksOn.Load()
}

// SetFrameBlocksForTest overrides N42_FRAME_BLOCKS for a test and returns
// the restore function.
func SetFrameBlocksForTest(on bool) func() {
	prev := FrameBlocksEnabled()
	frameBlocksOn.Store(on)
	return func() { frameBlocksOn.Store(prev) }
}

// FramePuller fetches frames by hash from peers. It returns what it got,
// each frame already admitted (gateway signature verified, hash matched).
type FramePuller func(ctx context.Context, hashes []types.Hash) map[types.Hash][]byte

type frameBlockState struct {
	store  *store.Store
	puller FramePuller

	// resolveMu serialises body resolution: a pushed block can reach the
	// deferred check and the import on two goroutines.
	resolveMu sync.Mutex

	// inFlight: frames named by blocks built or resolved here and not yet
	// written (FrameHash -> block number). The builder skips them, so a
	// chained build on an unwritten ancestor does not include a frame twice.
	mu       sync.Mutex
	inFlight map[types.Hash]uint64

	loggedMu sync.Mutex
	logged   map[types.Hash]struct{}
}

// SetFrameStore attaches the node's frame store (nil = none).
func (bc *BlockChain) SetFrameStore(st *store.Store) { bc.frames.store = st }

// FrameStore returns the attached frame store, or nil.
func (bc *BlockChain) FrameStore() *store.Store { return bc.frames.store }

// SetFramePuller attaches the peer pull used for frames missing locally.
func (bc *BlockChain) SetFramePuller(p FramePuller) { bc.frames.puller = p }

// MarkFramesInFlight records frames used by an unwritten block at number.
func (bc *BlockChain) MarkFramesInFlight(hashes []types.Hash, number uint64) {
	fs := &bc.frames
	fs.mu.Lock()
	if fs.inFlight == nil {
		fs.inFlight = make(map[types.Hash]uint64, len(hashes))
	}
	for _, h := range hashes {
		fs.inFlight[h] = number
	}
	fs.mu.Unlock()
}

// FramesInFlight returns a snapshot predicate over the in-flight set.
func (bc *BlockChain) FramesInFlight() func(types.Hash) bool {
	fs := &bc.frames
	fs.mu.Lock()
	snap := make(map[types.Hash]struct{}, len(fs.inFlight))
	for h := range fs.inFlight {
		snap[h] = struct{}{}
	}
	fs.mu.Unlock()
	return func(h types.Hash) bool { _, ok := snap[h]; return ok }
}

// inFlightPruneDepth: in-flight entries of blocks this far below a written
// block belong to abandoned siblings; their frames become fillable again.
const inFlightPruneDepth = 16

// framesPersisted runs after a block is written: its frames are in the
// Frames table now. The store pins them to the block's height (not
// fillable, not evictable) until a commit decides: committed frames are
// evicted KeepBlocks later, frames of an abandoned fork become fillable
// again (store.Commit). The in-flight set forgets them.
func (bc *BlockChain) framesPersisted(b *block.Block) {
	body, ok := b.Body().(*block.Body)
	if !ok || !body.IsFrameBody() {
		return
	}
	number := b.Number64().Uint64()
	if st := bc.frames.store; st != nil {
		st.Include(body.FrameHashes, number)
	}
	fs := &bc.frames
	fs.mu.Lock()
	for _, h := range body.FrameHashes {
		delete(fs.inFlight, h)
	}
	for h, n := range fs.inFlight {
		if n+inFlightPruneDepth < number {
			delete(fs.inFlight, h)
		}
	}
	fs.mu.Unlock()
}

// framesCommitWalkMax bounds the ancestor walk of framesCommitted. A
// commit that jumps further (catch-up) marks only the newest blocks; frames
// of the older ones are left pinned until released, then at worst filled
// again and skipped as stale nonces.
const framesCommitWalkMax = 256

// framesCommitted runs after blk became the committed head. A commit also
// commits every uncommitted ancestor, so the frames of the blocks between
// the store's previous committed head and blk are marked first, oldest
// first; then blk's own. The store releases fork pins at or below blk's
// height and evicts committed frames past the keep margin.
func (bc *BlockChain) framesCommitted(blk *block.Block) {
	st := bc.frames.store
	if st == nil || blk == nil {
		return
	}
	number := blk.Number64().Uint64()
	type commitStep struct {
		n      uint64
		hashes []types.Hash
	}
	var steps []commitStep
	if body, ok := blk.Body().(*block.Body); ok && body.IsFrameBody() {
		steps = append(steps, commitStep{number, body.FrameHashes})
	}
	if prev, ok := st.CommittedHead(); ok && number > prev+1 && bc.ChainDB != nil {
		if tx, err := bc.ChainDB.BeginRo(context.Background()); err == nil {
			hash, n := blk.ParentHash(), number-1
			for walked := 0; n > prev && walked < framesCommitWalkMax; walked++ {
				if hs, err := rawdb.ReadFrameBodyHashes(tx, hash, n); err == nil && len(hs) > 0 {
					steps = append(steps, commitStep{n, hs})
				}
				hdr := rawdb.ReadHeader(tx, hash, n)
				if hdr == nil {
					break
				}
				hash, n = hdr.ParentHash, n-1
			}
			tx.Rollback()
		}
	}
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].n == number {
			continue
		}
		st.Commit(steps[i].n, steps[i].hashes)
	}
	var own []types.Hash
	if len(steps) > 0 && steps[0].n == number {
		own = steps[0].hashes
	}
	st.Commit(number, own)
}

func framePullTimeout() time.Duration {
	if v, err := strconv.Atoi(os.Getenv(EnvFramePullTimeout)); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return 2 * time.Second
}

// ResolveFrames fills a frame body's transactions from its frames: the
// frame store first, then the Frames table, then a pull from peers. A body
// that is not a frame body, or is already resolved, is left alone. When a
// frame cannot be found the block stays unresolved and the error is
// ErrFramesUnavailable; nothing is logged twice for one block.
func (bc *BlockChain) ResolveFrames(b block.IBlock) error {
	body, ok := b.Body().(*block.Body)
	if !ok || !body.IsFrameBody() {
		return nil
	}
	fs := &bc.frames
	fs.resolveMu.Lock()
	defer fs.resolveMu.Unlock()
	if body.Txs != nil {
		return nil
	}
	t0 := time.Now()
	hashes := body.FrameHashes
	raws := make([][]byte, len(hashes))
	var missing []int
	for i, h := range hashes {
		if fs.store != nil {
			if raw, ok := fs.store.Get(h); ok {
				raws[i] = raw
				continue
			}
		}
		missing = append(missing, i)
	}
	fromDB := 0
	if len(missing) > 0 && bc.ChainDB != nil {
		if tx, err := bc.ChainDB.BeginRo(context.Background()); err == nil {
			still := missing[:0]
			for _, i := range missing {
				raw, err := rawdb.ReadFrame(tx, hashes[i])
				if err == nil && len(raw) > 0 {
					raws[i] = append([]byte(nil), raw...)
					fromDB++
					continue
				}
				still = append(still, i)
			}
			missing = still
			tx.Rollback()
		}
	}
	pulled := 0
	if len(missing) > 0 && fs.puller != nil {
		want := make([]types.Hash, len(missing))
		for j, i := range missing {
			want[j] = hashes[i]
		}
		ctx, cancel := context.WithTimeout(context.Background(), framePullTimeout())
		got := fs.puller(ctx, want)
		cancel()
		still := missing[:0]
		for _, i := range missing {
			if raw, ok := got[hashes[i]]; ok {
				raws[i] = raw
				pulled++
				continue
			}
			still = append(still, i)
		}
		missing = still
	}
	if len(missing) > 0 {
		fs.loggedMu.Lock()
		if fs.logged == nil || len(fs.logged) > 4096 {
			fs.logged = make(map[types.Hash]struct{})
		}
		_, seen := fs.logged[b.Hash()]
		fs.logged[b.Hash()] = struct{}{}
		fs.loggedMu.Unlock()
		if !seen {
			log.Warn("frame body: frames unavailable, not importing (no vote)", "number", b.Number64().Uint64(),
				"hash", b.Hash(), "frames", len(hashes), "missing", len(missing), "first", hashes[missing[0]])
		}
		return fmt.Errorf("%w: block %d misses %d of %d frames", ErrFramesUnavailable, b.Number64().Uint64(), len(missing), len(hashes))
	}
	txs, dropped, err := frame.DecodeFrames(raws, 0)
	if err != nil {
		return fmt.Errorf("frame body: block %d: %w", b.Number64().Uint64(), err)
	}
	if txs == nil {
		txs = []*transaction.Transaction{}
	}
	body.Txs = txs
	body.FrameData = raws
	bc.MarkFramesInFlight(hashes, b.Number64().Uint64())
	log.Debug("frame body resolved", "number", b.Number64().Uint64(), "frames", len(hashes), "txs", len(txs),
		"dropped", dropped, "fromDB", fromDB, "pulled", pulled, "ms", time.Since(t0).Milliseconds())
	return nil
}
