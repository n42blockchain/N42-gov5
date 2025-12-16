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

// =============================================================================
// Debug Trace APIs - Step 3
// =============================================================================
//
// This file extends DebugAPI with transaction and block tracing capabilities.
// For full tracing functionality, see internal/tracers/api.go
//
// Reference: https://geth.ethereum.org/docs/interacting-with-geth/rpc/ns-debug

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/holiman/uint256"
	"github.com/ledgerwatch/erigon-lib/kv"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/tracers/logger"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
)

// =============================================================================
// Trace Configuration
// =============================================================================

// TraceConfig holds extra parameters to trace functions.
type TraceConfig struct {
	*logger.Config
	Tracer  *string `json:"tracer,omitempty"`
	Timeout *string `json:"timeout,omitempty"`
	Reexec  *uint64 `json:"reexec,omitempty"`
}

// TraceCallConfig is the config for traceCall API.
type TraceCallConfig struct {
	TraceConfig
	StateOverrides *StateOverride  `json:"stateOverrides,omitempty"`
	BlockOverrides *BlockOverrides `json:"blockOverrides,omitempty"`
}

// ExecutionResult groups all structured logs emitted by the EVM
// while replaying a transaction in debug mode as well as transaction
// execution status, the amount of gas used and the return value.
type ExecutionResult struct {
	Gas         uint64         `json:"gas"`
	Failed      bool           `json:"failed"`
	ReturnValue string         `json:"returnValue"`
	StructLogs  []StructLogRes `json:"structLogs"`
}

// StructLogRes stores a structured log emitted by the EVM while replaying a
// transaction in debug mode.
type StructLogRes struct {
	Pc            uint64             `json:"pc"`
	Op            string             `json:"op"`
	Gas           uint64             `json:"gas"`
	GasCost       uint64             `json:"gasCost"`
	Depth         int                `json:"depth"`
	Error         string             `json:"error,omitempty"`
	Stack         *[]string          `json:"stack,omitempty"`
	Memory        *[]string          `json:"memory,omitempty"`
	Storage       *map[string]string `json:"storage,omitempty"`
	RefundCounter uint64             `json:"refund,omitempty"`
}

// =============================================================================
// Transaction Tracing
// =============================================================================

// TraceTransaction returns the structured logs created during the execution of EVM
// and returns them as a JSON object.
func (debug *DebugAPI) TraceTransaction(ctx context.Context, hash types.Hash, config *TraceConfig) (interface{}, error) {
	// Find the transaction
	var (
		tx          *transaction.Transaction
		blockHash   types.Hash
		blockNumber uint64
		index       uint64
	)

	err := debug.api.Database().View(ctx, func(t kv.Tx) error {
		var err error
		tx, blockHash, blockNumber, index, err = rawdb.ReadTransactionByHash(t, hash)
		return err
	})
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, errors.New("transaction not found")
	}
	_ = blockNumber // Used for context

	// Get the block
	blk, err := debug.api.BlockChain().GetBlockByHash(blockHash)
	if err != nil || blk == nil {
		return nil, fmt.Errorf("block %x not found", blockHash)
	}

	return debug.traceTx(ctx, tx, blk, int(index), config)
}

