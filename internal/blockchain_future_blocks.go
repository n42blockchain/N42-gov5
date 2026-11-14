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
//
// Future-block queue: AddFutureBlock and its processing loop.

package internal

import (
	"fmt"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/log"
)

func (bc *BlockChain) updateFutureBlocksLoop() {
	futureTimer := time.NewTicker(2 * time.Second)
	defer futureTimer.Stop()
	defer bc.wg.Done()

	for {
		select {
		case <-futureTimer.C:
			bc.processFutureBlocks()
		case <-bc.ctx.Done():
			return
		}
	}
}

func (bc *BlockChain) processFutureBlocks() {
	if bc.futureBlocks.Len() == 0 {
		return
	}

	currentNumber, err := requireBlockNumber(bc.CurrentBlock(), "current block number unavailable")
	if err != nil {
		log.Warn("Skipping future block processing", "err", err)
		return
	}

	blocks := make([]block.IBlock, 0, bc.futureBlocks.Len())
	for _, key := range bc.futureBlocks.Keys() {
		if value, ok := bc.futureBlocks.Get(key); ok {
			if _, err := requireBlockNumber(value, "future block number unavailable"); err != nil {
				log.Warn("Dropping future block with unavailable number", "hash", key, "err", err)
				bc.futureBlocks.Remove(key)
				continue
			}
			blocks = append(blocks, value)
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Number64().Cmp(blocks[j].Number64()) < 0
	})

	if len(blocks) == 0 {
		return
	}
	firstNumber, err := requireBlockNumber(blocks[0], "future block number unavailable")
	if err != nil {
		log.Warn("Skipping future block processing", "err", err)
		return
	}
	// Gate on the APPLIED head, not the canonical one: under HotStuff the
	// canonical head trails the applied head by the two-chain commit lag, so
	// a queued proposal (applied+1) always read as canonical+3 and never left
	// the queue. Round 35zzc: after a leg restart every follower held its
	// commit vote for a block whose parent it had just imported through
	// fetch-on-miss, the queued child was never retried, the view timed out,
	// and the fleet crawled one block per timeout for twelve minutes.
	headNum := currentNumber.Uint64()
	if an, ok := bc.appliedHeadNumber(); ok && an > headNum {
		headNum = an
	}
	if firstNumber.Uint64() > headNum+1 {
		return
	}

	// Insert one block at a time: under HotStuff the queue can hold SEVERAL
	// same-height sibling candidates (competing views), and a batch insert
	// trips InsertChain's contiguity check ("non contiguous insert: item 0 is
	// #N, item 1 is #N") — rejecting the WHOLE batch forever, in a retry loop.
	// Per-block inserts let each candidate succeed or fail on its own; a block
	// that imports (or is already known) leaves the queue, a still-unready one
	// stays for the next pass.
	inserted := 0
	for _, b := range blocks {
		// The queue holds proposals that arrived by push with consensus
		// authority; retry them with the same authority the push path used.
		if _, err := bc.InsertChainAuthorized([]block.IBlock{b}); err != nil {
			log.Debug("insert future block failed", "number", b.Number64().Uint64(),
				"hash", b.Hash().Hex()[:12], "err", err)
			continue
		}
		bc.futureBlocks.Remove(b.Hash())
		inserted++
	}
	if inserted > 0 {
		log.Infof("insert %d future block(s) success from %d queued", inserted, len(blocks))
	}
}

// AddFutureBlock queues a block for future processing if it is within the allowed window.
// PoS blocks (difficulty == 0) are never queued.
func (bc *BlockChain) AddFutureBlock(blk block.IBlock) error {
	max := uint64(time.Now().Unix() + maxTimeFutureBlocks)
	if blk.Time() > max {
		return fmt.Errorf("future block timestamp %v > allowed %v", blk.Time(), max)
	}
	// Note: difficulty-0 (PoS/HotStuff) blocks are NOT skipped. HotStuff blocks
	// pushed directly from the leader can arrive before their parent and must be
	// queued so processFutureBlocks imports them once the parent lands; skipping
	// them here would silently drop out-of-order blocks and stall the follower.
	concreteBlock, err := requireConcreteBlock(blk, "unexpected block type")
	if err != nil {
		return err
	}
	blockNumber, err := requireBlockNumber(concreteBlock, "block number unavailable")
	if err != nil {
		return err
	}
	// The lag executor holds it and imports it once its parent is applied:
	// a future-queue entry would only retry the same import and keep a
	// second reference to a decoded block (P7: S99's leader future-queued
	// 136 163k-tx blocks in 18 minutes, its heap spiking by 60-140M objects
	// in 30 s, while node6 queued 12).
	if bc.LagImportQueued(blk.Hash()) {
		log.Debug("future block left to the lag executor", "hash", blk.Hash(), "number", blockNumber.Uint64())
		return nil
	}
	log.Info("add future block", "hash", blk.Hash(), "number", blockNumber.Uint64(), "stateRoot", blk.StateRoot(), "txs", len(blk.Body().Transactions()))
	bc.futureBlocks.Add(blk.Hash(), concreteBlock)
	return nil
}
