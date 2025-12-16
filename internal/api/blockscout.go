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

package api

// Blockscout 兼容接口
//
// 本文件补充 Blockscout 区块链浏览器所需的 RPC 接口。
// Blockscout 文档: https://docs.blockscout.com/
//
// 必需接口清单:
//   - eth_syncing ✅
//   - eth_coinbase ✅
//   - eth_mining ✅
//   - eth_hashrate ✅
//   - eth_getBlockTransactionCountByNumber ✅
//   - eth_getTransactionByBlockNumberAndIndex ✅
//   - eth_getUncleCountByBlockNumber ✅
//   - eth_getBlockReceipts ✅ (新版 Blockscout 需要)

import (
	"context"
	"math/big"

	"github.com/holiman/uint256"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// =============================================================================
// 同步状态接口
// =============================================================================

// SyncProgress 表示同步进度
type SyncProgress struct {
	StartingBlock hexutil.Uint64 `json:"startingBlock"`
	CurrentBlock  hexutil.Uint64 `json:"currentBlock"`
	HighestBlock  hexutil.Uint64 `json:"highestBlock"`
	// 以下字段可选，用于更详细的同步进度
	PulledStates  hexutil.Uint64 `json:"pulledStates,omitempty"`
	KnownStates   hexutil.Uint64 `json:"knownStates,omitempty"`
	SyncedAccounts hexutil.Uint64 `json:"syncedAccounts,omitempty"`
	SyncedStorage  hexutil.Uint64 `json:"syncedStorage,omitempty"`
	HealedTrienodes hexutil.Uint64 `json:"healedTrienodes,omitempty"`
}

// Syncing returns false when the node is fully synced, otherwise returns sync progress.
// 返回 false 表示节点已完全同步，否则返回同步进度对象。
func (s *BlockChainAPI) Syncing() (interface{}, error) {
	// 获取当前区块高度
	currentBlock := s.api.BlockChain().CurrentBlock()
	if currentBlock == nil {
		return false, nil
	}
	currentHeight := currentBlock.Number64().Uint64()

	// TODO: 实际实现中应该从 P2P 层获取网络最高区块
	// 目前简化处理：如果节点有区块数据，就认为已同步
	// 实际应该对比网络中其他节点报告的最高区块
	highestBlock := currentHeight

	// 如果当前高度等于最高高度，表示已同步
	if currentHeight >= highestBlock {
		return false, nil
	}

	// 返回同步进度
	return &SyncProgress{
		StartingBlock: hexutil.Uint64(0),
		CurrentBlock:  hexutil.Uint64(currentHeight),
		HighestBlock:  hexutil.Uint64(highestBlock),
	}, nil
}

// =============================================================================
// 挖矿相关接口
// =============================================================================

// Coinbase returns the current coinbase address.
// 返回当前的挖矿收益地址。
func (s *BlockChainAPI) Coinbase() (types.Address, error) {
	// 从当前区块获取 coinbase
	currentBlock := s.api.BlockChain().CurrentBlock()
	if currentBlock == nil {
		return types.Address{}, nil
	}
	return currentBlock.Coinbase(), nil
}

// Mining returns an indication if this node is currently mining.
// 返回节点是否正在挖矿。
func (s *BlockChainAPI) Mining() bool {
	// TODO: 需要从 miner 模块获取实际状态
	// 目前返回 false，实际实现应检查 miner.Mining()
	return false
}

// Hashrate returns the POW hashrate.
// 返回 POW 算力（N42 使用 POS/POA，返回 0）。
func (s *BlockChainAPI) Hashrate() hexutil.Uint64 {
	// N42 使用 POS/POA 共识，没有 hashrate
	return hexutil.Uint64(0)
}

// =============================================================================
// 区块交易数量接口
// =============================================================================

// GetBlockTransactionCountByNumber returns the number of transactions in a block
// matching the given block number.
// 返回指定区块号的区块中的交易数量。
func (s *BlockChainAPI) GetBlockTransactionCountByNumber(ctx context.Context, blockNr jsonrpc.BlockNumber) (*hexutil.Uint, error) {
	var blk block.IBlock
	var err error

	if blockNr == jsonrpc.PendingBlockNumber || blockNr == jsonrpc.LatestBlockNumber {
		blk = s.api.BlockChain().CurrentBlock()
	} else {
		blk, err = s.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(blockNr.Int64())))
	}

	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, nil
	}

	n := hexutil.Uint(len(blk.Transactions()))
	return &n, nil
}

