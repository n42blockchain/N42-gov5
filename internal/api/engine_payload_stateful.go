package api

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	internalcore "github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func (e *EngineAPIV1) canonicalHead() block.IBlock {
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil {
		return nil
	}
	return e.api.api.BlockChain().CurrentBlock()
}

func (e *EngineAPIV1) canonicalHeadHash() types.Hash {
	return ethCompatibleBlockHash(e.canonicalHead(), e.chainConfig())
}

func (e *EngineAPIV1) validatePayloadExecution(blk block.IBlock, parentHash types.Hash, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes) error {
	if blk == nil || parentHash == (types.Hash{}) {
		return nil
	}
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil || e.api.api.engine == nil {
		return nil
	}
	parent := e.canonicalHead()
	if parent == nil || e.canonicalHeadHash() != parentHash {
		return nil
	}
	concreteBlock, ok := blk.(*block.Block)
	if !ok {
		return fmt.Errorf("unexpected execution payload block type %T", blk)
	}
	parentHeader := blockHeader(parent)
	header := blockHeader(concreteBlock)
	if parentHeader == nil || parentHeader.Number == nil || header == nil {
		return nil
	}
	db := e.api.api.BlockChain().DB()
	if db == nil {
		return nil
	}
	return withCanonicalParentState(db, parentHeader.Number.Uint64(), func(tx kv.Tx, stateReader state.StateReader, ibs *state.IntraBlockState) error {
		blockHashFunc := internalcore.GetHashFn(header, func(_ types.Hash, number uint64) *block.Header {
			if parentHeader.Number.Uint64() == number {
				return parentHeader
			}
			canonicalHash, err := rawdb.ReadCanonicalHash(tx, number)
			if err != nil || canonicalHash == (types.Hash{}) {
				return nil
			}
			return rawdb.ReadHeader(tx, canonicalHash, number)
		})
		gasPool := new(common.GasPool)
		gasPool.AddGas(concreteBlock.GasLimit())
		stateWriter := state.NewNoopWriter()
		usedGas := uint64(0)
		receipts := make(block.Receipts, 0, len(concreteBlock.Transactions()))
		cfg := e.chainConfig()
		if err := internalcore.ProcessExecutionBlockStart(parentBeaconRoot, cfg, ibs, header, e.api.api.engine); err != nil {
			return err
		}

		for i, txn := range concreteBlock.Transactions() {
			ibs.Prepare(txn.Hash(), concreteBlock.Hash(), i)
			receipt, _, err := internalcore.ApplyTransaction(cfg, blockHashFunc, e.api.api.engine, nil, gasPool, ibs, stateWriter, header, txn, &usedGas, vm2.Config{})
			if err != nil {
				return err
			}
			if receipt != nil {
				receipts = append(receipts, receipt)
			}
		}
		if usedGas != header.GasUsed {
			return fmt.Errorf("gas used by execution: %d, in header: %d", usedGas, header.GasUsed)
		}
		var rules *params.Rules
		if cfg != nil {
			rules = cfg.RulesWithTimestamp(uint64FromUint256OrZero(header.Number), header.Time)
		}
		if rules != nil && rules.IsPrague {
			actualRequests, err := internalcore.ProcessExecutionBlockEnd(receipts, cfg, ibs, header, e.api.api.engine)
			if err != nil {
				return err
			}
			if executionRequestsHash(actualRequests) != executionRequestsHash(expectedRequests) {
				return fmt.Errorf("invalid requests hash")
			}
		}
		return nil
	})
}

func withCanonicalParentState(db kv.RwDB, parentNumber uint64, fn func(tx kv.Tx, stateReader state.StateReader, ibs *state.IntraBlockState) error) error {
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var stateReader state.StateReader = state.NewPlainState(tx, parentNumber+1)
	if cache := layered.ExtractCache(db); cache != nil {
		stateReader = state.NewCachedStateReader(stateReader, cache)
	}
	return fn(tx, stateReader, state.New(stateReader))
}
