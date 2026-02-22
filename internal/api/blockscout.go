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

// Blockscout compatibility APIs.
//
// This file provides additional RPC methods required by the Blockscout block explorer.
// Reference: https://docs.blockscout.com/
//
// Blockscout v9.3.3 (2026-02-06)
// Supports: EIP-1559, EIP-2930, EIP-4844, EIP-7560, EIP-7702

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

const maxBatchAddresses = 1000

// SyncProgress represents synchronization progress.
type SyncProgress struct {
	StartingBlock   hexutil.Uint64 `json:"startingBlock"`
	CurrentBlock    hexutil.Uint64 `json:"currentBlock"`
	HighestBlock    hexutil.Uint64 `json:"highestBlock"`
	PulledStates    hexutil.Uint64 `json:"pulledStates,omitempty"`
	KnownStates     hexutil.Uint64 `json:"knownStates,omitempty"`
	SyncedAccounts  hexutil.Uint64 `json:"syncedAccounts,omitempty"`
	SyncedStorage   hexutil.Uint64 `json:"syncedStorage,omitempty"`
	HealedTrienodes hexutil.Uint64 `json:"healedTrienodes,omitempty"`
}

// Syncing returns false when the node is fully synced, otherwise returns sync progress.
func (s *BlockChainAPI) Syncing() (interface{}, error) {
	currentBlock := s.api.BlockChain().CurrentBlock()
	if currentBlock == nil {
		return false, nil
	}
	currentHeight := currentBlock.Number64().Uint64()

	// TODO: obtain highest block from P2P layer for accurate sync status.
	// Currently simplified: if the node has block data, it reports as synced.
	highestBlock := currentHeight

	if currentHeight >= highestBlock {
		return false, nil
	}

	return &SyncProgress{
		StartingBlock: hexutil.Uint64(0),
		CurrentBlock:  hexutil.Uint64(currentHeight),
		HighestBlock:  hexutil.Uint64(highestBlock),
	}, nil
}

// Coinbase returns the current coinbase address.
func (s *BlockChainAPI) Coinbase() (types.Address, error) {
	currentBlock := s.api.BlockChain().CurrentBlock()
	if currentBlock == nil {
		return types.Address{}, nil
	}
	return currentBlock.Coinbase(), nil
}

// Mining returns whether this node is currently mining.
// TODO: integrate with miner module for actual status.
func (s *BlockChainAPI) Mining() bool {
	return false
}

// Hashrate returns the POW hashrate. N42 uses POS/POA, so always returns 0.
func (s *BlockChainAPI) Hashrate() hexutil.Uint64 {
	return hexutil.Uint64(0)
}

// getBlockByNumber resolves a block by its jsonrpc.BlockNumber.
// PendingBlockNumber and LatestBlockNumber both return the current head block.
// Negative numbers (other than the two special values above) are rejected.
func (s *BlockChainAPI) getBlockByNumber(number jsonrpc.BlockNumber) (block.IBlock, error) {
	if number == jsonrpc.PendingBlockNumber || number == jsonrpc.LatestBlockNumber {
		return s.api.BlockChain().CurrentBlock(), nil
	}
	if number < 0 {
		return nil, fmt.Errorf("invalid block number: %d", number)
	}
	return s.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(number.Int64())))
}

// GetBlockTransactionCountByNumber returns the number of transactions in a block
// matching the given block number.
func (s *BlockChainAPI) GetBlockTransactionCountByNumber(ctx context.Context, blockNr jsonrpc.BlockNumber) (*hexutil.Uint, error) {
	blk, err := s.getBlockByNumber(blockNr)
	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, nil
	}

	n := hexutil.Uint(len(blk.Transactions()))
	return &n, nil
}

// GetUncleCountByBlockNumber returns number of uncles in the block for the given block number.
// N42 uses POA/POS consensus and has no uncle blocks.
func (s *BlockChainAPI) GetUncleCountByBlockNumber(ctx context.Context, blockNr jsonrpc.BlockNumber) (*hexutil.Uint, error) {
	blk, err := s.getBlockByNumber(blockNr)
	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, nil
	}

	n := hexutil.Uint(0)
	return &n, nil
}

