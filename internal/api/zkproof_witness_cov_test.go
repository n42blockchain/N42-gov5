// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func apiTNewChainOnlyAPI(t *testing.T) *API {
	t.Helper()
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	return &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
}

func TestBlockByNumberOrHash(t *testing.T) {
	a := apiTNewChainOnlyAPI(t)

	blk, err := BlockByNumberOrHash(context.Background(), jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(0)), a)
	if err != nil {
		t.Fatalf("BlockByNumberOrHash(number 0) error = %v", err)
	}
	if blk == nil {
		t.Fatal("BlockByNumberOrHash(number 0) = nil")
	}

	blk2, err := BlockByNumberOrHash(context.Background(), jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.PendingBlockNumber), a)
	if err != nil {
		t.Fatalf("BlockByNumberOrHash(pending) error = %v", err)
	}
	if blk2 == nil {
		t.Fatal("BlockByNumberOrHash(pending) = nil")
	}

	genesis := makeTestBlock(types.Hash{}, 0, 1)
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	h := ethCompatibleBlockHash(genesis, cfg)
	blk3, err := BlockByNumberOrHash(context.Background(), jsonrpc.BlockNumberOrHashWithHash(h, false), a)
	if err != nil {
		t.Fatalf("BlockByNumberOrHash(hash) error = %v", err)
	}
	if blk3 == nil {
		t.Fatal("BlockByNumberOrHash(hash) = nil, want genesis block")
	}

	if _, err := BlockByNumberOrHash(context.Background(), jsonrpc.BlockNumberOrHash{}, a); err == nil {
		t.Fatal("BlockByNumberOrHash(empty) error = nil, want error")
	}
}

func TestZKProofAPI_NoProof(t *testing.T) {
	a := apiTNewChainOnlyAPI(t)
	z := NewZKProofAPI(a)

	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(0))

	res, err := z.GetBlockZKProof(context.Background(), bnh)
	if err != nil {
		t.Fatalf("GetBlockZKProof() error = %v", err)
	}
	if res == nil || res.Available {
		t.Fatalf("GetBlockZKProof() = %+v, want unavailable", res)
	}

	if _, err := z.VerifyBlockZKProof(context.Background(), bnh); err == nil {
		t.Fatal("VerifyBlockZKProof(no proof) error = nil, want error")
	}

	status, err := z.GetProofStatus(context.Background(), bnh)
	if err != nil {
		t.Fatalf("GetProofStatus() error = %v", err)
	}
	if status != "pending" {
		t.Fatalf("GetProofStatus() = %q, want pending", status)
	}

	apis := z.APIs()
	if len(apis) != 1 || apis[0].Namespace != "zk" {
		t.Fatalf("APIs() = %+v, want single zk namespace", apis)
	}
}

func TestZKProofAPI_BlockNotFound(t *testing.T) {
	a := apiTNewChainOnlyAPI(t)
	z := NewZKProofAPI(a)
	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(999))

	if _, err := z.GetBlockZKProof(context.Background(), bnh); err == nil {
		t.Fatal("GetBlockZKProof(not found) error = nil, want error")
	}
	if _, err := z.VerifyBlockZKProof(context.Background(), bnh); err == nil {
		t.Fatal("VerifyBlockZKProof(not found) error = nil, want error")
	}
	if _, err := z.GetProofStatus(context.Background(), bnh); err == nil {
		t.Fatal("GetProofStatus(not found) error = nil, want error")
	}
}

func TestWitnessAPI_NewAndNotJMT(t *testing.T) {
	a := apiTNewChainOnlyAPI(t)
	w := NewWitnessAPI(a)
	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(0))

	// bc is the preciseChainStub, not *internal.BlockChain, so the type
	// assertion fails and GetBlockWitness reports "witness not supported".
	if _, err := w.GetBlockWitness(context.Background(), bnh); err == nil {
		t.Fatal("GetBlockWitness(stub chain) error = nil, want error")
	}
}
