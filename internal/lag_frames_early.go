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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// Early frame-store writes (N42_FRAMES_WRITE_EARLY=1, default off).
//
// With the frame store (N42_FRAME_DB) a block's frames go to it in their own
// durable transaction, and the block's write transaction waits for that one
// before it commits (imported = durable). By default the frames write starts
// at the hand-off, ~one write before the block's own write, and each block's
// frames transaction (~17 MB, ~107 ms held) contends with the previous
// block's for the frame store's writer lock: the block's write then waits
// ~20 ms (p90 ~60 ms) for its frames while holding the chain database's
// writer lock.
//
// With the flag on, the frames write is queued when the block is queued for
// the lag executor (its frames are all resolved then), a block or more
// before its hand-off, and one goroutine writes the queued frames in queue
// order, several blocks per transaction when they pile up (one commit and
// fsync for all of them). The hand-off takes the queued write instead of
// starting one; the block's write still waits for it before committing.
//
// Frames are content-addressed rows nothing names until a block is stored,
// so the frames of a queued block that is never written (a losing fork) are
// left behind harmlessly, as those of a failed write already are.

// framesWriteEarly is N42_FRAMES_WRITE_EARLY.
var framesWriteEarly = parseFramesWriteEarly(os.Getenv("N42_FRAMES_WRITE_EARLY"))

func parseFramesWriteEarly(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

// SetFramesWriteEarlyForTest switches the early frames write and returns the
// restore.
func SetFramesWriteEarlyForTest(on bool) func() {
	prev := framesWriteEarly
	framesWriteEarly = on
	return func() { framesWriteEarly = prev }
}

// framesBatchBytes bounds the frames one frame-store transaction writes.
const framesBatchBytes = 96 << 20

// framesQueue is the chain's serial frame-store writer and the early writes
// of queued blocks by hash.
type framesQueue struct {
	mu      sync.Mutex
	pending []*framesWrite
	running bool
	byHash  map[types.Hash]*framesWrite
	// taken: hand-offs that took an early write (tests).
	taken uint64
}

// blockFrames returns blk's frame hashes and frames when it is a frame body
// whose frames are all resolved (ok false otherwise).
func blockFrames(blk *block.Block) (hashes []types.Hash, raws [][]byte, ok bool) {
	body, isBody := blk.Body().(*block.Body)
	if !isBody || !body.IsFrameBody() || len(body.FrameData) == 0 {
		return nil, nil, false
	}
	if len(body.FrameData) < len(body.FrameHashes) {
		return nil, nil, false
	}
	for i := range body.FrameHashes {
		if len(body.FrameData[i]) == 0 {
			return nil, nil, false
		}
	}
	return body.FrameHashes, body.FrameData, true
}

// startFramesWriteEarly queues blk's frames for the frame store when it is
// queued for the lag executor (flag on, frame store, all frames resolved).
func (bc *BlockChain) startFramesWriteEarly(blk *block.Block) {
	if !framesWriteEarly || rawdb.FrameStore() == nil {
		return
	}
	hashes, raws, ok := blockFrames(blk)
	if !ok {
		return
	}
	hash := blk.Hash()
	fq := &bc.framesQ
	fq.mu.Lock()
	if _, dup := fq.byHash[hash]; dup {
		fq.mu.Unlock()
		return
	}
	if fq.byHash == nil {
		fq.byHash = make(map[types.Hash]*framesWrite)
	}
	fw := &framesWrite{done: make(chan struct{}), hashes: hashes, raws: raws}
	fq.byHash[hash] = fw
	fq.mu.Unlock()
	bc.queueFramesWrite(fw)
}

// takeFramesWrite returns blk's frames write for its hand-off: the early one
// when queued (flag on), else a new one; nil without a frame store or for a
// block that is not a frame body.
func (bc *BlockChain) takeFramesWrite(blk *block.Block) *framesWrite {
	if !framesWriteEarly {
		return startFramesWrite(blk)
	}
	if rawdb.FrameStore() == nil {
		return nil
	}
	fq := &bc.framesQ
	fq.mu.Lock()
	fw := fq.byHash[blk.Hash()]
	delete(fq.byHash, blk.Hash())
	if fw != nil {
		fq.taken++
	}
	fq.mu.Unlock()
	if fw != nil {
		return fw
	}
	body, ok := blk.Body().(*block.Body)
	if !ok || !body.IsFrameBody() || len(body.FrameData) == 0 {
		return nil
	}
	fw = &framesWrite{done: make(chan struct{}), hashes: body.FrameHashes, raws: body.FrameData}
	bc.queueFramesWrite(fw)
	return fw
}

// dropFramesWriteEarly forgets hash's early frames write (the block left
// the lag executor's queue without a hand-off). The write itself goes on.
func (bc *BlockChain) dropFramesWriteEarly(hash types.Hash) {
	if !framesWriteEarly {
		return
	}
	fq := &bc.framesQ
	fq.mu.Lock()
	delete(fq.byHash, hash)
	fq.mu.Unlock()
}

// queueFramesWrite appends fw to the serial writer's queue, starting the
// writer when it is idle.
func (bc *BlockChain) queueFramesWrite(fw *framesWrite) {
	fq := &bc.framesQ
	fq.mu.Lock()
	fq.pending = append(fq.pending, fw)
	start := !fq.running
	fq.running = true
	fq.mu.Unlock()
	if start {
		go bc.framesWriteLoop()
	}
}

// framesWriteLoop writes the queued frames in queue order, as many blocks
// per transaction as framesBatchBytes allows, until the queue is empty.
func (bc *BlockChain) framesWriteLoop() {
	fq := &bc.framesQ
	for {
		fq.mu.Lock()
		if len(fq.pending) == 0 {
			fq.running = false
			fq.mu.Unlock()
			return
		}
		n, size := 0, 0
		for n < len(fq.pending) {
			sz := fq.pending[n].size()
			if n > 0 && size+sz > framesBatchBytes {
				break
			}
			size += sz
			n++
		}
		batch := append([]*framesWrite(nil), fq.pending[:n]...)
		fq.pending = append(fq.pending[:0], fq.pending[n:]...)
		fq.mu.Unlock()

		t0 := time.Now()
		err := writeFramesBatch(batch)
		took := time.Since(t0)
		for _, fw := range batch {
			fw.err, fw.took = err, took
			close(fw.done)
		}
	}
}

// writeFramesBatch writes the frames of batch in one frame-store
// transaction.
func writeFramesBatch(batch []*framesWrite) error {
	fdb := rawdb.FrameStore()
	if fdb == nil {
		return nil
	}
	return fdb.Update(context.Background(), func(tx kv.RwTx) error {
		for _, fw := range batch {
			for i, h := range fw.hashes {
				if i >= len(fw.raws) || len(fw.raws[i]) == 0 {
					continue
				}
				if err := rawdb.WriteFrame(tx, h, fw.raws[i]); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (fw *framesWrite) size() int {
	n := 0
	for _, r := range fw.raws {
		n += len(r)
	}
	return n
}