// GetUncleByBlockNumberAndIndex returns the uncle block for the given block hash and index.
// N42 uses POA/POS consensus and has no uncle blocks, so this always returns nil.
func (s *BlockChainAPI) GetUncleByBlockNumberAndIndex(ctx context.Context, blockNr jsonrpc.BlockNumber, index hexutil.Uint) (map[string]interface{}, error) {
	return nil, nil
}

// getBlockByNumber resolves a block by its jsonrpc.BlockNumber for TransactionAPI.
func (s *TransactionAPI) getBlockByNumber(number jsonrpc.BlockNumber) (block.IBlock, error) {
	if number == jsonrpc.PendingBlockNumber || number == jsonrpc.LatestBlockNumber {
		return s.api.BlockChain().CurrentBlock(), nil
	}
	if number < 0 {
		return nil, fmt.Errorf("invalid block number: %d", number)
	}
	return s.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(number.Int64())))
}

// GetTransactionByBlockNumberAndIndex returns the transaction for the given block number and index.
func (s *TransactionAPI) GetTransactionByBlockNumberAndIndex(ctx context.Context, blockNr jsonrpc.BlockNumber, index hexutil.Uint) *RPCTransaction {
	blk, err := s.getBlockByNumber(blockNr)
	if err != nil || blk == nil {
		return nil
	}

	txs := blk.Transactions()
	if int(index) >= len(txs) {
		return nil
	}

	header := blk.Header()
	if header == nil {
		return nil
	}
	headerBaseFee := header.BaseFee64()
	if headerBaseFee == nil {
		headerBaseFee = new(uint256.Int)
	}
	return newRPCTransaction(
		txs[index],
		blk.Hash(),
		blk.Number64().Uint64(),
		uint64(index),
		headerBaseFee.ToBig(),
	)
}

// BlockReceipt represents a single transaction receipt.
type BlockReceipt struct {
	BlockHash         avmcommon.Hash     `json:"blockHash"`
	BlockNumber       hexutil.Uint64     `json:"blockNumber"`
	TransactionHash   avmcommon.Hash     `json:"transactionHash"`
	TransactionIndex  hexutil.Uint64     `json:"transactionIndex"`
	From              avmcommon.Address  `json:"from"`
	To                *avmcommon.Address `json:"to"`
	GasUsed           hexutil.Uint64     `json:"gasUsed"`
	CumulativeGasUsed hexutil.Uint64     `json:"cumulativeGasUsed"`
	ContractAddress   *avmcommon.Address `json:"contractAddress"`
	Logs              []*avmtypes.Log    `json:"logs"`
	LogsBloom         block.Bloom        `json:"logsBloom"`
	Status            hexutil.Uint64     `json:"status"`
	EffectiveGasPrice *hexutil.Big       `json:"effectiveGasPrice"`
	Type              hexutil.Uint64     `json:"type"`
	Root              hexutil.Bytes      `json:"root,omitempty"`
	BlobGasUsed       *hexutil.Uint64    `json:"blobGasUsed,omitempty"`
	BlobGasPrice      *hexutil.Big       `json:"blobGasPrice,omitempty"`
}

// GetBlockReceipts returns all transaction receipts for a given block.
func (s *BlockChainAPI) GetBlockReceipts(ctx context.Context, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]*BlockReceipt, error) {
	var blk block.IBlock
	var err error

	if blockNr, ok := blockNrOrHash.Number(); ok {
		blk, err = s.getBlockByNumber(blockNr)
	} else if hash, ok := blockNrOrHash.Hash(); ok {
		blk, err = s.api.BlockChain().GetBlockByHash(types.Hash(hash))
	}

	if err != nil {
		return nil, err
	}
	if blk == nil {
		return nil, nil
	}

	receipts, err := s.api.BlockChain().GetReceipts(blk.Hash())
	if err != nil {
		return nil, err
	}

	txs := blk.Transactions()
	if len(receipts) != len(txs) {
		return nil, nil
	}

	header := blk.Header()
	blockHash := blk.Hash()
	blockNumber := blk.Number64().Uint64()
	headerBaseFee := header.BaseFee64()
	if headerBaseFee == nil {
		headerBaseFee = new(uint256.Int)
	}
	baseFee := headerBaseFee.ToBig()

	result := make([]*BlockReceipt, len(receipts))
	for i, receipt := range receipts {
		tx := txs[i]
		from := tx.From()

		gasPrice := new(big.Int).Add(baseFee, tx.EffectiveGasTipValue(headerBaseFee).ToBig())

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
			EffectiveGasPrice: (*hexutil.Big)(gasPrice),
			Type:              hexutil.Uint64(tx.Type()),
		}

		if to := tx.To(); to != nil {
			addr := avmtypes.FromastAddress(to)
			br.To = addr
		}

		if !receipt.ContractAddress.IsNull() {
			addr := avmtypes.FromastAddress(&receipt.ContractAddress)
			br.ContractAddress = addr
		}

		if receipt.Logs != nil {
			br.Logs = avmtypes.FromastLogs(receipt.Logs)
		} else {
			br.Logs = []*avmtypes.Log{}
		}

		if len(receipt.PostState) > 0 {
			br.Root = receipt.PostState
		}

		// Blob gas fields for EIP-4844 blob transactions
		if tx.Type() == transaction.BlobTxType {
			blobGasUsed := hexutil.Uint64(tx.BlobGas())
			br.BlobGasUsed = &blobGasUsed
			br.BlobGasPrice = (*hexutil.Big)(big.NewInt(1)) // minimum blob gas price
		}

		result[i] = br
	}

	return result, nil
}

