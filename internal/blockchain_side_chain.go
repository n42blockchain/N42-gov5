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
// Side-chain insertion, ancestor recovery and bad-sibling bookkeeping.

package internal

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// =============================================================================
// Sidechain Insertion
// =============================================================================

func (bc *BlockChain) insertSideChain(blk block.IBlock, it *insertIterator, authorizedSwitch bool) (int, error) {
	var (
		externTd      uint256.Int
		lastBlock     = blk
		current       = bc.CurrentBlock()
		currentNumber *uint256.Int
	)
	err := ErrPrunedAncestor
	currentNumber, err = requireBlockNumber(current, "current block number unavailable")
	if err != nil {
		return 0, err
	}

	for ; blk != nil && errors.Is(err, ErrPrunedAncestor); blk, err = it.next() {
		blockNumber, numberErr := requireBlockNumber(blk, "block number unavailable")
		if numberErr != nil {
			return it.index, numberErr
		}
		if currentNumber.Cmp(blockNumber) >= 0 {
			canonical, err := bc.GetBlockByNumber(blockNumber)
			if err != nil {
				return 0, err
			}
			if canonical != nil && canonical.Hash() == blk.Hash() {
				pt := bc.GetTd(blk.Hash(), blockNumber)
				if pt == nil {
					log.Warn("Missing td for canonical block, skipping sidechain processing", "hash", blk.Hash(), "number", blk.Number64())
					return 0, nil
				}
				externTd = *pt
				continue
			}
			deferredSibling := false
			if h, ok := blk.Header().(*block.Header); ok && bc.chainConfig != nil && bc.chainConfig.IsDeferredExecution(h.Time) {
				deferredSibling = true // siblings carry the same parent root by construction
			}
			if canonical != nil && !deferredSibling && canonical.StateRoot() == blk.StateRoot() {
				log.Warn("Sidechain ghost-state mismatch detected", "number", blk.Number64(), "sideroot", blk.StateRoot(), "canonroot", canonical.StateRoot())
				return it.index, errors.New("sidechain ghost-state mismatch")
			}
		}
		if externTd.Cmp(uint256.NewInt(0)) == 0 {
			if blockNumber.IsZero() {
				return it.index, errors.New("sidechain block has no parent")
			}
			parentTd := bc.GetTd(blk.ParentHash(), uint256.NewInt(0).Sub(blockNumber, uint256.NewInt(1)))
			if parentTd == nil {
				log.Warn("Missing td for parent block, skipping sidechain processing", "parentHash", blk.ParentHash(), "number", blk.Number64())
				return 0, nil
			}
			externTd = *parentTd
		}
		externTd = *externTd.Add(&externTd, blk.Difficulty())

		if !bc.HasBlock(blk.Hash(), blockNumber.Uint64()) {
			start := time.Now()
			if err := bc.writeBlockWithTd(blk, externTd.Clone()); err != nil {
				return it.index, err
			}
			log.Debug("Stored sidechain block", "number", blk.Number64(), "hash", blk.Hash(),
				"diff", blk.Difficulty(), "elapsed", time.Since(start).Seconds(),
				"txs", len(blk.Transactions()), "gas", blk.GasUsed(), "root", blk.StateRoot())
		} else {
			bc.tdCache.Add(blk.Hash(), externTd.Clone())
		}
		lastBlock = blk
	}

	reorg, err := bc.forker.ReorgNeeded(current.Header(), lastBlock.Header())
	if err != nil {
		return it.index, err
	}
	if !reorg {
		localTd := bc.GetTd(current.Hash(), currentNumber)
		log.Info("Sidechain written to disk", "start", it.first().Number64(), "end", it.previous().Number64(), "sidetd", externTd, "localtd", localTd)
		return it.index, err
	}

	var (
		hashes  []types.Hash
		numbers []uint64
	)
	parent := it.previous()
	for parent != nil {
		parentHeader, err := requireConcreteHeader(parent, "unexpected parent header type")
		if err != nil {
			return it.index, err
		}
		parentNumber, err := requireHeaderNumber(parent, "parent header number unavailable")
		if err != nil {
			return it.index, err
		}
		if bc.HasState(parent.StateRoot()) {
			break
		}
		hashes = append(hashes, parent.Hash())
		numbers = append(numbers, parentNumber.Uint64())
		if parentNumber.IsZero() {
			parent = nil
			break
		}
		parent = bc.GetHeader(parentHeader.ParentHash, uint256.NewInt(0).Sub(parentNumber, uint256.NewInt(1)))
	}
	if parent == nil {
		// Semantic sentinel, not a bare error: the side chain's ancestry walks
		// off blocks we don't hold (e.g. a committed same-height sibling whose
		// direct push we missed). The gossip/push handlers future-queue on
		// unknown-ancestor and actively fetch the missing parent by hash;
		// a bare error would mark the block BAD and drop it permanently.
		return it.index, consensus.ErrUnknownAncestor
	}

	var blocks []block.IBlock
	for i := len(hashes) - 1; i >= 0; i-- {
		b := bc.GetBlock(hashes[i], numbers[i])
		blocks = append(blocks, b)

		if len(blocks) >= 2048 {
			log.Info("Importing heavy sidechain segment", "blocks", len(blocks), "start", blocks[0].Number64(), "end", b.Number64())
			if _, err := bc.insertChain(blocks, authorizedSwitch); err != nil {
				return 0, err
			}
			blocks = blocks[:0]
			if bc.insertStopped() {
				log.Debug("Abort during blocks processing")
				return 0, nil
			}
		}
	}
	if len(blocks) > 0 {
		log.Info("Importing sidechain segment", "start", blocks[0].Number64(), "end", blocks[len(blocks)-1].Number64())
		return bc.insertChain(blocks, authorizedSwitch)
	}
	return 0, nil
}

