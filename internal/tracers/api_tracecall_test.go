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

package tracers_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/holiman/uint256"

	"errors"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	types "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	common "github.com/n42blockchain/N42/common/types"

	core "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/tracers"
	_ "github.com/n42blockchain/N42/internal/tracers/native"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	rpc "github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	modstate "github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// liveFakeBackend is a Backend implementation whose state-access methods are
// backed by a real in-memory MDBX-free (memdb) database and a real
// IntraBlockState, so TraceCall / traceTx can be exercised end to end without
// needing a running node.
type liveFakeBackend struct {
	db      kv.RwDB
	block   *types.Block
	from    common.Address
	txFound bool
}

func newLiveFakeBackend(t *testing.T) *liveFakeBackend {
	t.Helper()
	db := memdb.NewTestDB(t)
	txDb := memdb.BeginRw(t, db)
	ibs := modstate.New(modstate.NewPlainState(txDb, 1))

	from := common.HexToAddress("0xa11ce")
	ibs.CreateAccount(from, true)
	ibs.AddBalance(from, uint256.NewInt(1_000_000_000_000))
	if err := ibs.CommitBlock(params.TestChainConfig.Rules(1), modstate.NewPlainStateWriter(txDb, txDb, 1)); err != nil {
		t.Fatalf("CommitBlock: %v", err)
	}
	if err := txDb.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	header := &types.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
		GasLimit:   30_000_000,
		Time:       1,
	}
	block := types.NewBlock(header, nil).(*types.Block)

	return &liveFakeBackend{db: db, block: block, from: from}
}

func (l *liveFakeBackend) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	return l.block.Header().(*types.Header), nil
}
func (l *liveFakeBackend) HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Header, error) {
	return l.block.Header().(*types.Header), nil
}
func (l *liveFakeBackend) BlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	return l.block, nil
}
func (l *liveFakeBackend) BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, error) {
	return l.block, nil
}
func (l *liveFakeBackend) GetTransaction(ctx context.Context, txHash common.Hash) (*transaction.Transaction, common.Hash, uint64, uint64, error) {
	if l.txFound {
		var tx transaction.Transaction
		return &tx, l.block.Hash(), l.block.Number64().Uint64(), 0, nil
	}
	return nil, common.Hash{}, 0, 0, errors.New("tx not found")
}
func (l *liveFakeBackend) RPCGasCap() uint64                { return 50_000_000 }
func (l *liveFakeBackend) ChainConfig() *params.ChainConfig { return params.TestChainConfig }
func (l *liveFakeBackend) Engine() consensus.Engine         { return nil }
func (l *liveFakeBackend) ChainDb() kv.RwDB                 { return l.db }
func (l *liveFakeBackend) StateAtBlock(ctx context.Context, tx kv.Tx, block *types.Block) (*modstate.IntraBlockState, error) {
	return modstate.New(modstate.NewPlainState(tx, block.Number64().Uint64())), nil
}
func (l *liveFakeBackend) StateAtTransaction(ctx context.Context, tx kv.Tx, block *types.Block, txIndex int) (*transaction.Message, evmtypes.BlockContext, *modstate.IntraBlockState, error) {
	if !l.txFound {
		return nil, evmtypes.BlockContext{}, nil, errors.New("tx not found")
	}
	to := common.HexToAddress("0xb0b")
	msg := transaction.NewMessage(l.from, &to, 0, uint256.NewInt(1), 100000, uint256.NewInt(1), uint256.NewInt(1), uint256.NewInt(1), uint256.NewInt(1), nil, nil, nil, false, true)
	header := l.block.Header().(*types.Header)
	vmctx := core.NewEVMBlockContext(header, func(uint64) common.Hash { return common.Hash{} }, nil, params.TestChainConfig, nil)
	statedb := modstate.New(modstate.NewPlainState(tx, header.Number.Uint64()))
	statedb.CreateAccount(l.from, true)
	statedb.AddBalance(l.from, uint256.NewInt(1_000_000_000_000))
	return &msg, vmctx, statedb, nil
}