// Accounts returns the collection of accounts this node manages.
func (s *BlockChainAPI) Accounts() []types.Address {
	if s.api.accountManager == nil {
		return []types.Address{}
	}
	return s.api.accountManager.Accounts()
}

// GetProof returns the Merkle-proof for a given account and optionally some storage keys.
// TODO: implement full MPT proof generation; currently returns basic account info without proofs.
func (s *BlockChainAPI) GetProof(ctx context.Context, address types.Address, storageKeys []string, blockNrOrHash jsonrpc.BlockNumberOrHash) (*AccountResult, error) {
	tx, err := s.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state := s.api.State(tx, blockNrOrHash)
	if state == nil {
		return nil, nil
	}

	balance := state.GetBalance(address)
	nonce := state.GetNonce(address)
	code := state.GetCode(address)
	codeHash := types.Hash{}
	if len(code) > 0 {
		codeHash = types.BytesToHash(code)
	}

	storageProof := make([]StorageResult, len(storageKeys))
	for i, key := range storageKeys {
		var value uint256.Int
		k := types.HexToHash(key)
		state.GetState(address, &k, &value)
		storageProof[i] = StorageResult{
			Key:   key,
			Value: (*hexutil.Big)(value.ToBig()),
			Proof: []string{}, // TODO: actual storage proof
		}
	}

	return &AccountResult{
		Address:      address,
		AccountProof: []string{}, // TODO: actual account proof
		Balance:      (*hexutil.Big)(balance.ToBig()),
		CodeHash:     codeHash,
		Nonce:        hexutil.Uint64(nonce),
		StorageHash:  types.Hash{}, // TODO: actual storage root
		StorageProof: storageProof,
	}, nil
}

// BlockscoutCompatibilityInfo contains Blockscout compatibility metadata.
type BlockscoutCompatibilityInfo struct {
	Compatible        bool                `json:"compatible"`
	BlockscoutVersion string              `json:"blockscoutVersion"`
	NodeVersion       string              `json:"nodeVersion"`
	SupportedAPIs     []string            `json:"supportedAPIs"`
	Features          *BlockscoutFeatures `json:"features"`
}

// BlockscoutFeatures lists supported Blockscout feature flags.
type BlockscoutFeatures struct {
	EIP1559            bool `json:"eip1559"`
	EIP2930            bool `json:"eip2930"`
	EIP4844            bool `json:"eip4844"`
	EIP7560            bool `json:"eip7560"`
	BlockReceipts      bool `json:"blockReceipts"`
	BlobBaseFee        bool `json:"blobBaseFee"`
	SimulateV1         bool `json:"simulateV1"`
	StateProofs        bool `json:"stateProofs"`
	BatchRequests      bool `json:"batchRequests"`
	WebSocketStreaming bool `json:"webSocketStreaming"`
	TraceAPI           bool `json:"traceAPI"`
	TxPoolAPI          bool `json:"txPoolAPI"`
	AccountEnumeration bool `json:"accountEnumeration"`
	UncleBlocks        bool `json:"uncleBlocks"`
	EIP7702            bool `json:"eip7702"`
}

