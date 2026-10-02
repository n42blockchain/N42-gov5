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

	"github.com/n42blockchain/N42/common/hexutil"
	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	rpc "github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestTraceAPIBlockPropagatesLookupError(t *testing.T) {
	tapi := NewTraceAPI(&fakeBackend{})
	_, err := tapi.Block(context.Background(), rpc.LatestBlockNumber)
	if err == nil {
		t.Fatalf("expected error from missing block")
	}
}

func TestTraceAPITransactionPropagatesError(t *testing.T) {
	wantErr := errors.New("tx lookup failed")
	tapi := NewTraceAPI(&fakeBackend{getTxErr: wantErr})
	_, err := tapi.Transaction(context.Background(), common.HexToHash("0x01"))
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error propagated, got %v", err)
	}
}

func TestTraceAPIGetPropagatesError(t *testing.T) {
	wantErr := errors.New("tx lookup failed")
	tapi := NewTraceAPI(&fakeBackend{getTxErr: wantErr})
	_, err := tapi.Get(context.Background(), common.HexToHash("0x01"), []hexutil.Uint{0})
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error propagated, got %v", err)
	}
}

func TestTraceAPIReplayTransactionPropagatesError(t *testing.T) {
	wantErr := errors.New("tx lookup failed")
	tapi := NewTraceAPI(&fakeBackend{getTxErr: wantErr})
	_, err := tapi.ReplayTransaction(context.Background(), common.HexToHash("0x01"), nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error propagated, got %v", err)
	}
}

func TestTraceAPIReplayBlockTransactionsPropagatesBlockLookupError(t *testing.T) {
	tapi := NewTraceAPI(&fakeBackend{})
	_, err := tapi.ReplayBlockTransactions(context.Background(), rpc.LatestBlockNumber, nil)
	if err == nil {
		t.Fatalf("expected error from missing block")
	}
}

func TestTraceAPICallPropagatesInvalidArgsError(t *testing.T) {
	tapi := NewTraceAPI(&fakeBackend{})
	_, err := tapi.Call(context.Background(), api.TransactionArgs{}, nil, nil)
	if err == nil {
		t.Fatalf("expected error from TraceCall path (no block state available)")
	}
}

func TestTraceAPICallUsesProvidedBlockNrOrHash(t *testing.T) {
	tapi := NewTraceAPI(&fakeBackend{})
	bnh := rpc.BlockNumberOrHashWithHash(common.HexToHash("0x01"), false)
	_, err := tapi.Call(context.Background(), api.TransactionArgs{}, nil, &bnh)
	if err == nil {
		t.Fatalf("expected error since fakeBackend has no block for the given hash")
	}
}

func TestTraceAPIFilterPropagatesLatestBlockError(t *testing.T) {
	tapi := NewTraceAPI(&fakeBackend{})
	_, err := tapi.Filter(context.Background(), TraceFilterRequest{})
	if err == nil {
		t.Fatalf("expected error from missing latest block")
	}
}
