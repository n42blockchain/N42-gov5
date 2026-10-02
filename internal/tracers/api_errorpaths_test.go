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
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	types "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv"
	rpc "github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// fakeBackend is a minimal, fully-controllable Backend implementation used to
// exercise the error / early-return branches of API and TraceAPI without
// standing up a real chain, database, or EVM.
type fakeBackend struct {
	headerByHashErr   error
	headerByNumberErr error
	blockByHash       *types.Block
	blockByHashErr    error
	blockByNumber     *types.Block
	blockByNumberErr  error
	getTxErr          error
	rpcGasCap         uint64
	chainConfig       *params.ChainConfig
	chainDbErr        error
}

func (f *fakeBackend) HeaderByHash(ctx context.Context, hash common.Hash) (*types.Header, error) {
	return nil, f.headerByHashErr
}
func (f *fakeBackend) HeaderByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Header, error) {
	return nil, f.headerByNumberErr
}
func (f *fakeBackend) BlockByHash(ctx context.Context, hash common.Hash) (*types.Block, error) {
	return f.blockByHash, f.blockByHashErr
}
func (f *fakeBackend) BlockByNumber(ctx context.Context, number rpc.BlockNumber) (*types.Block, error) {
	return f.blockByNumber, f.blockByNumberErr
}
func (f *fakeBackend) GetTransaction(ctx context.Context, txHash common.Hash) (*transaction.Transaction, common.Hash, uint64, uint64, error) {
	return nil, common.Hash{}, 0, 0, f.getTxErr
}
func (f *fakeBackend) RPCGasCap() uint64                { return f.rpcGasCap }
func (f *fakeBackend) ChainConfig() *params.ChainConfig { return f.chainConfig }
func (f *fakeBackend) Engine() consensus.Engine         { return nil }
func (f *fakeBackend) ChainDb() kv.RwDB                 { return nil }
func (f *fakeBackend) StateAtBlock(ctx context.Context, tx kv.Tx, block *types.Block) (*state.IntraBlockState, error) {
	return nil, errors.New("not implemented in fakeBackend")
}
func (f *fakeBackend) StateAtTransaction(ctx context.Context, tx kv.Tx, block *types.Block, txIndex int) (*transaction.Message, evmtypes.BlockContext, *state.IntraBlockState, error) {
	return nil, evmtypes.BlockContext{}, nil, errors.New("not implemented in fakeBackend")
}

func TestAPIBlockByNumberNotFound(t *testing.T) {
	api := NewAPI(&fakeBackend{})
	_, err := api.blockByNumber(context.Background(), rpc.LatestBlockNumber)
	if err == nil {
		t.Fatalf("expected not-found error")
	}
}

func TestAPIBlockByNumberBackendError(t *testing.T) {
	wantErr := errors.New("backend down")
	api := NewAPI(&fakeBackend{blockByNumberErr: wantErr})
	_, err := api.blockByNumber(context.Background(), rpc.LatestBlockNumber)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected backend error propagated, got %v", err)
	}
}

func TestAPIBlockByHashNotFound(t *testing.T) {
	api := NewAPI(&fakeBackend{})
	_, err := api.blockByHash(context.Background(), common.HexToHash("0x01"))
	if err == nil {
		t.Fatalf("expected not-found error")
	}
}

func TestAPITraceBlockByNumberPropagatesLookupError(t *testing.T) {
	api := NewAPI(&fakeBackend{})
	_, err := api.TraceBlockByNumber(context.Background(), rpc.LatestBlockNumber, nil)
	if err == nil {
		t.Fatalf("expected error from missing block")
	}
}

func TestAPITraceBlockByHashPropagatesLookupError(t *testing.T) {
	api := NewAPI(&fakeBackend{})
	_, err := api.TraceBlockByHash(context.Background(), common.HexToHash("0x01"), nil)
	if err == nil {
		t.Fatalf("expected error from missing block")
	}
}

func TestAPITraceBlockDecodeError(t *testing.T) {
	api := NewAPI(&fakeBackend{})
	_, err := api.TraceBlock(context.Background(), []byte{0xff, 0xff, 0xff}, nil)
	if err == nil {
		t.Fatalf("expected rlp decode error")
	}
}

func TestAPITraceBlockGenesisNotTraceable(t *testing.T) {
	block := types.NewBlock(&types.Header{Number: uint256.NewInt(0)}, nil).(*types.Block)
	api := NewAPI(&fakeBackend{})
	_, err := api.traceBlock(context.Background(), block, nil)
	if err == nil || err.Error() != "genesis is not traceable" {
		t.Fatalf("expected genesis-not-traceable error, got %v", err)
	}
}

func TestAPITraceTransactionNotFound(t *testing.T) {
	api := NewAPI(&fakeBackend{getTxErr: nil})
	_, err := api.TraceTransaction(context.Background(), common.HexToHash("0x01"), nil)
	if !errors.Is(err, errTxNotFound) {
		t.Fatalf("expected errTxNotFound, got %v", err)
	}
}

func TestAPITraceTransactionBackendError(t *testing.T) {
	wantErr := errors.New("tx lookup failed")
	api := NewAPI(&fakeBackend{getTxErr: wantErr})
	_, err := api.TraceTransaction(context.Background(), common.HexToHash("0x01"), nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected backend error propagated, got %v", err)
	}
}

func TestAPITraceCallInvalidArgs(t *testing.T) {
	tapi := NewAPI(&fakeBackend{})
	_, err := tapi.TraceCall(context.Background(), api.TransactionArgs{}, rpc.BlockNumberOrHash{}, nil)
	if err == nil {
		t.Fatalf("expected error for neither block nor hash specified")
	}
}

func TestAPITraceCallPendingUnsupported(t *testing.T) {
	tapi := NewAPI(&fakeBackend{})
	pending := rpc.BlockNumberOrHashWithNumber(rpc.PendingBlockNumber)
	_, err := tapi.TraceCall(context.Background(), api.TransactionArgs{}, pending, nil)
	if err == nil {
		t.Fatalf("expected pending-not-supported error")
	}
}

func TestChainContextGetHeaderFallsBackToHash(t *testing.T) {
	api := NewAPI(&fakeBackend{headerByNumberErr: errors.New("no header by number")})
	cc := &chainContext{api: api, ctx: context.Background()}
	if h := cc.GetHeader(common.HexToHash("0x01"), 1); h != nil {
		t.Fatalf("expected nil header when both lookups fail")
	}
}

func TestAPIsRegistersBothNamespaces(t *testing.T) {
	apis := APIs(&fakeBackend{})
	if len(apis) != 2 {
		t.Fatalf("expected 2 APIs, got %d", len(apis))
	}
	if apis[0].Namespace != "debug" || apis[1].Namespace != "trace" {
		t.Fatalf("unexpected namespaces: %s, %s", apis[0].Namespace, apis[1].Namespace)
	}
}