// GetBlockscoutCompatibility returns Blockscout v9.3.3 compatibility info.
func (s *BlockChainAPI) GetBlockscoutCompatibility() *BlockscoutCompatibilityInfo {
	return &BlockscoutCompatibilityInfo{
		Compatible:        true,
		BlockscoutVersion: "9.3.3",
		NodeVersion:       "N42/v" + params.Version,
		SupportedAPIs: []string{
			"eth", "web3", "net", "txpool", "debug",
			"admin", "personal", "miner", "rpc",
		},
		Features: &BlockscoutFeatures{
			EIP1559:            true,
			EIP2930:            true,
			EIP4844:            true, // Blob transactions
			EIP7560:            true, // Transaction simulation
			BlockReceipts:      true,
			BlobBaseFee:        true, // eth_blobBaseFee
			SimulateV1:         true, // eth_simulateV1
			StateProofs:        true, // Partial support
			BatchRequests:      true,
			WebSocketStreaming: true,
			TraceAPI:           true,
			TxPoolAPI:          true,
			AccountEnumeration: true,
			UncleBlocks:        false, // POA/POS - no uncles
			EIP7702:            true,  // EIP-7702 SetCode authorization
		},
	}
}

// BatchGetBalance returns balances for multiple addresses in a single call.
func (s *BlockChainAPI) BatchGetBalance(ctx context.Context, addresses []types.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]*hexutil.Big, error) {
	if len(addresses) > maxBatchAddresses {
		return nil, fmt.Errorf("batch size %d exceeds maximum of %d", len(addresses), maxBatchAddresses)
	}
	tx, err := s.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state := s.api.State(tx, blockNrOrHash)
	if state == nil {
		return nil, nil
	}

	result := make([]*hexutil.Big, len(addresses))
	for i, address := range addresses {
		balance := state.GetBalance(address)
		result[i] = (*hexutil.Big)(balance.ToBig())
	}
	return result, nil
}

// BatchGetCode returns contract code for multiple addresses in a single call.
func (s *BlockChainAPI) BatchGetCode(ctx context.Context, addresses []types.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) ([]hexutil.Bytes, error) {
	if len(addresses) > maxBatchAddresses {
		return nil, fmt.Errorf("batch size %d exceeds maximum of %d", len(addresses), maxBatchAddresses)
	}
	tx, err := s.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	ibs := s.api.State(tx, blockNrOrHash)
	if ibs == nil {
		return nil, nil
	}

	result := make([]hexutil.Bytes, len(addresses))
	for i, address := range addresses {
		code := ibs.GetCode(address)
		result[i] = code
	}
	return result, nil
}

// BlobBaseFee returns the current blob base fee in wei (EIP-4844).
// N42 does not currently have ExcessBlobGas in headers, so this returns
// the minimum blob base fee (1 wei) for API compatibility.
func (s *BlockChainAPI) BlobBaseFee(ctx context.Context) (*hexutil.Big, error) {
	currentBlock := s.api.BlockChain().CurrentBlock()
	if currentBlock == nil {
		return nil, errors.New("no current block")
	}

	return (*hexutil.Big)(big.NewInt(1)), nil
}

// SimulateV1BlockOverride contains block-level overrides for simulation.
type SimulateV1BlockOverride struct {
	Number        *hexutil.Uint64 `json:"number,omitempty"`
	Time          *hexutil.Uint64 `json:"time,omitempty"`
	GasLimit      *hexutil.Uint64 `json:"gasLimit,omitempty"`
	FeeRecipient  *types.Address  `json:"feeRecipient,omitempty"`
	PrevRandao    *types.Hash     `json:"prevRandao,omitempty"`
	BaseFeePerGas *hexutil.Big    `json:"baseFeePerGas,omitempty"`
	BlobBaseFee   *hexutil.Big    `json:"blobBaseFee,omitempty"`
}

// SimulateV1BlockStateCalls represents a block with state overrides and calls
type SimulateV1BlockStateCalls struct {
	BlockOverrides *SimulateV1BlockOverride `json:"blockOverrides,omitempty"`
	StateOverrides *StateOverride           `json:"stateOverrides,omitempty"`
	Calls          []TransactionArgs        `json:"calls"`
}

