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

package tracers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	common2 "github.com/n42blockchain/N42/common"
	types "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	common "github.com/n42blockchain/N42/common/types"
	core "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/tracers/logger"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	rpc "github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

const (
	// defaultTraceTimeout is the amount of time a single transaction can execute
	// by default before being forcefully aborted.
	defaultTraceTimeout = 5 * time.Second

	// defaultTraceReexec is the number of blocks the tracer is willing to go back
	// and reexecute to produce missing historical state necessary to run a specific
	// trace.
	defaultTraceReexec = uint64(128)

	// defaultTracechainMemLimit is the size of the triedb, at which traceChain
	// switches over and tries to use a disk-backed database instead of building
	// on top of memory.
	// For non-archive nodes, this limit _will_ be overblown, as disk-backed tries
	// will only be found every ~15K blocks or so.
	defaultTracechainMemLimit = common.StorageSize(500 * 1024 * 1024)

	// maximumPendingTraceStates is the maximum number of states allowed waiting
	// for tracing. The creation of trace state will be paused if the unused
	// trace states exceed this limit.
	maximumPendingTraceStates = 128
)

var errTxNotFound = errors.New("transaction not found")

// StateReleaseFunc is used to deallocate resources held by constructing a
// historical state for tracing purposes.
type StateReleaseFunc func()

// Backend interface provides the common API services (that are provided by
// both full and light clients) with access to necessary functions.
type Backend interface {
	HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error)
	HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Header, error)
	BlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error)
	BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, error)
	GetTransaction(ctx context.Context, txHash common.Hash) (*transaction.Transaction, common.Hash, uint64, uint64, error)
	RPCGasCap() uint64
	ChainConfig() *params.ChainConfig
	Engine() consensus.Engine
	ChainDb() kv.RwDB
	StateAtBlock(ctx context.Context, tx kv.Tx, block *types.Block /*, reexec uint64, base *state.IntraBlockState, readOnly bool, preferDisk bool*/) (*state.IntraBlockState, error)
	StateAtTransaction(ctx context.Context, tx kv.Tx, block *types.Block, txIndex int /*, reexec uint64*/) (*transaction.Message, evmtypes.BlockContext, *state.IntraBlockState, error)
}

// API is the collection of tracing APIs exposed over the private debugging endpoint.
type API struct {
	backend Backend
}

// NewAPI creates a new API definition for the tracing methods of the Ethereum service.
func NewAPI(backend Backend) *API {
	return &API{backend: backend}
}

type chainContext struct {
	api *API
	ctx context.Context
}

func (context *chainContext) Engine() consensus.Engine {
	return context.api.backend.Engine()
}

func (context *chainContext) GetHeader(hash common.Hash, number uint64) *types.Header {
	header, err := context.api.backend.HeaderByNumber(context.ctx, rpc.BlockNumber(number))
	if err != nil {
		return nil
	}
	if header.Hash() == hash {
		return header
	}
	header, err = context.api.backend.HeaderByHash(context.ctx, hash)
	if err != nil {
		return nil
	}
	return header
}

// blockByNumber is the wrapper of the chain access function offered by the backend.
// It will return an error if the block is not found.
func (api *API) blockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, error) {
	block, err := api.backend.BlockByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	if block == nil {
		return nil, fmt.Errorf("block #%d not found", number)
	}
	return block, nil
}

// blockByHash is the wrapper of the chain access function offered by the backend.
// It will return an error if the block is not found.
func (api *API) blockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	block, err := api.backend.BlockByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	if block == nil {
		return nil, fmt.Errorf("block %s not found", hash.Hex())
	}
	return block, nil
}

// blockByNumberAndHash is the wrapper of the chain access function offered by
// the backend. It will return an error if the block is not found.
//
// Note this function is friendly for the light client which can only retrieve the
// historical(before the CHT) header/block by number.
func (api *API) blockByNumberAndHash(ctx context.Context, number rpc.BlockNumber, hash common.Hash) (*types.Block, error) {
	block, err := api.blockByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	if block.Hash() == hash {
		return block, nil
	}
	return api.blockByHash(ctx, hash)
}

// TraceConfig holds extra parameters to trace functions.
type TraceConfig struct {
	*logger.Config
	Tracer  *string
	Timeout *string
	Reexec  *uint64
	// Config specific to given tracer. Note struct logger
	// config are historically embedded in main object.
	TracerConfig json.RawMessage
}

// TraceCallConfig is the config for traceCall API. It holds one more
// field to override the state for tracing.
type TraceCallConfig struct {
	TraceConfig
	StateOverrides *api.StateOverride
	BlockOverrides *api.BlockOverrides
}

