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

// This file contains all block/state write methods for BlockChain.
// Read-only methods are in blockchain_reader.go.

import (
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/exex"
	nodeMetrics "github.com/n42blockchain/N42/internal/metrics"
	"github.com/n42blockchain/N42/internal/tracing"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// WriteBlockWithoutState persists a block without writing its state.
func (bc *BlockChain) WriteBlockWithoutState(blk block.IBlock) error {
	if bc.insertStopped() {
		return errInsertionInterrupted
	}
	return bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		return rawdb.WriteBlock(tx, blk.(*block.Block))
	})
}

// writeBlockWithTd writes a block along with its total difficulty to the database.
// Used during sidechain insertion to ensure td is available for ReorgNeeded checks.
func (bc *BlockChain) writeBlockWithTd(blk block.IBlock, td *uint256.Int) error {
	if bc.insertStopped() {
		return errInsertionInterrupted
	}
	if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, blk.(*block.Block)); err != nil {
			return err
		}
		return rawdb.WriteTd(tx, blk.Hash(), blk.Number64().Uint64(), td)
	}); err != nil {
		return err
	}
	bc.tdCache.Add(blk.Hash(), td)
	return nil
}

// WriteBlockWithState writes a block, its receipts, and state to the database (public API).
func (bc *BlockChain) WriteBlockWithState(blk block.IBlock, receipts []*block.Receipt, ibs interface{}, nopay map[types.Address]*uint256.Int) error {
	bc.lock.Lock()
	defer bc.lock.Unlock()

	stateDB, ok := ibs.(*state.IntraBlockState)
	if !ok {
		return errors.New("WriteBlockWithState: ibs must be *state.IntraBlockState")
	}
	_, err := bc.writeBlockWithState(blk, receipts, stateDB, nopay)
	return err
}

