// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"os"
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

func TestEnvInt(t *testing.T) {
	const key = "N42_TEST_ENVINT_COV"
	os.Unsetenv(key)
	if got := envInt(key, 7); got != 7 {
		t.Fatalf("envInt(unset) = %d, want default 7", got)
	}

	os.Setenv(key, "42")
	defer os.Unsetenv(key)
	if got := envInt(key, 7); got != 42 {
		t.Fatalf("envInt(42) = %d, want 42", got)
	}

	os.Setenv(key, "not-a-number")
	if got := envInt(key, 7); got != 7 {
		t.Fatalf("envInt(invalid) = %d, want default 7", got)
	}

	os.Setenv(key, "-5")
	if got := envInt(key, 7); got != 7 {
		t.Fatalf("envInt(negative) = %d, want default 7 (non-positive rejected)", got)
	}
}

func newConsensusTestAPI(t *testing.T) *ConsensusAPI {
	t.Helper()
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg}
	return NewConsensusAPI(api)
}

func TestConsensusAPI_ResolveBlockNumber(t *testing.T) {
	c := newConsensusTestAPI(t)

	latest, err := c.resolveBlockNumber(jsonrpc.LatestBlockNumber)
	if err != nil {
		t.Fatalf("resolveBlockNumber(latest) error = %v", err)
	}
	if latest != 0 {
		t.Fatalf("resolveBlockNumber(latest) = %d, want 0 (genesis)", latest)
	}

	earliest, err := c.resolveBlockNumber(jsonrpc.EarliestBlockNumber)
	if err != nil {
		t.Fatalf("resolveBlockNumber(earliest) error = %v", err)
	}
	if earliest != 0 {
		t.Fatalf("resolveBlockNumber(earliest) = %d, want 0", earliest)
	}

	specific, err := c.resolveBlockNumber(jsonrpc.BlockNumber(5))
	if err != nil {
		t.Fatalf("resolveBlockNumber(5) error = %v", err)
	}
	if specific != 5 {
		t.Fatalf("resolveBlockNumber(5) = %d, want 5", specific)
	}

	if _, err := c.resolveBlockNumber(jsonrpc.BlockNumber(-5)); err == nil {
		t.Fatal("resolveBlockNumber(-5) error = nil, want error")
	}
}

func TestConsensusAPI_GetStateProofInfoRequiresConcreteChain(t *testing.T) {
	c := newConsensusTestAPI(t)
	if _, err := c.GetStateProofInfo(context.Background()); err == nil {
		t.Fatal("GetStateProofInfo(stub chain) error = nil, want error (concrete blockchain required)")
	}
}

func TestConsensusAPI_LivePoolRequiresConcreteChain(t *testing.T) {
	c := newConsensusTestAPI(t)
	if _, err := c.livePool(); err == nil {
		t.Fatal("livePool(stub chain) error = nil, want error")
	}
}

func TestConsensusAPI_RegisterAndSubmitRequireLivePool(t *testing.T) {
	c := newConsensusTestAPI(t)

	if _, err := c.RegisterCommitteeValidator(context.Background(), hexutil.Uint64(0), nil, nil); err == nil {
		t.Fatal("RegisterCommitteeValidator(no pool) error = nil, want error")
	}
	if _, err := c.SubmitCommitteeSignature(context.Background(), hexutil.Uint64(0), types.Hash{}, hexutil.Uint64(0), nil); err == nil {
		t.Fatal("SubmitCommitteeSignature(no pool) error = nil, want error")
	}
}
