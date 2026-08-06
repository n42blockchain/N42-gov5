package main

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/params"
)

func TestValidateEthereumRLPHeaderRejectsInvalidGasLimit(t *testing.T) {
	parent := &block.Header{Number: uint256.NewInt(0), GasLimit: 10_000, Time: 1}
	header := &block.Header{Number: uint256.NewInt(1), GasLimit: params.MinGasLimit - 1, Time: 2}

	if err := validateEthereumRLPHeader(header, parent, &params.ChainConfig{}); err == nil {
		t.Fatal("expected gas-limit validation error")
	}
}

func TestValidateEthereumRLPHeaderChecksTransitions(t *testing.T) {
	parent := &block.Header{Number: uint256.NewInt(1), GasLimit: 10_000, Time: 10, BaseFee: uint256.NewInt(7)}
	cfg := &params.ChainConfig{LondonBlock: big.NewInt(2)}

	for name, header := range map[string]*block.Header{
		"same timestamp": {Number: uint256.NewInt(2), GasLimit: 20_000, Time: 10, BaseFee: uint256.NewInt(7)},
		"wrong number":   {Number: uint256.NewInt(3), GasLimit: 20_000, Time: 11, BaseFee: uint256.NewInt(7)},
		"bad gas used":   {Number: uint256.NewInt(2), GasLimit: 20_000, GasUsed: 20_001, Time: 11, BaseFee: uint256.NewInt(7)},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateEthereumRLPHeader(header, parent, cfg); err == nil {
				t.Fatal("expected Ethereum header validation error")
			}
		})
	}
}

func TestValidateEthereumRLPHeaderChecksCancunBlobGas(t *testing.T) {
	zero := uint64(0)
	incorrectExcess := uint64(1)
	parent := &block.Header{
		Number:        uint256.NewInt(0),
		GasLimit:      10_000,
		Time:          1,
		BlobGasUsed:   &zero,
		ExcessBlobGas: &zero,
	}
	header := &block.Header{
		Number:        uint256.NewInt(1),
		GasLimit:      10_000,
		Time:          2,
		BlobGasUsed:   &zero,
		ExcessBlobGas: &incorrectExcess,
	}
	cfg := &params.ChainConfig{CancunTime: big.NewInt(0)}

	if err := validateEthereumRLPHeader(header, parent, cfg); err == nil {
		t.Fatal("expected EIP-4844 excess blob gas validation error")
	}
}

func TestValidateEthereumRLPHeaderUsesPragueBlobLimits(t *testing.T) {
	zero := uint64(0)
	cfg := &params.ChainConfig{CancunTime: big.NewInt(0), PragueTime: big.NewInt(0)}
	maxBlobGas := cfg.BlobMaxGasPerBlock(2)
	parent := &block.Header{
		Number:        uint256.NewInt(0),
		GasLimit:      10_000,
		Time:          1,
		BlobGasUsed:   &zero,
		ExcessBlobGas: &zero,
	}
	header := &block.Header{
		Number:        uint256.NewInt(1),
		GasLimit:      10_000,
		Time:          2,
		BlobGasUsed:   &maxBlobGas,
		ExcessBlobGas: &zero,
	}

	if err := validateEthereumRLPHeader(header, parent, cfg); err != nil {
		t.Fatalf("expected Prague blob gas limit to be valid: %v", err)
	}
}
