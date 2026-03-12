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

package graphql

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/holiman/uint256"

	blockpkg "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// maxLogsBlockRange limits the number of blocks a log query may span to prevent
// unbounded scans against the database.
const maxLogsBlockRange = 2000

// Resolver resolves GraphQL queries by delegating to the internal API backend.
type Resolver struct {
	api *api.API
}

// NewResolver creates a new Resolver backed by the given API instance.
func NewResolver(apiBackend *api.API) *Resolver {
	return &Resolver{api: apiBackend}
}

// Block resolves a block by number or hash. When neither is provided the latest
// block is returned.
func (r *Resolver) Block(ctx context.Context, args BlockArgs) (*Block, error) {
	var iblock blockpkg.IBlock
	var err error

	switch {
	case args.Hash != nil:
		iblock, err = r.api.BlockChain().GetBlockByHash(*args.Hash)
		if err != nil {
			return nil, fmt.Errorf("block not found by hash: %w", err)
		}
	case args.Number != nil:
		num := uint64(*args.Number)
		iblock, err = r.api.BlockChain().GetBlockByNumber(uint256.NewInt(num))
		if err != nil {
			return nil, fmt.Errorf("block not found by number: %w", err)
		}
	default:
		iblock = r.api.BlockChain().CurrentBlock()
	}

	if iblock == nil {
		return nil, errors.New("block not found")
	}

	return r.marshalBlock(iblock)
}

// Transaction resolves a single transaction by its hash.
func (r *Resolver) Transaction(ctx context.Context, hash types.Hash) (*Transaction, error) {
	tx, blockHash, blockNumber, index, err := r.readTransactionByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, errors.New("transaction not found")
	}

	result := marshalTransaction(tx, blockHash, blockNumber, index)
	return result, nil
}

// Account resolves account state at a given block number. When blockNr is nil
// the latest state is used.
func (r *Resolver) Account(ctx context.Context, addr types.Address, blockNr *uint64) (*Account, error) {
	var bnOrHash jsonrpc.BlockNumberOrHash
	if blockNr != nil {
		bn := jsonrpc.BlockNumber(int64(*blockNr))
		bnOrHash = jsonrpc.BlockNumberOrHashWithNumber(bn)
	} else {
		bnOrHash = jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	}

	dbTx, err := r.api.Database().BeginRo(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open database transaction: %w", err)
	}
	defer dbTx.Rollback()

	ibs := r.api.State(dbTx, bnOrHash)
	if ibs == nil {
		return nil, errors.New("state not available for requested block")
	}

	balance := ibs.GetBalance(addr)
	nonce := ibs.GetNonce(addr)
	code := ibs.GetCode(addr)

	return &Account{
		Address:          addr,
		Balance:          (*hexutil.Big)(balance.ToBig()),
		Nonce:            hexutil.Uint64(nonce),
		Code:             code,
		TransactionCount: hexutil.Uint64(nonce),
	}, nil
}

// Logs resolves log entries matching the given filter criteria.
func (r *Resolver) Logs(ctx context.Context, filter LogFilter) ([]*Log, error) {
	currentBlock := r.api.BlockChain().CurrentBlock()
	if currentBlock == nil {
		return nil, errors.New("chain not available")
	}
	currentNum := currentBlock.Number64().Uint64()

	var from, to uint64
	if filter.FromBlock != nil {
		from = uint64(*filter.FromBlock)
	}
	if filter.ToBlock != nil {
		to = uint64(*filter.ToBlock)
	} else {
		to = currentNum
	}

	if from > to {
		return nil, errors.New("fromBlock must be less than or equal to toBlock")
	}
	if to-from > maxLogsBlockRange {
		return nil, fmt.Errorf("block range exceeds maximum of %d blocks", maxLogsBlockRange)
	}

	var results []*Log

	for num := from; num <= to; num++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		iblock, err := r.api.BlockChain().GetBlockByNumber(uint256.NewInt(num))
		if err != nil || iblock == nil {
			continue
		}

		blockLogs, err := r.api.BlockChain().GetLogs(iblock.Hash())
		if err != nil {
			continue
		}

		blockHash := iblock.Hash()
		for txIdx, txLogs := range blockLogs {
			for _, lg := range txLogs {
				if !matchLog(lg, filter) {
					continue
				}
				results = append(results, &Log{
					Address:          lg.Address,
					Topics:           lg.Topics,
					Data:             lg.Data,
					BlockNumber:      hexutil.Uint64(num),
					BlockHash:        blockHash,
					TransactionHash:  lg.TxHash,
					TransactionIndex: hexutil.Uint64(txIdx),
					LogIndex:         hexutil.Uint64(lg.Index),
					Removed:          lg.Removed,
				})
			}
		}
	}

	if results == nil {
		results = make([]*Log, 0)
	}
	return results, nil
}