// =============================================================================
// Uncle 相关接口
// =============================================================================

// GetUncleCountByBlockNumber returns number of uncles in the block for the given block number.
// 返回指定区块号的区块中的 Uncle 数量。
// 注意：N42 使用 POA/POS 共识，没有 Uncle 区块。
func (s *BlockChainAPI) GetUncleCountByBlockNumber(ctx context.Context, blockNr jsonrpc.BlockNumber) (*hexutil.Uint, error) {
	var blk block.IBlock
	var err error

	if blockNr == jsonrpc.PendingBlockNumber || blockNr == jsonrpc.LatestBlockNumber {
		blk = s.api.BlockChain().CurrentBlock()
	} else {
		blk, err = s.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(blockNr.Int64())))
	}

	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, nil
	}

	// POA/POS 没有 Uncle
	n := hexutil.Uint(0)
	return &n, nil
}

// GetUncleByBlockNumberAndIndex returns the uncle block for the given block hash and index.
// 返回指定区块号和索引的 Uncle 区块。
// 注意：N42 使用 POA/POS 共识，没有 Uncle 区块，始终返回 nil。
func (s *BlockChainAPI) GetUncleByBlockNumberAndIndex(ctx context.Context, blockNr jsonrpc.BlockNumber, index hexutil.Uint) (map[string]interface{}, error) {
	// POA/POS 没有 Uncle
	return nil, nil
}

// =============================================================================
// 交易查询接口
// =============================================================================

// GetTransactionByBlockNumberAndIndex returns the transaction for the given block number and index.
// 返回指定区块号和交易索引的交易。
func (s *TransactionAPI) GetTransactionByBlockNumberAndIndex(ctx context.Context, blockNr jsonrpc.BlockNumber, index hexutil.Uint) *RPCTransaction {
	var blk block.IBlock
	var err error

	if blockNr == jsonrpc.PendingBlockNumber || blockNr == jsonrpc.LatestBlockNumber {
		blk = s.api.BlockChain().CurrentBlock()
	} else {
		blk, err = s.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(blockNr.Int64())))
	}

	if err != nil || blk == nil {
		return nil
	}

	txs := blk.Transactions()
	if int(index) >= len(txs) {
		return nil
	}

	return newRPCTransaction(
		txs[index],
		blk.Hash(),
		blk.Number64().Uint64(),
		uint64(index),
		blk.Header().BaseFee64().ToBig(),
	)
}

// =============================================================================
// 区块收据接口 (Blockscout 新版需要)
// =============================================================================

// BlockReceipt 表示单个交易收据
type BlockReceipt struct {
	BlockHash         avmcommon.Hash    `json:"blockHash"`
	BlockNumber       hexutil.Uint64    `json:"blockNumber"`
	TransactionHash   avmcommon.Hash    `json:"transactionHash"`
	TransactionIndex  hexutil.Uint64    `json:"transactionIndex"`
	From              avmcommon.Address `json:"from"`
	To                *avmcommon.Address `json:"to"`
	GasUsed           hexutil.Uint64    `json:"gasUsed"`
	CumulativeGasUsed hexutil.Uint64    `json:"cumulativeGasUsed"`
	ContractAddress   *avmcommon.Address `json:"contractAddress"`
	Logs              []*avmtypes.Log   `json:"logs"`
	LogsBloom         block.Bloom       `json:"logsBloom"`
	Status            hexutil.Uint64    `json:"status"`
	EffectiveGasPrice hexutil.Uint64    `json:"effectiveGasPrice"`
	Type              hexutil.Uint64    `json:"type"`
	Root              hexutil.Bytes     `json:"root,omitempty"`
}