// StdTraceConfig holds extra parameters to standard-json trace functions.
type StdTraceConfig struct {
	logger.Config
	Reexec *uint64
	TxHash common.Hash
}

// txTraceResult is the result of a single transaction trace.
type txTraceResult struct {
	Result interface{} `json:"result,omitempty"` // Trace results produced by the tracer
	Error  string      `json:"error,omitempty"`  // Trace failure produced by the tracer
}

// blockTraceTask represents a single block trace task when an entire chain is
// being traced.
type blockTraceTask struct {
	statedb *state.IntraBlockState // Intermediate state prepped for tracing
	block   *types.Block           // Block to trace the transactions from
	release StateReleaseFunc       // The function to release the held resource for this task
	results []*txTraceResult       // Trace results produced by the task
}

// blockTraceResult represents the results of tracing a single block when an entire
// chain is being traced.
type blockTraceResult struct {
	Block  hexutil.Uint64   `json:"block"`  // Block number corresponding to this trace
	Hash   common.Hash      `json:"hash"`   // Block hash corresponding to this trace
	Traces []*txTraceResult `json:"traces"` // Trace results produced by the task
}

// txTraceTask represents a single transaction trace task when an entire block
// is being traced.
type txTraceTask struct {
	statedb *state.IntraBlockState // Intermediate state prepped for tracing
	index   int                    // Transaction offset in the block
}

// TraceBlockByNumber returns the structured logs created during the execution of
// EVM and returns them as a JSON object.
func (api *API) TraceBlockByNumber(ctx context.Context, number rpc.BlockNumber, config *TraceConfig) ([]*txTraceResult, error) {
	block, err := api.blockByNumber(ctx, number)
	if err != nil {
		return nil, err
	}
	return api.traceBlock(ctx, block, config)
}

// TraceBlockByHash returns the structured logs created during the execution of
// EVM and returns them as a JSON object.
func (api *API) TraceBlockByHash(ctx context.Context, hash common.Hash, config *TraceConfig) ([]*txTraceResult, error) {
	block, err := api.blockByHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	return api.traceBlock(ctx, block, config)
}

// TraceBlock returns the structured logs created during the execution of EVM
// and returns them as a JSON object.
func (api *API) TraceBlock(ctx context.Context, blob hexutil.Bytes, config *TraceConfig) ([]*txTraceResult, error) {
	block := new(types.Block)
	if err := rlp.Decode(bytes.NewReader(blob), block); err != nil {
		return nil, fmt.Errorf("could not decode block: %w", err)
	}
	return api.traceBlock(ctx, block, config)
}

// traceBlock configures a new tracer according to the provided configuration, and
// executes all the transactions contained within. The return value will be one item
// per transaction, dependent on the requested tracer.
func (api *API) traceBlock(ctx context.Context, block *types.Block, config *TraceConfig) ([]*txTraceResult, error) {
	if block.Number64().Uint64() == 0 {
		return nil, errors.New("genesis is not traceable")
	}
	// Prepare base state
	parent, err := api.blockByNumberAndHash(ctx, rpc.BlockNumber(block.Number64().Uint64()-1), block.ParentHash())
	if err != nil {
		return nil, err
	}
	//reexec := defaultTraceReexec
	//if config != nil && config.Reexec != nil {
	//	reexec = *config.Reexec
	//}

	rtx, err := api.backend.ChainDb().BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer rtx.Rollback()

	statedb, err := api.backend.StateAtBlock(ctx, rtx, parent /*, reexec, nil, true, false*/)
	if err != nil {
		return nil, err
	}
	// defer release()

	// JS tracers have high overhead. In this case run a parallel
	// process that generates states in one thread and traces txes
	// in separate worker threads.
	//if config != nil && config.Tracer != nil && *config.Tracer != "" {
	//	if isJS := DefaultDirectory.IsJS(*config.Tracer); isJS {
	//		return api.traceBlockParallel(ctx, block, statedb, config)
	//	}
	//}

	// Native tracers have low overhead
	txs := block.Transactions()
	blockHash := block.Hash()
	blockHeader, ok := block.Header().(*types.Header)
	if !ok {
		return nil, errors.New("unexpected block header type")
	}
	var (
		blockCtx = core.NewEVMBlockContext(blockHeader, core.GetHashFn(blockHeader, api.chainContext(ctx).GetHeader), api.chainContext(ctx).Engine(), nil)
		signer   = transaction.MakeSigner(api.backend.ChainConfig(), block.Number64().ToBig())
		results  = make([]*txTraceResult, len(txs))
	)
	for i, tx := range txs {
		// Generate the next state snapshot fast without tracing
		msg, _ := tx.AsMessage(signer, block.BaseFee64())
		txctx := &Context{
			BlockHash:   blockHash,
			BlockNumber: block.Number64().ToBig(),
			TxIndex:     i,
			TxHash:      tx.Hash(),
		}
		res, err := api.traceTx(ctx, &msg, txctx, blockCtx, statedb, config)
		if err != nil {
			return nil, err
		}
		results[i] = &txTraceResult{Result: res}
		// Finalize the state so any modifications are written to the trie
		// Only delete empty objects if EIP158/161 (a.k.a Spurious Dragon) is in effect
		//statedb.SoftFinalise(is158)
		statedb.FinalizeTx(api.backend.ChainConfig().Rules(block.Number64().Uint64()), state.NewNoopWriter())
	}
	return results, nil
}