// SimulateV1Options contains simulation options
type SimulateV1Options struct {
	TraceTransfers bool `json:"traceTransfers,omitempty"`
	Validation     bool `json:"validation,omitempty"`
}

// SimulateV1CallResult represents the result of a single simulated call
type SimulateV1CallResult struct {
	ReturnData hexutil.Bytes   `json:"returnData"`
	Logs       []*avmtypes.Log `json:"logs"`
	GasUsed    hexutil.Uint64  `json:"gasUsed"`
	Status     hexutil.Uint64  `json:"status"`
	Error      *SimulateError  `json:"error,omitempty"`
}

// SimulateError represents an error from simulation
type SimulateError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// SimulateV1BlockResult represents the result of simulating a block
type SimulateV1BlockResult struct {
	Number        hexutil.Uint64         `json:"number"`
	Hash          types.Hash             `json:"hash"`
	Timestamp     hexutil.Uint64         `json:"timestamp"`
	GasLimit      hexutil.Uint64         `json:"gasLimit"`
	GasUsed       hexutil.Uint64         `json:"gasUsed"`
	FeeRecipient  types.Address          `json:"feeRecipient"`
	BaseFeePerGas *hexutil.Big           `json:"baseFeePerGas"`
	PrevRandao    types.Hash             `json:"prevRandao"`
	Calls         []SimulateV1CallResult `json:"calls"`
}

// SimulateV1 executes a series of transactions against the state, returning the results.
// Implements EIP-7560 eth_simulateV1 for Blockscout compatibility.
func (s *BlockChainAPI) SimulateV1(ctx context.Context, blocks []SimulateV1BlockStateCalls, blockNrOrHash *jsonrpc.BlockNumberOrHash, opts *SimulateV1Options) ([]SimulateV1BlockResult, error) {
	if len(blocks) == 0 {
		return []SimulateV1BlockResult{}, nil
	}

	// Set default block
	bNrOrHash := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	if blockNrOrHash != nil {
		bNrOrHash = *blockNrOrHash
	}

	// Get base block
	var baseBlock block.IBlock
	var err error
	if blockNr, ok := bNrOrHash.Number(); ok {
		baseBlock, err = s.getBlockByNumber(blockNr)
	} else if hash, ok := bNrOrHash.Hash(); ok {
		baseBlock, err = s.api.BlockChain().GetBlockByHash(hash)
	}

	if err != nil || baseBlock == nil {
		return nil, errors.New("block not found")
	}

	baseHeader, ok := baseBlock.Header().(*block.Header)
	if !ok {
		return nil, errors.New("invalid header type")
	}

	results := make([]SimulateV1BlockResult, 0, len(blocks))

	// Process each simulated block
	for blockIdx, simBlock := range blocks {
		blockResult, err := s.simulateBlock(ctx, simBlock, baseHeader, uint64(blockIdx))
		if err != nil {
			return nil, err
		}
		results = append(results, *blockResult)
	}

	return results, nil
}