// GetBlockReceipts returns all transaction receipts for a given block.
// 返回指定区块的所有交易收据。
// 这是 Blockscout 新版本所需的重要接口。
func (s *BlockChainAPI) GetBlockReceipts(ctx context.Context, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]*BlockReceipt, error) {
	var blk block.IBlock
	var err error

	// 获取区块
	if blockNr, ok := blockNrOrHash.Number(); ok {
		if blockNr == jsonrpc.PendingBlockNumber || blockNr == jsonrpc.LatestBlockNumber {
			blk = s.api.BlockChain().CurrentBlock()
		} else {
			blk, err = s.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(blockNr.Int64())))
		}
	} else if hash, ok := blockNrOrHash.Hash(); ok {
		blk, err = s.api.BlockChain().GetBlockByHash(types.Hash(hash))
	}

	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, nil
	}

	// 获取收据
	receipts, err := s.api.BlockChain().GetReceipts(blk.Hash())
	if err != nil {
		return nil, err
	}

	txs := blk.Transactions()
	if len(receipts) != len(txs) {
		// 收据数量应该与交易数量相同
		return nil, nil
	}

	header := blk.Header()
	blockHash := blk.Hash()
	blockNumber := blk.Number64().Uint64()
	baseFee := header.BaseFee64().ToBig()

	result := make([]*BlockReceipt, len(receipts))
	for i, receipt := range receipts {
		tx := txs[i]
		from := tx.From()

		// 计算有效 gas 价格
		gasPrice := new(big.Int).Add(baseFee, tx.EffectiveGasTipValue(header.BaseFee64()).ToBig())

		// 构建收据
		br := &BlockReceipt{
			BlockHash:         avmtypes.FromastHash(blockHash),
			BlockNumber:       hexutil.Uint64(blockNumber),
			TransactionHash:   avmtypes.FromastHash(tx.Hash()),
			TransactionIndex:  hexutil.Uint64(i),
			From:              *avmtypes.FromastAddress(from),
			GasUsed:           hexutil.Uint64(receipt.GasUsed),
			CumulativeGasUsed: hexutil.Uint64(receipt.CumulativeGasUsed),
			LogsBloom:         receipt.Bloom,
			Status:            hexutil.Uint64(receipt.Status),
			EffectiveGasPrice: hexutil.Uint64(gasPrice.Uint64()),
			Type:              hexutil.Uint64(tx.Type()),
		}

		// To 地址
		if to := tx.To(); to != nil {
			addr := avmtypes.FromastAddress(to)
			br.To = addr
		}

		// 合约地址
		if !receipt.ContractAddress.IsNull() {
			addr := avmtypes.FromastAddress(&receipt.ContractAddress)
			br.ContractAddress = addr
		}

		// 日志
		if receipt.Logs != nil {
			br.Logs = avmtypes.FromastLogs(receipt.Logs)
		} else {
			br.Logs = []*avmtypes.Log{}
		}

		// Post state root
		if len(receipt.PostState) > 0 {
			br.Root = receipt.PostState
		}

		result[i] = br
	}

	return result, nil
}

// =============================================================================
// 账户相关接口
// =============================================================================

// Accounts returns the collection of accounts this node manages.
// 返回此节点管理的账户列表。
func (s *BlockChainAPI) Accounts() []types.Address {
	if s.api.accountManager == nil {
		return []types.Address{}
	}
	return s.api.accountManager.Accounts()
}

// =============================================================================
// 其他工具接口
// =============================================================================

// GetProof returns the Merkle-proof for a given account and optionally some storage keys.
// 返回给定账户的 Merkle 证明（可选包含存储键）。
// 注意：完整实现需要 MPT 证明生成，这里提供基础结构。
func (s *BlockChainAPI) GetProof(ctx context.Context, address types.Address, storageKeys []string, blockNrOrHash jsonrpc.BlockNumberOrHash) (*AccountResult, error) {
	// TODO: 实现完整的 Merkle 证明
	// 目前返回基础信息，不包含实际证明

	tx, err := s.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state := s.api.State(tx, blockNrOrHash)
	if state == nil {
		return nil, nil
	}

	// 获取账户信息
	balance := state.GetBalance(address)
	nonce := state.GetNonce(address)
	code := state.GetCode(address)
	codeHash := types.Hash{}
	if len(code) > 0 {
		codeHash = types.BytesToHash(code)
	}

	// 构建存储证明
	storageProof := make([]StorageResult, len(storageKeys))
	for i, key := range storageKeys {
		var value uint256.Int
		k := types.HexToHash(key)
		state.GetState(address, &k, &value)
		storageProof[i] = StorageResult{
			Key:   key,
			Value: (*hexutil.Big)(value.ToBig()),
			Proof: []string{}, // TODO: 实际证明
		}
	}

	return &AccountResult{
		Address:      address,
		AccountProof: []string{}, // TODO: 实际证明
		Balance:      (*hexutil.Big)(balance.ToBig()),
		CodeHash:     codeHash,
		Nonce:        hexutil.Uint64(nonce),
		StorageHash:  types.Hash{}, // TODO: 实际存储根
		StorageProof: storageProof,
	}, nil
}