// chainContext constructs the context reader which is used by the evm for reading
// the necessary chain context.
func (api *API) chainContext(ctx context.Context) core.ChainContext {
	return &chainContext{api: api, ctx: ctx}
}

// containsTx reports whether the transaction with a certain hash
// is contained within the specified block.
func containsTx(block *types.Block, hash common.Hash) bool {
	for _, tx := range block.Transactions() {
		if tx.Hash() == hash {
			return true
		}
	}
	return false
}

// TraceTransaction returns the structured logs created during the execution of EVM
// and returns them as a JSON object.
func (api *API) TraceTransaction(ctx context.Context, hash common.Hash, config *TraceConfig) (interface{}, error) {
	tx, blockHash, blockNumber, index, err := api.backend.GetTransaction(ctx, hash)
	if err != nil {
		return nil, err
	}
	// Only mined txes are supported
	if tx == nil {
		return nil, errTxNotFound
	}
	// It shouldn't happen in practice.
	if blockNumber == 0 {
		return nil, errors.New("genesis is not traceable")
	}
	//reexec := defaultTraceReexec
	//if config != nil && config.Reexec != nil {
	//	reexec = *config.Reexec
	//}
	block, err := api.blockByNumberAndHash(ctx, rpc.BlockNumber(blockNumber), blockHash)
	if err != nil {
		return nil, err
	}

	dbTx, err := api.backend.ChainDb().BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer dbTx.Rollback()

	msg, vmctx, statedb, err := api.backend.StateAtTransaction(ctx, dbTx, block, int(index))
	if err != nil {
		return nil, err
	}
	//defer release()

	txctx := &Context{
		BlockHash:   blockHash,
		BlockNumber: block.Number64().ToBig(),
		TxIndex:     int(index),
		TxHash:      hash,
	}
	return api.traceTx(ctx, msg, txctx, vmctx, statedb, config)
}

// TraceCall lets you trace a given eth_call. It collects the structured logs
// created during the execution of EVM if the given transaction was added on
// top of the provided block and returns them as a JSON object.
func (api *API) TraceCall(ctx context.Context, args api.TransactionArgs, blockNrOrHash rpc.BlockNumberOrHash, config *TraceCallConfig) (interface{}, error) {
	// Try to retrieve the specified block
	var (
		err   error
		block *types.Block
	)
	if hash, ok := blockNrOrHash.Hash(); ok {
		block, err = api.blockByHash(ctx, hash)
	} else if number, ok := blockNrOrHash.Number(); ok {
		if number == rpc.PendingBlockNumber {
			// We don't have access to the miner here. For tracing 'future' transactions,
			// it can be done with block- and state-overrides instead, which offers
			// more flexibility and stability than trying to trace on 'pending', since
			// the contents of 'pending' is unstable and probably not a true representation
			// of what the next actual block is likely to contain.
			return nil, errors.New("tracing on top of pending is not supported")
		}
		block, err = api.blockByNumber(ctx, number)
	} else {
		return nil, errors.New("invalid arguments; neither block nor hash specified")
	}
	if err != nil {
		return nil, err
	}
	// try to recompute the state
	//reexec := defaultTraceReexec
	//if config != nil && config.Reexec != nil {
	//	reexec = *config.Reexec
	//}

	rtx, err := api.backend.ChainDb().BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer rtx.Rollback()

	statedb, err := api.backend.StateAtBlock(ctx, rtx, block /*, reexec, nil, true, false*/)
	if err != nil {
		return nil, err
	}

	traceHeader, ok := block.Header().(*types.Header)
	if !ok {
		return nil, errors.New("unexpected block header type")
	}
	vmctx := core.NewEVMBlockContext(traceHeader, core.GetHashFn(traceHeader, api.chainContext(ctx).GetHeader), api.backend.Engine(), nil)
	// Apply the customization rules if required.
	if config != nil {
		if err := config.StateOverrides.Apply(statedb); err != nil {
			return nil, err
		}
		config.BlockOverrides.Apply(&vmctx)
	}
	// Execute the trace
	msg, err := args.ToMessage(api.backend.RPCGasCap(), block.BaseFee64().ToBig())
	if err != nil {
		return nil, err
	}

	var traceConfig *TraceConfig
	if config != nil {
		traceConfig = &config.TraceConfig
	}
	return api.traceTx(ctx, &msg, new(Context), vmctx, statedb, traceConfig)
}