// traceTx configures a new tracer according to the provided configuration, and
// executes the given message in the provided environment.
func (debug *DebugAPI) traceTx(ctx context.Context, tx *transaction.Transaction, blk block.IBlock, txIndex int, config *TraceConfig) (interface{}, error) {
	// Set up the tracer
	var (
		tracer  vm.EVMLogger
		timeout = 5 * time.Second
	)

	// Parse timeout from config
	if config != nil && config.Timeout != nil {
		if parsed, err := time.ParseDuration(*config.Timeout); err == nil {
			timeout = parsed
		}
	}

	// Create the tracer
	if config == nil || config.Tracer == nil {
		// Default struct logger
		logConfig := &logger.Config{}
		if config != nil && config.Config != nil {
			logConfig = config.Config
		}
		tracer = logger.NewStructLogger(logConfig)
	} else {
		// Custom tracer (not fully supported yet)
		return nil, errors.New("custom tracers not yet supported")
	}

	// Get state at the beginning of the block
	var ibs *state.IntraBlockState
	err := debug.api.Database().View(ctx, func(t kv.Tx) error {
		blockNum := blk.Number64().Uint64()
		if blockNum > 0 {
			blockNum--
		}
		stateReader := state.NewPlainState(t, blockNum)
		ibs = state.New(stateReader)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Get header as concrete type
	header, ok := blk.Header().(*block.Header)
	if !ok {
		return nil, errors.New("invalid header type")
	}

	// Replay transactions up to the target
	txs := blk.Transactions()
	signer := transaction.MakeSigner(debug.api.GetChainConfig(), header.Number64().ToBig())

	for i := 0; i < txIndex; i++ {
		msg, err := txs[i].AsMessage(signer, header.BaseFee64())
		if err != nil {
			return nil, err
		}
		vmConfig := vm.Config{}
		txContext := internal.NewEVMTxContext(msg)
		blockContext := internal.NewEVMBlockContext(header, internal.GetHashFn(header, nil), debug.api.engine, nil)
		evm := vm.NewEVM(blockContext, txContext, ibs, debug.api.GetChainConfig(), vmConfig)

		gp := new(common.GasPool).AddGas(header.GasLimit)
		result, err := internal.ApplyMessage(evm, msg, gp, true, false)
		if err != nil {
			return nil, err
		}
		ibs.FinalizeTx(debug.api.GetChainConfig().Rules(header.Number64().Uint64()), state.NewNoopWriter())
		_ = result
	}

	// Execute the target transaction with tracing
	msg, err := tx.AsMessage(signer, header.BaseFee64())
	if err != nil {
		return nil, err
	}

	vmConfig := vm.Config{Tracer: tracer, NoBaseFee: true}
	txContext := internal.NewEVMTxContext(msg)
	blockContext := internal.NewEVMBlockContext(header, internal.GetHashFn(header, nil), debug.api.engine, nil)
	evm := vm.NewEVM(blockContext, txContext, ibs, debug.api.GetChainConfig(), vmConfig)

	// Set timeout
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	go func() {
		<-deadlineCtx.Done()
		evm.Cancel()
	}()

	gp := new(common.GasPool).AddGas(header.GasLimit)
	result, err := internal.ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		return nil, err
	}

	// Format the result based on tracer type
	if structLogger, ok := tracer.(*logger.StructLogger); ok {
		return formatLogs(structLogger.StructLogs(), result.UsedGas, result.Failed(), result.Return()), nil
	}

	return nil, errors.New("unsupported tracer type")
}

// formatLogs formats EVM returned structured logs for json output.
func formatLogs(logs []logger.StructLog, gas uint64, failed bool, returnValue []byte) *ExecutionResult {
	formatted := make([]StructLogRes, len(logs))
	for i, log := range logs {
		formatted[i] = StructLogRes{
			Pc:            log.Pc,
			Op:            log.Op.String(),
			Gas:           log.Gas,
			GasCost:       log.GasCost,
			Depth:         log.Depth,
			RefundCounter: log.RefundCounter,
		}
		if log.Err != nil {
			formatted[i].Error = log.Err.Error()
		}
		if len(log.Stack) > 0 {
			stack := make([]string, len(log.Stack))
			for j, val := range log.Stack {
				stack[j] = val.Hex()
			}
			formatted[i].Stack = &stack
		}
		if len(log.Memory) > 0 {
			memory := make([]string, 0, (len(log.Memory)+31)/32)
			for j := 0; j+32 <= len(log.Memory); j += 32 {
				memory = append(memory, fmt.Sprintf("%x", log.Memory[j:j+32]))
			}
			formatted[i].Memory = &memory
		}
		if len(log.Storage) > 0 {
			storage := make(map[string]string)
			for k, v := range log.Storage {
				storage[k.Hex()] = v.Hex()
			}
			formatted[i].Storage = &storage
		}
	}
	return &ExecutionResult{
		Gas:         gas,
		Failed:      failed,
		ReturnValue: hexutil.Encode(returnValue),
		StructLogs:  formatted,
	}
}

// =============================================================================
// Block Tracing
// =============================================================================

// txTraceResult is the result of a single transaction trace.
type txTraceResult struct {
	TxHash types.Hash  `json:"txHash"`
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// TraceBlockByNumber returns the structured logs created during the execution of
// EVM and returns them as a JSON object.
func (debug *DebugAPI) TraceBlockByNumber(ctx context.Context, number jsonrpc.BlockNumber, config *TraceConfig) ([]*txTraceResult, error) {
	var blk block.IBlock
	var err error

	if number == jsonrpc.LatestBlockNumber || number == jsonrpc.PendingBlockNumber {
		blk = debug.api.BlockChain().CurrentBlock()
	} else {
		blk, err = debug.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(number.Int64())))
	}

	if err != nil || blk == nil {
		return nil, fmt.Errorf("block #%d not found", number)
	}

	return debug.traceBlock(ctx, blk, config)
}

