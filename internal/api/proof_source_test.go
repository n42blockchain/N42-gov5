// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

type fakeProofSource struct {
	calls []uint64
	err   error
}

func (f *fakeProofSource) ProveAt(_ context.Context, _ kv.Tx, address types.Address, keys []string, n uint64) (*AccountResult, error) {
	f.calls = append(f.calls, n)
	if f.err != nil {
		return nil, f.err
	}
	return &AccountResult{Address: address, AccountProof: []string{"0xsource"}, Nonce: hexutil.Uint64(n)}, nil
}

// TestGetProofSource: eth_getProof asks the proof source first, at the
// resolved height; ErrProofNotCovered falls back to the node's own path and
// any other source error is returned as is.
func TestGetProofSource(t *testing.T) {
	api := setupStorageTestAPI(t)
	addr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	src := &fakeProofSource{}
	api.api.SetProofSource(src)

	// "latest" on a genesis-only chain resolves to 0; an explicit number the
	// node cannot resolve (no such block here) is taken as given.
	for _, tc := range []struct {
		arg  jsonrpc.BlockNumberOrHash
		want uint64
	}{
		{jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber), 0},
		{jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(12345)), 12345},
	} {
		res, err := api.GetProof(context.Background(), addr, nil, tc.arg)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.AccountProof) != 1 || res.AccountProof[0] != "0xsource" || uint64(res.Nonce) != tc.want {
			t.Fatalf("%v: answer did not come from the source at height %d: %+v", tc.arg, tc.want, res)
		}
	}

	src.err = ErrProofNotCovered
	res, err := api.GetProof(context.Background(), addr, nil, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber))
	if err != nil || res == nil || (len(res.AccountProof) == 1 && res.AccountProof[0] == "0xsource") {
		t.Fatalf("not covered: want the node's own answer, got %+v, %v", res, err)
	}

	boom := errors.New("archive disagrees with the header")
	src.err = boom
	if _, err := api.GetProof(context.Background(), addr, nil, jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)); !errors.Is(err, boom) {
		t.Fatalf("a source error must be final, got %v", err)
	}
}