// matchLog returns true when the log satisfies the filter's address and topic
// constraints.
func matchLog(lg *blockpkg.Log, filter LogFilter) bool {
	if len(filter.Addresses) > 0 {
		matched := false
		for _, addr := range filter.Addresses {
			if lg.Address == addr {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	for i, topicGroup := range filter.Topics {
		if len(topicGroup) == 0 {
			continue
		}
		if i >= len(lg.Topics) {
			return false
		}
		matched := false
		for _, topic := range topicGroup {
			if lg.Topics[i] == topic {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// marshalBlock converts an IBlock into the GraphQL Block type.
func (r *Resolver) marshalBlock(iblock blockpkg.IBlock) (*Block, error) {
	header, ok := iblock.Header().(*blockpkg.Header)
	if !ok || header == nil {
		return nil, errors.New("invalid block header type")
	}

	parentHash := iblock.ParentHash()

	gqlBlock := &Block{
		Number:           hexutil.Uint64(iblock.Number64().Uint64()),
		Hash:             iblock.Hash(),
		Parent:           &parentHash,
		Timestamp:        hexutil.Uint64(iblock.Time()),
		GasUsed:          hexutil.Uint64(iblock.GasUsed()),
		GasLimit:         hexutil.Uint64(iblock.GasLimit()),
		Miner:            iblock.Coinbase(),
		StateRoot:        header.Root,
		ReceiptsRoot:     header.ReceiptHash,
		TransactionsRoot: header.TxHash,
		LogsBloom:        header.Bloom[:],
		ExtraData:        header.Extra,
	}

	if header.Difficulty != nil {
		gqlBlock.Difficulty = (*hexutil.Big)(header.Difficulty.ToBig())
	} else {
		gqlBlock.Difficulty = (*hexutil.Big)(new(big.Int))
	}

	td := r.api.BlockChain().GetTd(iblock.Hash(), iblock.Number64())
	if td != nil {
		gqlBlock.TotalDifficulty = (*hexutil.Big)(td.ToBig())
	} else {
		gqlBlock.TotalDifficulty = (*hexutil.Big)(new(big.Int))
	}

	if header.BaseFee != nil {
		gqlBlock.BaseFeePerGas = (*hexutil.Big)(header.BaseFee.ToBig())
	}

	// Marshal transactions.
	txs := iblock.Transactions()
	gqlTxs := make([]*Transaction, len(txs))
	blockHash := iblock.Hash()
	blockNum := iblock.Number64().Uint64()
	for i, tx := range txs {
		gqlTxs[i] = marshalTransaction(tx, blockHash, blockNum, uint64(i))
	}
	gqlBlock.Transactions = gqlTxs

	return gqlBlock, nil
}

// marshalTransaction converts a raw transaction into the GraphQL Transaction type.
func marshalTransaction(tx *transaction.Transaction, blockHash types.Hash, blockNumber uint64, index uint64) *Transaction {
	bn := hexutil.Uint64(blockNumber)
	idx := hexutil.Uint64(index)

	result := &Transaction{
		Hash:             tx.Hash(),
		Nonce:            hexutil.Uint64(tx.Nonce()),
		Gas:              hexutil.Uint64(tx.Gas()),
		Input:            tx.Data(),
		Type:             hexutil.Uint64(tx.Type()),
		BlockNumber:      &bn,
		BlockHash:        &blockHash,
		TransactionIndex: &idx,
	}

	if tx.Value() != nil {
		result.Value = (*hexutil.Big)(tx.Value().ToBig())
	} else {
		result.Value = (*hexutil.Big)(new(big.Int))
	}

	if tx.GasPrice() != nil {
		result.GasPrice = (*hexutil.Big)(tx.GasPrice().ToBig())
	} else {
		result.GasPrice = (*hexutil.Big)(new(big.Int))
	}

	if to := tx.To(); to != nil {
		result.To = to
	}

	if from := tx.From(); from != nil {
		result.From = *from
	}

	// EIP-1559 fields.
	if tx.GasFeeCap() != nil {
		result.MaxFeePerGas = (*hexutil.Big)(tx.GasFeeCap().ToBig())
	}
	if tx.GasTipCap() != nil {
		result.MaxPriorityFeePerGas = (*hexutil.Big)(tx.GasTipCap().ToBig())
	}

	return result
}

// readTransactionByHash looks up a transaction from the database by its hash.
func (r *Resolver) readTransactionByHash(ctx context.Context, hash types.Hash) (
	tx *transaction.Transaction, blockHash types.Hash, blockNumber uint64, index uint64, err error,
) {
	dbTx, txErr := r.api.Database().BeginRo(ctx)
	if txErr != nil {
		err = fmt.Errorf("failed to open database transaction: %w", txErr)
		return
	}
	defer dbTx.Rollback()

	tx, blockHash, blockNumber, index, err = rawdb.ReadTransactionByHash(dbTx, hash)
	return
}
