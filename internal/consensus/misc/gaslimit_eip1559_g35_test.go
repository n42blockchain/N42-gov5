// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for VerifyGaslimit's bound/floor checks, and the eip1559.go
// success paths (VerifyEip1559Header, CalcBaseFee increase/decrease/steady).

package misc

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/params"
)

func TestG35VerifyGaslimitWithinBounds(t *testing.T) {
	if err := VerifyGaslimit(1_000_000, 1_000_000); err != nil {
		t.Fatalf("expected unchanged gas limit to be valid, got %v", err)
	}
}

func TestG35VerifyGaslimitExceedsBound(t *testing.T) {
	parent := uint64(1_000_000)
	limit := parent / params.GasLimitBoundDivisor
	if err := VerifyGaslimit(parent, parent+limit+1); err == nil {
		t.Fatalf("expected error for gas limit increase beyond bound")
	}
}

func TestG35VerifyGaslimitBelowMin(t *testing.T) {
	if err := VerifyGaslimit(params.MinGasLimit, params.MinGasLimit-1); err == nil {
		t.Fatalf("expected error for gas limit below minimum")
	}
}

func TestG35VerifyGaslimitDecreaseWithinBound(t *testing.T) {
	parent := uint64(10_000_000)
	limit := parent / params.GasLimitBoundDivisor
	if err := VerifyGaslimit(parent, parent-limit+1); err != nil {
		t.Fatalf("expected small decrease to be valid, got %v", err)
	}
}

func londonConfig() *params.ChainConfig {
	return &params.ChainConfig{LondonBlock: big.NewInt(0)}
}

func TestG35CalcBaseFeeSteady(t *testing.T) {
	parent := &block.Header{
		Number:   uint256.NewInt(10),
		GasLimit: 20_000_000,
		GasUsed:  10_000_000, // == target (limit/elasticity)
		BaseFee:  uint256.NewInt(1_000_000_000),
	}
	got := CalcBaseFee(londonConfig(), parent)
	if got.Cmp(parent.BaseFee.ToBig()) != 0 {
		t.Fatalf("expected unchanged base fee at target usage, got %v want %v", got, parent.BaseFee)
	}
}

func TestG35CalcBaseFeeIncrease(t *testing.T) {
	parent := &block.Header{
		Number:   uint256.NewInt(10),
		GasLimit: 20_000_000,
		GasUsed:  20_000_000, // over target
		BaseFee:  uint256.NewInt(1_000_000_000),
	}
	got := CalcBaseFee(londonConfig(), parent)
	if got.Cmp(parent.BaseFee.ToBig()) <= 0 {
		t.Fatalf("expected base fee to increase above parent, got %v", got)
	}
}

func TestG35CalcBaseFeeDecrease(t *testing.T) {
	parent := &block.Header{
		Number:   uint256.NewInt(10),
		GasLimit: 20_000_000,
		GasUsed:  0, // under target
		BaseFee:  uint256.NewInt(1_000_000_000),
	}
	got := CalcBaseFee(londonConfig(), parent)
	if got.Cmp(parent.BaseFee.ToBig()) >= 0 {
		t.Fatalf("expected base fee to decrease below parent, got %v", got)
	}
}

func TestG35CalcBaseFeeNotLondonYet(t *testing.T) {
	config := &params.ChainConfig{LondonBlock: big.NewInt(100)}
	parent := &block.Header{Number: uint256.NewInt(10), BaseFee: uint256.NewInt(5)}
	got := CalcBaseFee(config, parent)
	if got.Uint64() != params.InitialBaseFee {
		t.Fatalf("expected InitialBaseFee pre-London, got %v", got)
	}
}

func TestG35CalcBaseFeeNilParent(t *testing.T) {
	got := CalcBaseFee(londonConfig(), nil)
	if got.Uint64() != params.InitialBaseFee {
		t.Fatalf("expected InitialBaseFee for nil parent, got %v", got)
	}
}

func TestG35CalcBaseFeeNilParentBaseFee(t *testing.T) {
	parent := &block.Header{Number: uint256.NewInt(10), GasLimit: 1000}
	got := CalcBaseFee(londonConfig(), parent)
	if got.Uint64() != params.InitialBaseFee {
		t.Fatalf("expected InitialBaseFee when parent.BaseFee is nil, got %v", got)
	}
}

func TestG35VerifyEip1559HeaderHappyPath(t *testing.T) {
	config := londonConfig()
	parent := &block.Header{
		Number:   uint256.NewInt(10),
		GasLimit: 20_000_000,
		GasUsed:  10_000_000,
		BaseFee:  uint256.NewInt(1_000_000_000),
	}
	expected := CalcBaseFee(config, parent)
	header := &block.Header{
		Number:   uint256.NewInt(11),
		GasLimit: parent.GasLimit,
		BaseFee:  uint256.MustFromBig(expected),
	}
	if err := VerifyEip1559Header(config, parent, header); err != nil {
		t.Fatalf("expected valid EIP-1559 header, got %v", err)
	}
}

func TestG35VerifyEip1559HeaderNilParent(t *testing.T) {
	if err := VerifyEip1559Header(londonConfig(), nil, &block.Header{}); err == nil {
		t.Fatalf("expected error for nil parent")
	}
}

func TestG35VerifyEip1559HeaderNilHeader(t *testing.T) {
	parent := &block.Header{Number: uint256.NewInt(1), GasLimit: 1000}
	if err := VerifyEip1559Header(londonConfig(), parent, nil); err == nil {
		t.Fatalf("expected error for nil header")
	}
}

func TestG35VerifyEip1559HeaderMissingBaseFee(t *testing.T) {
	config := londonConfig()
	parent := &block.Header{Number: uint256.NewInt(1), GasLimit: 20_000_000, BaseFee: uint256.NewInt(1)}
	header := &block.Header{Number: uint256.NewInt(2), GasLimit: 20_000_000}
	if err := VerifyEip1559Header(config, parent, header); err == nil {
		t.Fatalf("expected error for missing baseFee")
	}
}

func TestG35VerifyEip1559HeaderWrongBaseFee(t *testing.T) {
	config := londonConfig()
	parent := &block.Header{Number: uint256.NewInt(1), GasLimit: 20_000_000, GasUsed: 10_000_000, BaseFee: uint256.NewInt(1_000_000_000)}
	header := &block.Header{Number: uint256.NewInt(2), GasLimit: 20_000_000, BaseFee: uint256.NewInt(1)}
	if err := VerifyEip1559Header(config, parent, header); err == nil {
		t.Fatalf("expected error for mismatched baseFee")
	}
}

func TestG35VerifyEip1559HeaderPreLondonGasLimitScaled(t *testing.T) {
	config := &params.ChainConfig{LondonBlock: big.NewInt(100)}
	parent := &block.Header{Number: uint256.NewInt(10), GasLimit: 10_000_000, BaseFee: uint256.NewInt(1)}
	// pre-London: parentGasLimit is implicitly doubled by ElasticityMultiplier,
	// so the header may jump up to ~2x without violating VerifyGaslimit.
	header := &block.Header{
		Number:   uint256.NewInt(11),
		GasLimit: 20_000_000, // parent.GasLimit * ElasticityMultiplier
		BaseFee:  uint256.MustFromBig(CalcBaseFee(config, parent)),
	}
	if err := VerifyEip1559Header(config, parent, header); err != nil {
		t.Fatalf("expected valid pre-London header, got %v", err)
	}
}