func TestAPITraceCallSimpleTransferSucceeds(t *testing.T) {
	backend := newLiveFakeBackend(t)
	tapi := tracers.NewAPI(backend)

	to := common.HexToAddress("0xb0b")
	gas := hexutil.Uint64(100000)
	value := (*hexutil.Big)(uint256.NewInt(100).ToBig())
	args := api.TransactionArgs{
		From:  avmAddr(backend.from),
		To:    avmAddr(to),
		Gas:   &gas,
		Value: value,
	}

	bnh := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(1))
	res, err := tapi.TraceCall(context.Background(), args, bnh, nil)
	if err != nil {
		t.Fatalf("TraceCall: %v", err)
	}
	raw, ok := res.(json.RawMessage)
	if !ok {
		b, merr := json.Marshal(res)
		if merr != nil {
			t.Fatalf("marshal result: %v", merr)
		}
		raw = b
	}
	if len(raw) == 0 {
		t.Fatalf("expected non-empty trace result")
	}
}

func TestAPITraceCallWithCustomTracerAndTimeout(t *testing.T) {
	backend := newLiveFakeBackend(t)
	tapi := tracers.NewAPI(backend)

	to := common.HexToAddress("0xb0b")
	gas := hexutil.Uint64(100000)
	value := (*hexutil.Big)(uint256.NewInt(1).ToBig())
	args := api.TransactionArgs{
		From:  avmAddr(backend.from),
		To:    avmAddr(to),
		Gas:   &gas,
		Value: value,
	}
	tracerName := "noopTracer"
	timeout := "2s"
	cfg := &tracers.TraceCallConfig{
		TraceConfig: tracers.TraceConfig{
			Tracer:  &tracerName,
			Timeout: &timeout,
		},
	}

	bnh := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(1))
	res, err := tapi.TraceCall(context.Background(), args, bnh, cfg)
	if err != nil {
		t.Fatalf("TraceCall: %v", err)
	}
	if res == nil {
		t.Fatalf("expected non-nil result")
	}
}

func TestAPITraceCallInvalidTimeoutErrors(t *testing.T) {
	backend := newLiveFakeBackend(t)
	tapi := tracers.NewAPI(backend)

	to := common.HexToAddress("0xb0b")
	gas := hexutil.Uint64(100000)
	args := api.TransactionArgs{
		From: avmAddr(backend.from),
		To:   avmAddr(to),
		Gas:  &gas,
	}
	badTimeout := "not-a-duration"
	cfg := &tracers.TraceCallConfig{
		TraceConfig: tracers.TraceConfig{Timeout: &badTimeout},
	}
	bnh := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(1))
	if _, err := tapi.TraceCall(context.Background(), args, bnh, cfg); err == nil {
		t.Fatalf("expected error for invalid timeout duration")
	}
}

func TestAPITraceCallUnknownTracerErrors(t *testing.T) {
	backend := newLiveFakeBackend(t)
	tapi := tracers.NewAPI(backend)

	to := common.HexToAddress("0xb0b")
	gas := hexutil.Uint64(100000)
	args := api.TransactionArgs{
		From: avmAddr(backend.from),
		To:   avmAddr(to),
		Gas:  &gas,
	}
	badTracer := "doesNotExistTracer"
	cfg := &tracers.TraceCallConfig{
		TraceConfig: tracers.TraceConfig{Tracer: &badTracer},
	}
	bnh := rpc.BlockNumberOrHashWithNumber(rpc.BlockNumber(1))
	if _, err := tapi.TraceCall(context.Background(), args, bnh, cfg); err == nil {
		t.Fatalf("expected error for unknown tracer name")
	}
}

func TestAPITraceTransactionSucceeds(t *testing.T) {
	backend := newLiveFakeBackend(t)
	backend.txFound = true
	tapi := tracers.NewAPI(backend)

	res, err := tapi.TraceTransaction(context.Background(), common.HexToHash("0x1234"), nil)
	if err != nil {
		t.Fatalf("TraceTransaction: %v", err)
	}
	if res == nil {
		t.Fatalf("expected non-nil trace result")
	}
}

func TestAPITraceBlockByNumberEmptyBlockSucceeds(t *testing.T) {
	backend := newLiveFakeBackend(t)
	tapi := tracers.NewAPI(backend)

	results, err := tapi.TraceBlockByNumber(context.Background(), rpc.BlockNumber(1), nil)
	if err != nil {
		t.Fatalf("TraceBlockByNumber: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results for empty block, got %d", len(results))
	}
}

func avmAddr(a common.Address) *avmcommon.Address {
	addr := avmcommon.Address(a)
	return &addr
}