// writeBlockWithState persists block, receipts, state, and reward data, then
// decides whether a reorg is needed. Returns the resulting WriteStatus.
func (bc *BlockChain) writeBlockWithState(blk block.IBlock, receipts []*block.Receipt, ibs *state.IntraBlockState, nopay map[types.Address]*uint256.Int) (status WriteStatus, retErr error) {
	// OpenTelemetry: trace block write operations.
	bcTracer := tracing.Tracer("blockchain")
	_, span := tracing.StartSpan(bc.ctx, bcTracer, "blockchain.writeBlockWithState")
	span.SetAttributes(
		tracing.Int64Attr("block.number", int64(blk.Number64().Uint64())),
		tracing.StringAttr("block.hash", blk.Hash().String()),
		tracing.Int64Attr("block.tx_count", int64(len(blk.Body().Transactions()))),
		tracing.Int64Attr("block.receipt_count", int64(len(receipts))),
	)
	defer func() {
		if retErr != nil {
			tracing.SetSpanError(span, retErr)
		} else {
			tracing.SetSpanOK(span)
		}
		span.End()
	}()

	var externTd *uint256.Int

	if err := bc.ChainDB.Update(bc.ctx, func(tx kv.RwTx) error {
		ptd, err := rawdb.ReadTd(tx, blk.ParentHash(), uint256.NewInt(0).Sub(blk.Number64(), uint256.NewInt(1)).Uint64())
		if err != nil {
			log.Errorf("ReadTd failed err: %v", err)
		}
		if ptd == nil {
			return consensus.ErrUnknownAncestor
		}

		externTd = uint256.NewInt(0).Add(ptd, blk.Difficulty())
		if err := rawdb.WriteTd(tx, blk.Hash(), blk.Number64().Uint64(), externTd); err != nil {
			return err
		}
		log.Trace("writeTd:", "number", blk.Number64().Uint64(), "hash", blk.Hash(), "td", externTd.Uint64())

		if len(receipts) > 0 {
			if err := rawdb.AppendReceipts(tx, blk.Number64().Uint64(), receipts); err != nil {
				log.Errorf("rawdb.AppendReceipts failed err= %v", err)
				return err
			}
			if err := rawdb.WriteLogIndex(tx, blk.Number64().Uint64(), receipts); err != nil {
				log.Errorf("rawdb.WriteLogIndex failed err= %v", err)
				return err
			}
		}
		if err := rawdb.WriteBlock(tx, blk.(*block.Block)); err != nil {
			return err
		}

		var stateWriter state.WriterWithChangeSets = state.NewPlainStateWriter(tx, tx, blk.Number64().Uint64())
		if cache := layered.ExtractCache(bc.ChainDB); cache != nil {
			stateWriter = state.NewCachedStateWriter(stateWriter, cache)
		}
		if err := ibs.CommitBlock(bc.chainConfig.Rules(blk.Number64().Uint64()), stateWriter); err != nil {
			return err
		}
		if err := stateWriter.WriteChangeSets(); err != nil {
			return fmt.Errorf("writing changesets for block %d failed: %w", blk.Number64().Uint64(), err)
		}
		if err := stateWriter.WriteHistory(); err != nil {
			return fmt.Errorf("writing history for block %d failed: %w", blk.Number64().Uint64(), err)
		}

		if nopay != nil {
			for addr, v := range nopay {
				rawdb.PutAccountReward(tx, addr, v)
			}
		}
		return nil
	}); err != nil {
		return NonStatTy, err
	}

	if externTd != nil {
		bc.tdCache.Add(blk.Hash(), externTd)
	}

	reorg, err := bc.forker.ReorgNeeded(bc.CurrentBlock().Header(), blk.Header())
	if err != nil {
		return NonStatTy, err
	}

	if reorg {
		if blk.ParentHash() != bc.CurrentBlock().Hash() {
			if err := bc.reorg(nil, bc.CurrentBlock(), blk); err != nil {
				return NonStatTy, err
			}
		}
		status = CanonStatTy
	} else {
		status = SideStatTy
	}

	if status == CanonStatTy {
		if err := bc.writeHeadBlock(nil, blk); err != nil {
			log.Errorf("failed to save latest blocks, err: %v", err)
			return NonStatTy, err
		}

		// Notify ExEx extensions about the committed block.
		if bc.exexManager != nil {
			bc.exexManager.Notify(&exex.ExExNotification{
				Type:     exex.NotificationCommit,
				Block:    blk,
				Receipts: receipts,
				Hash:     blk.Hash(),
				Number:   blk.Number64().Uint64(),
			})
		}
	}

	if _, ok := bc.futureBlocks.Get(blk.Hash()); ok {
		bc.futureBlocks.Remove(blk.Hash())
	}
	return status, nil
}

// writeHeadBlock updates the canonical head block and its associated indexes.
func (bc *BlockChain) writeHeadBlock(tx kv.RwTx, blk block.IBlock) error {
	var err error
	var notExternalTx bool
	if tx == nil {
		tx, err = bc.ChainDB.BeginRw(bc.ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		notExternalTx = true
	}

	rawdb.WriteHeadBlockHash(tx, blk.Hash())
	rawdb.WriteTxLookupEntries(tx, blk.(*block.Block))

	if err = rawdb.WriteCanonicalHash(tx, blk.Hash(), blk.Number64().Uint64()); err != nil {
		return err
	}

	if notExternalTx {
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	b := blk.(*block.Block)
	bc.currentBlock.Store(b)
	blockNum := blk.Number64().Uint64()
	headBlockGauge.Set(blockNum)
	headGasUsedGauge.Set(b.GasUsed())
	headGasLimitGauge.Set(b.GasLimit())
	headTransactionsGauge.Set(uint64(len(b.Transactions())))
	nodeMetrics.SyncCurrentBlock.Set(blockNum)
	return nil
}

// writeKnownBlock updates the head block with a known block
// and introduces chain reorg if necessary.
func (bc *BlockChain) writeKnownBlock(tx kv.RwTx, blk block.IBlock) error {
	var notExternalTx bool
	var err error
	if tx == nil {
		tx, err = bc.ChainDB.BeginRw(bc.ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		notExternalTx = true
	}

	current := bc.CurrentBlock()
	if blk.ParentHash() != current.Hash() {
		if err := bc.reorg(tx, current, blk); err != nil {
			return err
		}
	}
	if err = bc.writeHeadBlock(tx, blk); err != nil {
		return err
	}
	if notExternalTx {
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