// simulateBlock simulates a single block with calls
func (s *BlockChainAPI) simulateBlock(ctx context.Context, simBlock SimulateV1BlockStateCalls, baseHeader *block.Header, blockOffset uint64) (*SimulateV1BlockResult, error) {
	// Set up block parameters
	blockNumber := baseHeader.Number64().Uint64() + blockOffset + 1
	blockTime := baseHeader.Time + (blockOffset+1)*12 // Assume 12s block time
	gasLimit := baseHeader.GasLimit
	feeRecipient := baseHeader.Coinbase
	baseFee := baseHeader.BaseFee
	prevRandao := baseHeader.MixDigest

	// Apply block overrides
	if simBlock.BlockOverrides != nil {
		if simBlock.BlockOverrides.Number != nil {
			blockNumber = uint64(*simBlock.BlockOverrides.Number)
		}
		if simBlock.BlockOverrides.Time != nil {
			blockTime = uint64(*simBlock.BlockOverrides.Time)
		}
		if simBlock.BlockOverrides.GasLimit != nil {
			gasLimit = uint64(*simBlock.BlockOverrides.GasLimit)
		}
		if simBlock.BlockOverrides.FeeRecipient != nil {
			feeRecipient = *simBlock.BlockOverrides.FeeRecipient
		}
		if simBlock.BlockOverrides.BaseFeePerGas != nil {
			baseFee = uint256.MustFromBig(simBlock.BlockOverrides.BaseFeePerGas.ToInt())
		}
		if simBlock.BlockOverrides.PrevRandao != nil {
			prevRandao = *simBlock.BlockOverrides.PrevRandao
		}
	}

	// Create simulated header
	simHeader := &block.Header{
		Number:     uint256.NewInt(blockNumber),
		Time:       blockTime,
		GasLimit:   gasLimit,
		Coinbase:   feeRecipient,
		BaseFee:    baseFee,
		MixDigest:  prevRandao,
		ParentHash: baseHeader.Hash(),
		Difficulty: baseHeader.Difficulty,
	}

	// Get state
	var ibs *state.IntraBlockState
	err := s.api.Database().View(ctx, func(t kv.Tx) error {
		stateReader := state.NewPlainState(t, baseHeader.Number64().Uint64())
		ibs = state.New(stateReader)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Apply state overrides
	if simBlock.StateOverrides != nil {
		if err := simBlock.StateOverrides.Apply(ibs); err != nil {
			return nil, err
		}
	}

	// Execute calls
	callResults := make([]SimulateV1CallResult, 0, len(simBlock.Calls))
	totalGasUsed := uint64(0)
	gp := new(common.GasPool).AddGas(gasLimit)

	for _, call := range simBlock.Calls {
		callResult := s.simulateCall(ctx, call, simHeader, ibs, gp)
		totalGasUsed += uint64(callResult.GasUsed)
		callResults = append(callResults, callResult)
	}

	return &SimulateV1BlockResult{
		Number:        hexutil.Uint64(blockNumber),
		Hash:          simHeader.Hash(),
		Timestamp:     hexutil.Uint64(blockTime),
		GasLimit:      hexutil.Uint64(gasLimit),
		GasUsed:       hexutil.Uint64(totalGasUsed),
		FeeRecipient:  feeRecipient,
		BaseFeePerGas: (*hexutil.Big)(baseFee.ToBig()),
		PrevRandao:    prevRandao,
		Calls:         callResults,
	}, nil
}

// simulateCall simulates a single call
func (s *BlockChainAPI) simulateCall(ctx context.Context, args TransactionArgs, header *block.Header, ibs *state.IntraBlockState, gp *common.GasPool) SimulateV1CallResult {
	// Set defaults
	if err := args.setDefaults(ctx, s.api); err != nil {
		return SimulateV1CallResult{
			Status: hexutil.Uint64(0),
			Error: &SimulateError{
				Code:    -32000,
				Message: err.Error(),
			},
		}
	}

	// Create message
	msg, err := args.ToMessage(s.api.RPCGasCap(), header.BaseFee.ToBig())
	if err != nil {
		return SimulateV1CallResult{
			Status: hexutil.Uint64(0),
			Error: &SimulateError{
				Code:    -32000,
				Message: err.Error(),
			},
		}
	}

	// Set up EVM
	vmConfig := vm.Config{NoBaseFee: true}
	txContext := internal.NewEVMTxContext(msg)
	blockContext := internal.NewEVMBlockContext(header, internal.GetHashFn(header, nil), s.api.engine, nil)
	evm := vm.NewEVM(blockContext, txContext, ibs, s.api.GetChainConfig(), vmConfig)

	// Execute
	result, err := internal.ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		return SimulateV1CallResult{
			Status: hexutil.Uint64(0),
			Error: &SimulateError{
				Code:    -32000,
				Message: err.Error(),
			},
		}
	}

	// Build result
	status := uint64(1)
	if result.Failed() {
		status = 0
	}

	// Get logs
	logs := ibs.GetLogs(types.Hash{})
	var rpcLogs []*avmtypes.Log
	if len(logs) > 0 {
		rpcLogs = avmtypes.FromastLogs(logs)
	} else {
		rpcLogs = []*avmtypes.Log{}
	}

	callResult := SimulateV1CallResult{
		ReturnData: result.Return(),
		Logs:       rpcLogs,
		GasUsed:    hexutil.Uint64(result.UsedGas),
		Status:     hexutil.Uint64(status),
	}

	if result.Err != nil {
		callResult.Error = &SimulateError{
			Code:    3, // Execution reverted
			Message: result.Err.Error(),
		}
	}

	return callResult
}