// TraceBlockByHash returns the structured logs created during the execution of
// EVM and returns them as a JSON object.
func (debug *DebugAPI) TraceBlockByHash(ctx context.Context, hash types.Hash, config *TraceConfig) ([]*txTraceResult, error) {
	blk, err := debug.api.BlockChain().GetBlockByHash(hash)
	if err != nil || blk == nil {
		return nil, fmt.Errorf("block %x not found", hash)
	}
	return debug.traceBlock(ctx, blk, config)
}

// traceBlock configures a new tracer according to the provided configuration, and
// executes all the transactions contained within.
func (debug *DebugAPI) traceBlock(ctx context.Context, blk block.IBlock, config *TraceConfig) ([]*txTraceResult, error) {
	txs := blk.Transactions()
	results := make([]*txTraceResult, len(txs))

	for i, tx := range txs {
		result, err := debug.traceTx(ctx, tx, blk, i, config)
		results[i] = &txTraceResult{
			TxHash: tx.Hash(),
		}
		if err != nil {
			results[i].Error = err.Error()
		} else {
			results[i].Result = result
		}
	}

	return results, nil
}

// =============================================================================
// Call Tracing
// =============================================================================

// TraceCall lets you trace a given eth_call. It collects the structured logs
// created during the execution of EVM if the given transaction was added on
// top of the provided block and returns them as a JSON object.
func (debug *DebugAPI) TraceCall(ctx context.Context, args TransactionArgs, blockNrOrHash jsonrpc.BlockNumberOrHash, config *TraceCallConfig) (interface{}, error) {
	// Get the block
	var blk block.IBlock
	var err error

	if blockNr, ok := blockNrOrHash.Number(); ok {
		if blockNr == jsonrpc.LatestBlockNumber || blockNr == jsonrpc.PendingBlockNumber {
			blk = debug.api.BlockChain().CurrentBlock()
		} else {
			blk, err = debug.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(blockNr.Int64())))
		}
	} else if hash, ok := blockNrOrHash.Hash(); ok {
		blk, err = debug.api.BlockChain().GetBlockByHash(hash)
	}

	if err != nil || blk == nil {
		return nil, errors.New("block not found")
	}

	header, ok := blk.Header().(*block.Header)
	if !ok {
		return nil, errors.New("invalid header type")
	}

	// Set up the tracer
	var tracer vm.EVMLogger
	timeout := 5 * time.Second

	if config != nil && config.Timeout != nil {
		if parsed, err := time.ParseDuration(*config.Timeout); err == nil {
			timeout = parsed
		}
	}

	// Create the tracer
	if config == nil || config.Tracer == nil {
		logConfig := &logger.Config{}
		if config != nil && config.Config != nil {
			logConfig = config.Config
		}
		tracer = logger.NewStructLogger(logConfig)
	} else {
		return nil, errors.New("custom tracers not yet supported")
	}

	// Get state
	var ibs *state.IntraBlockState
	err = debug.api.Database().View(ctx, func(t kv.Tx) error {
		stateReader := state.NewPlainState(t, header.Number64().Uint64())
		ibs = state.New(stateReader)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Apply state overrides
	if config != nil && config.StateOverrides != nil {
		if err := config.StateOverrides.Apply(ibs); err != nil {
			return nil, err
		}
	}

	// Create the message
	msg, err := args.ToMessage(debug.api.RPCGasCap(), header.BaseFee64().ToBig())
	if err != nil {
		return nil, err
	}

	// Set up EVM
	vmConfig := vm.Config{Tracer: tracer, NoBaseFee: true}
	txContext := internal.NewEVMTxContext(msg)
	blockContext := internal.NewEVMBlockContext(header, internal.GetHashFn(header, nil), debug.api.engine, nil)

	// Apply block overrides
	if config != nil && config.BlockOverrides != nil {
		config.BlockOverrides.Apply(&blockContext)
	}

	evm := vm.NewEVM(blockContext, txContext, ibs, debug.api.GetChainConfig(), vmConfig)

	// Set timeout
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	go func() {
		<-deadlineCtx.Done()
		evm.Cancel()
	}()

	// Execute
	gp := new(common.GasPool).AddGas(header.GasLimit)
	result, err := internal.ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		return nil, err
	}

	// Format result
	if structLogger, ok := tracer.(*logger.StructLogger); ok {
		return formatLogs(structLogger.StructLogs(), result.UsedGas, result.Failed(), result.Return()), nil
	}

	return nil, errors.New("unsupported tracer type")
}

