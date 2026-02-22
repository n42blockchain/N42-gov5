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
)

var errTxNotFound = errors.New("transaction not found")

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

// txTraceResult is the result of a single transaction trace.
type txTraceResult struct {
	Result interface{} `json:"result,omitempty"` // Trace results produced by the tracer
	Error  string      `json:"error,omitempty"`  // Trace failure produced by the tracer
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
		return nil, fmt.Errorf("could not decode block: %v", err)
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
	parent, err := api.blockByNumberAndHash(ctx, rpc.BlockNumber(block.Number64().Uint64()-1), block.ParentHash())
	if err != nil {
		return nil, err
	}
	rtx, err := api.backend.ChainDb().BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer rtx.Rollback()

	statedb, err := api.backend.StateAtBlock(ctx, rtx, parent)
	if err != nil {
		return nil, err
	}
	var (
		txs      = block.Transactions()
		blockHash = block.Hash()
		blockCtx  = core.NewEVMBlockContext(block.Header().(*types.Header), core.GetHashFn(block.Header().(*types.Header), api.chainContext(ctx).GetHeader), api.chainContext(ctx).Engine(), nil)
		signer    = transaction.MakeSigner(api.backend.ChainConfig(), block.Number64().ToBig())
		results   = make([]*txTraceResult, len(txs))
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
		statedb.FinalizeTx(api.backend.ChainConfig().Rules(block.Number64().Uint64()), state.NewNoopWriter())
	}
	return results, nil
}

// chainContext constructs the context reader which is used by the evm for reading
// the necessary chain context.
func (api *API) chainContext(ctx context.Context) core.ChainContext {
	return &chainContext{api: api, ctx: ctx}
}

// TraceTransaction returns the structured logs created during the execution of EVM
// and returns them as a JSON object.
func (api *API) TraceTransaction(ctx context.Context, hash common.Hash, config *TraceConfig) (interface{}, error) {
	tx, blockHash, blockNumber, index, err := api.backend.GetTransaction(ctx, hash)
	if err != nil {
		return nil, err
	}
	if tx == nil {
		return nil, errTxNotFound
	}
	if blockNumber == 0 {
		return nil, errors.New("genesis is not traceable")
	}
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
	rtx, err := api.backend.ChainDb().BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer rtx.Rollback()

	statedb, err := api.backend.StateAtBlock(ctx, rtx, block)
	if err != nil {
		return nil, err
	}

	vmctx := core.NewEVMBlockContext(block.Header().(*types.Header), core.GetHashFn(block.Header().(*types.Header), api.chainContext(ctx).GetHeader), api.backend.Engine(), nil)
	if config != nil {
		if err := config.StateOverrides.Apply(statedb); err != nil {
			return nil, err
		}
		config.BlockOverrides.Apply(&vmctx)
	}
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
	if config == nil {
		config = &TraceConfig{}
	}
	var (
		err       error
		timeout   = defaultTraceTimeout
		txContext = core.NewEVMTxContext(message)
		tracer    Tracer = logger.NewStructLogger(config.Config)
	)
	if config.Tracer != nil {
		tracer, err = DefaultDirectory.New(*config.Tracer, txctx, config.TracerConfig)
		if err != nil {
			return nil, err
		}
	}
	vmenv := vm.NewEVM(vmctx, txContext, statedb, api.backend.ChainConfig(), vm.Config{Debug: true, Tracer: tracer, NoBaseFee: true})
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
			vmenv.Cancel()
		}
	}()
	defer cancel()
	statedb.Prepare(txctx.TxHash, txctx.BlockHash, txctx.TxIndex)
	if _, err = core.ApplyMessage(vmenv, message, new(common2.GasPool).AddGas(message.Gas()), true, false); err != nil {
		return nil, fmt.Errorf("tracing failed: %w", err)
	}
	return tracer.GetResult()
}

// APIs returns the collection of RPC services the tracer package offers.
func APIs(backend Backend) []rpc.API {
	return []rpc.API{
		{
			Namespace: "debug",
			Service:   NewAPI(backend),
		},
	}
}