// =============================================================================
// Ancestor Recovery
// =============================================================================

func (bc *BlockChain) recoverAncestors(blk block.IBlock, authorizedSwitch bool) (types.Hash, error) {
	var (
		hashes  []types.Hash
		numbers []uint256.Int
		parent  = blk
	)
	for parent != nil {
		parentNumber, err := requireBlockNumber(parent, "ancestor block number unavailable")
		if err != nil {
			return types.Hash{}, err
		}
		if bc.HasState(parent.Hash()) {
			break
		}
		hashes = append(hashes, parent.Hash())
		numbers = append(numbers, *parentNumber)
		if parentNumber.IsZero() {
			break
		}
		parent = bc.GetBlock(parent.ParentHash(), parentNumber.Uint64()-1)
	}
	if parent == nil {
		// See insertSideChain: unknown-ancestor semantics so callers can
		// future-queue + fetch instead of marking the block bad.
		return types.Hash{}, consensus.ErrUnknownAncestor
	}
	for i := len(hashes) - 1; i >= 0; i-- {
		var b block.IBlock
		if i == 0 {
			b = blk
		} else {
			b = bc.GetBlock(hashes[i], numbers[i].Uint64())
		}
		if _, err := bc.insertChain([]block.IBlock{b}, authorizedSwitch); err != nil {
			return b.ParentHash(), err
		}
	}
	return blk.Hash(), nil
}

// isUnknownAncestorErr reports whether err signals a missing parent, across the
// several independent "unknown ancestor" errors defined in different packages
// (internal.ErrUnknownAncestor, consensus.ErrUnknownAncestor, consensus/misc,
// plus bare errors.New in apoa/apos). A child that arrives before its parent
// must be future-queued, not reported as a bad block. Matching only
// internal.ErrUnknownAncestor let the consensus-package sentinel returned by
// hotstuff VerifyHeader fall through to reportBlock, spinning a joining node on
// BAD BLOCK while it waited for the gap to fill.
func isUnknownAncestorErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnknownAncestor) || errors.Is(err, consensus.ErrUnknownAncestor) {
		return true
	}
	return strings.Contains(err.Error(), "unknown ancestor")
}

// reportBlock logs a bad block error.
func (bc *BlockChain) reportBlock(blk block.IBlock, receipts []*block.Receipt, err error) {
	bc.markBadSibling(blk)
	var receiptString string
	for i, receipt := range receipts {
		receiptString += fmt.Sprintf("\t %d: cumulative: %v gas: %v contract: %v status: %v tx: %v logs: %v bloom: %x state: %x\n",
			i, receipt.CumulativeGasUsed, receipt.GasUsed, receipt.ContractAddress.String(),
			receipt.Status, receipt.TxHash.String(), "Logs", receipt.GetBloom(), receipt.PostState)
	}
	log.Error(fmt.Sprintf(`
########## BAD BLOCK #########

Number: %v
Hash: %#x
%v

Error: %v
##############################
`, blk.Number64().String(), blk.Hash(), receiptString, err))
}

// markBadSibling persists a validation failure so this node's leader never
// converges on the stored block again (LowestSiblingAtHeight, and the miner's
// deterministic-rebuild short circuit). Written from its own transaction on
// another goroutine: reportBlock runs inside the import's read transaction.
// The mark is advisory for PROPOSING only -- import never consults it, so a
// transient local failure cannot make this node refuse the committed chain.
func (bc *BlockChain) markBadSibling(blk block.IBlock) {
	if blk == nil {
		return
	}
	hash, number := blk.Hash(), blk.Number64().Uint64()
	bc.badSiblingsMu.Lock()
	if bc.badSiblings == nil {
		bc.badSiblings = make(map[types.Hash]struct{})
	}
	bc.badSiblings[hash] = struct{}{}
	bc.badSiblingsMu.Unlock()
	go func() {
		if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
			return rawdb.WriteBadHeaderMark(tx, hash, number)
		}); err != nil {
			log.Warn("bad-sibling mark not persisted", "hash", hash.Hex()[:12], "number", number, "err", err)
		}
	}()
}

// BadSibling reports whether this node recorded a validation failure for hash
// (in memory this process, or persisted by an earlier one).
func (bc *BlockChain) BadSibling(hash types.Hash) bool {
	bc.badSiblingsMu.RLock()
	_, ok := bc.badSiblings[hash]
	bc.badSiblingsMu.RUnlock()
	if ok {
		return true
	}
	_ = bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		ok = rawdb.IsBadHeaderMarked(tx, hash)
		return nil
	})
	return ok
}