// =============================================================================
// Access List Creation
// =============================================================================

// AccessListResult returns an optional access list
type AccessListResult struct {
	Accesslist *AccessList    `json:"accessList,omitempty"`
	Error      string         `json:"error,omitempty"`
	GasUsed    hexutil.Uint64 `json:"gasUsed"`
}

// AccessList is a list of addresses and storage keys
type AccessList []AccessTuple

// AccessTuple is an address and storage keys pair
type AccessTuple struct {
	Address     types.Address `json:"address"`
	StorageKeys []types.Hash  `json:"storageKeys"`
}

// CreateAccessList creates an EIP-2930 type AccessList for the given transaction.
// Note: This is a simplified implementation that returns an empty access list.
// Full implementation requires compatible AccessListTracer interface.
func (s *BlockChainAPI) CreateAccessList(ctx context.Context, args TransactionArgs, blockNrOrHash *jsonrpc.BlockNumberOrHash) (*AccessListResult, error) {
	// Set default block
	bNrOrHash := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.PendingBlockNumber)
	if blockNrOrHash != nil {
		bNrOrHash = *blockNrOrHash
	}

	// Get block
	var blk block.IBlock
	var err error

	if blockNr, ok := bNrOrHash.Number(); ok {
		if blockNr == jsonrpc.LatestBlockNumber || blockNr == jsonrpc.PendingBlockNumber {
			blk = s.api.BlockChain().CurrentBlock()
		} else {
			blk, err = s.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(blockNr.Int64())))
		}
	} else if hash, ok := bNrOrHash.Hash(); ok {
		blk, err = s.api.BlockChain().GetBlockByHash(hash)
	}

	if err != nil || blk == nil {
		return nil, errors.New("block not found")
	}

	header, ok := blk.Header().(*block.Header)
	if !ok {
		return nil, errors.New("invalid header type")
	}

	// Get state
	var ibs *state.IntraBlockState
	err = s.api.Database().View(ctx, func(t kv.Tx) error {
		stateReader := state.NewPlainState(t, header.Number64().Uint64())
		ibs = state.New(stateReader)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Create message
	if err := args.setDefaults(ctx, s.api); err != nil {
		return nil, err
	}
	msg, err := args.ToMessage(s.api.RPCGasCap(), header.BaseFee64().ToBig())
	if err != nil {
		return nil, err
	}

	// Set up EVM without tracer for gas estimation
	vmConfig := vm.Config{NoBaseFee: true}
	txContext := internal.NewEVMTxContext(msg)
	blockContext := internal.NewEVMBlockContext(header, internal.GetHashFn(header, nil), s.api.engine, nil)
	evm := vm.NewEVM(blockContext, txContext, ibs, s.api.GetChainConfig(), vmConfig)

	// Execute to get gas used
	gp := new(common.GasPool).AddGas(header.GasLimit)
	result, err := internal.ApplyMessage(evm, msg, gp, true, false)
	if err != nil {
		return &AccessListResult{Error: err.Error()}, nil
	}

	// Return empty access list with gas used
	// TODO: Implement full access list generation when AccessListTracer is compatible
	emptyList := make(AccessList, 0)
	return &AccessListResult{
		Accesslist: &emptyList,
		GasUsed:    hexutil.Uint64(result.UsedGas),
	}, nil
}

// =============================================================================
// Debug Utility Functions
// =============================================================================

// GetBlockRlp retrieves the RLP encoded for of a single block.
func (debug *DebugAPI) GetBlockRlp(ctx context.Context, number uint64) (hexutil.Bytes, error) {
	blk, err := debug.api.BlockChain().GetBlockByNumber(uint256.NewInt(number))
	if err != nil || blk == nil {
		return nil, fmt.Errorf("block #%d not found", number)
	}

	encoded, err := rlp.EncodeToBytes(blk)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// GetHeaderRlp retrieves the RLP encoded for of a single header.
func (debug *DebugAPI) GetHeaderRlp(ctx context.Context, number uint64) (hexutil.Bytes, error) {
	header := debug.api.BlockChain().GetHeaderByNumber(uint256.NewInt(number))
	if header == nil {
		return nil, fmt.Errorf("header #%d not found", number)
	}

	encoded, err := rlp.EncodeToBytes(header)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

// PrintBlock retrieves a block and returns its pretty printed form.
func (debug *DebugAPI) PrintBlock(ctx context.Context, number uint64) (string, error) {
	blk, err := debug.api.BlockChain().GetBlockByNumber(uint256.NewInt(number))
	if err != nil || blk == nil {
		return "", fmt.Errorf("block #%d not found", number)
	}

	// Pretty print
	data, err := json.MarshalIndent(blk, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// =============================================================================
// Bad Blocks and Storage Range
// =============================================================================

// BadBlockArgs represents the entries in the list returned when bad blocks are queried.
type BadBlockArgs struct {
	Hash   types.Hash             `json:"hash"`
	Block  map[string]interface{} `json:"block"`
	RLP    string                 `json:"rlp"`
	Reason string                 `json:"reason,omitempty"`
}

// GetBadBlocks returns a list of the last 'bad blocks' that the client has seen.
// Bad blocks are blocks that failed verification.
func (debug *DebugAPI) GetBadBlocks(ctx context.Context) ([]*BadBlockArgs, error) {
	// N42 doesn't maintain a bad blocks cache by default
	// Return empty list as placeholder
	return []*BadBlockArgs{}, nil
}

// StorageRangeResult represents the result of a storage range query.
type StorageRangeResult struct {
	Storage map[types.Hash]StorageEntry `json:"storage"`
	NextKey *types.Hash                 `json:"nextKey"` // nil if no more keys
}

// StorageEntry represents a single storage entry.
type StorageEntry struct {
	Key   *types.Hash `json:"key"`
	Value types.Hash  `json:"value"`
}

// StorageRangeAt returns the storage at the given block height and transaction index.
func (debug *DebugAPI) StorageRangeAt(ctx context.Context, blockHashOrNumber interface{}, txIndex int, contractAddress types.Address, keyStart types.Hash, maxResult int) (*StorageRangeResult, error) {
	var (
		blk block.IBlock
		err error
	)

	// Parse block identifier
	switch v := blockHashOrNumber.(type) {
	case string:
		// Try to parse as hash first
		if len(v) == 66 && v[:2] == "0x" {
			hash := types.HexToHash(v)
			blk, err = debug.api.BlockChain().GetBlockByHash(hash)
		} else {
			// Parse as number
			num, parseErr := strconv.ParseUint(v, 0, 64)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid block identifier: %v", v)
			}
			blk, err = debug.api.BlockChain().GetBlockByNumber(uint256.NewInt(num))
		}
	case float64:
		blk, err = debug.api.BlockChain().GetBlockByNumber(uint256.NewInt(uint64(v)))
	default:
		return nil, fmt.Errorf("invalid block identifier type")
	}

	if err != nil || blk == nil {
		return nil, fmt.Errorf("block not found")
	}

	// Get state at the block
	var stateDB *state.IntraBlockState
	if err := debug.api.Database().View(ctx, func(tx kv.Tx) error {
		ibs := debug.api.State(tx, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(blk.Number64().Uint64())))
		if ibs != nil {
			stateDB = ibs.(*state.IntraBlockState)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if stateDB == nil {
		return nil, fmt.Errorf("state not available")
	}

	// Note: Full storage iteration requires trie access
	// This is a simplified implementation that returns empty storage
	// Full implementation would need access to storage trie iterator
	result := &StorageRangeResult{
		Storage: make(map[types.Hash]StorageEntry),
		NextKey: nil,
	}

	return result, nil
}

// AccountRangeResult represents the result of an account range query.
type AccountRangeResult struct {
	Accounts map[types.Address]AccountRangeEntry `json:"accounts"`
	NextKey  types.Address                       `json:"next"`
}

// AccountRangeEntry represents a single account in the result.
type AccountRangeEntry struct {
	Balance  string     `json:"balance"`
	Nonce    uint64     `json:"nonce"`
	Root     types.Hash `json:"root"`
	CodeHash types.Hash `json:"codeHash"`
}

// AccountRange enumerates accounts starting at a given point.
func (debug *DebugAPI) AccountRange(ctx context.Context, blockNrOrHash jsonrpc.BlockNumberOrHash, start []byte, maxResults int, nocode, nostorage, incompletes bool) (*AccountRangeResult, error) {
	// Simplified implementation - returns empty result
	// Full implementation would need trie iteration
	return &AccountRangeResult{
		Accounts: make(map[types.Address]AccountRangeEntry),
		NextKey:  types.Address{},
	}, nil
}

// SetTrieFlushInterval configures how often in-memory trie nodes are persisted.
func (debug *DebugAPI) SetTrieFlushInterval(interval string) error {
	// No-op in N42
	return nil
}

// GetTrieFlushInterval returns the current trie flush interval.
func (debug *DebugAPI) GetTrieFlushInterval() string {
	return "1h0m0s" // Default value
}