// traceTx configures a new tracer according to the provided configuration, and
// executes the given message in the provided environment. The return value will
// be tracer dependent.
func (api *API) traceTx(ctx context.Context, message *transaction.Message, txctx *Context, vmctx evmtypes.BlockContext, statedb *state.IntraBlockState, config *TraceConfig) (interface{}, error) {
	var (
		tracer    Tracer
		err       error
		timeout   = defaultTraceTimeout
		txContext = core.NewEVMTxContext(message)
	)
	if config == nil {
		config = &TraceConfig{}
	}
	// Default tracer is the struct logger
	tracer = logger.NewStructLogger(config.Config)
	if config.Tracer != nil {
		tracer, err = DefaultDirectory.New(*config.Tracer, txctx, config.TracerConfig)
		if err != nil {
			return nil, err
		}
	}
	//Debug: true, Tracer: logger.NewMarkdownLogger(nil, os.Stdout)
	vmenv := vm.NewEVM(vmctx, txContext, statedb, api.backend.ChainConfig(), vm.Config{Debug: true, Tracer: tracer, NoBaseFee: true})

	// Define a meaningful timeout of a single transaction trace
	if config.Timeout != nil {
		if timeout, err = time.ParseDuration(*config.Timeout); err != nil {
			return nil, err
		}
	}
	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	go func() {
		<-deadlineCtx.Done()
		if errors.Is(deadlineCtx.Err(), context.DeadlineExceeded) {
			tracer.Stop(errors.New("execution timeout"))
			// Stop evm execution. Note cancellation is not necessarily immediate.
			vmenv.Cancel()
		}
	}()
	defer cancel()

	// Call Prepare to clear out the statedb access list
	statedb.Prepare(txctx.TxHash, txctx.BlockHash, txctx.TxIndex)
	if _, err = core.ApplyMessage(vmenv, message, new(common2.GasPool).AddGas(message.Gas()), true, false); err != nil {
		return nil, fmt.Errorf("tracing failed: %w", err)
	}
	return tracer.GetResult()
}

// APIs return the collection of RPC services the tracer package offers.
func APIs(backend Backend) []rpc.API {
	// Append all the local APIs and return
	return []rpc.API{
		{
			Namespace: "debug",
			Service:   NewAPI(backend),
		},
	}
}

// overrideConfig returns a copy of original with forks enabled by override enabled,
// along with a boolean that indicates whether the copy is canonical (equivalent to the original).
// Note: the Clique-part is _not_ deep copied
func overrideConfig(original *params.ChainConfig, override *params.ChainConfig) (*params.ChainConfig, bool) {
	copy := new(params.ChainConfig)
	*copy = *original
	canon := true

	// Apply forks (after Berlin) to the copy.
	if block := override.BerlinBlock; block != nil {
		copy.BerlinBlock = block
		canon = false
	}
	if block := override.LondonBlock; block != nil {
		copy.LondonBlock = block
		canon = false
	}
	if block := override.ArrowGlacierBlock; block != nil {
		copy.ArrowGlacierBlock = block
		canon = false
	}
	if block := override.GrayGlacierBlock; block != nil {
		copy.GrayGlacierBlock = block
		canon = false
	}
	if block := override.MergeNetsplitBlock; block != nil {
		copy.MergeNetsplitBlock = block
		canon = false
	}
	if timestamp := override.ShanghaiBlock; timestamp != nil {
		copy.ShanghaiBlock = timestamp
		canon = false
	}
	if timestamp := override.CancunBlock; timestamp != nil {
		copy.CancunBlock = timestamp
		canon = false
	}
	if timestamp := override.PragueTime; timestamp != nil {
		copy.PragueTime = timestamp
		canon = false
	}

	return copy, canon
}
