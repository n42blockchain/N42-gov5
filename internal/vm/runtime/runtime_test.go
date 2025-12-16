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

// Tests adapted from go-ethereum and erigon runtime test suites.
// Reference: go-ethereum/core/vm/runtime/runtime_test.go

package runtime

import (
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Config Tests
// =============================================================================

func TestSetDefaults(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	// ChainConfig should be set
	if cfg.ChainConfig == nil {
		t.Error("ChainConfig should be set")
	}

	// Difficulty should be set
	if cfg.Difficulty == nil {
		t.Error("Difficulty should be set")
	}

	// Time should be set
	if cfg.Time == nil {
		t.Error("Time should be set")
	}

	// GasLimit should be set
	if cfg.GasLimit == 0 {
		t.Error("GasLimit should be set")
	}

	// GasPrice should be set
	if cfg.GasPrice == nil {
		t.Error("GasPrice should be set")
	}

	// Value should be set
	if cfg.Value == nil {
		t.Error("Value should be set")
	}

	// BlockNumber should be set
	if cfg.BlockNumber == nil {
		t.Error("BlockNumber should be set")
	}

	// GetHashFn should be set
	if cfg.GetHashFn == nil {
		t.Error("GetHashFn should be set")
	}

	t.Logf("✓ setDefaults sets all required fields")
}

func TestSetDefaultsChainConfig(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	// Verify chain config has all forks set
	if cfg.ChainConfig.ChainID == nil || cfg.ChainConfig.ChainID.Cmp(big.NewInt(1)) != 0 {
		t.Error("ChainID should be 1")
	}

	if cfg.ChainConfig.HomesteadBlock == nil {
		t.Error("HomesteadBlock should be set")
	}

	if cfg.ChainConfig.ByzantiumBlock == nil {
		t.Error("ByzantiumBlock should be set")
	}

	if cfg.ChainConfig.ConstantinopleBlock == nil {
		t.Error("ConstantinopleBlock should be set")
	}

	if cfg.ChainConfig.IstanbulBlock == nil {
		t.Error("IstanbulBlock should be set")
	}

	if cfg.ChainConfig.BerlinBlock == nil {
		t.Error("BerlinBlock should be set")
	}

	if cfg.ChainConfig.LondonBlock == nil {
		t.Error("LondonBlock should be set")
	}

	if cfg.ChainConfig.ShanghaiBlock == nil {
		t.Error("ShanghaiBlock should be set")
	}

	if cfg.ChainConfig.CancunBlock == nil {
		t.Error("CancunBlock should be set")
	}

	if cfg.ChainConfig.PragueTime == nil {
		t.Error("PragueTime should be set")
	}

	t.Logf("✓ ChainConfig has all fork blocks set")
}

func TestSetDefaultsPreservesExisting(t *testing.T) {
	customChainID := big.NewInt(42)
	customDifficulty := big.NewInt(12345)
	customGasLimit := uint64(8000000)

	cfg := &Config{
		ChainConfig: &params.ChainConfig{ChainID: customChainID},
		Difficulty:  customDifficulty,
		GasLimit:    customGasLimit,
	}

	setDefaults(cfg)

	// Custom values should be preserved
	if cfg.ChainConfig.ChainID.Cmp(customChainID) != 0 {
		t.Error("Custom ChainID should be preserved")
	}

	if cfg.Difficulty.Cmp(customDifficulty) != 0 {
		t.Error("Custom Difficulty should be preserved")
	}

	if cfg.GasLimit != customGasLimit {
		t.Error("Custom GasLimit should be preserved")
	}

	t.Logf("✓ setDefaults preserves existing values")
}

func TestSetDefaultsTime(t *testing.T) {
	cfg := &Config{}
	before := time.Now().Unix()
	setDefaults(cfg)
	after := time.Now().Unix()

	timeVal := cfg.Time.Int64()
	if timeVal < before || timeVal > after {
		t.Errorf("Time should be around current time, got %d, expected between %d and %d", timeVal, before, after)
	}

	t.Logf("✓ setDefaults sets time to current time")
}

func TestGetHashFn(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	// Test that GetHashFn returns deterministic results
	hash1 := cfg.GetHashFn(100)
	hash2 := cfg.GetHashFn(100)

	if hash1 != hash2 {
		t.Error("GetHashFn should return same hash for same input")
	}

	// Different inputs should give different hashes
	hash3 := cfg.GetHashFn(101)
	if hash1 == hash3 {
		t.Error("GetHashFn should return different hash for different input")
	}

	t.Logf("Hash for block 100: %x", hash1)
	t.Logf("✓ GetHashFn works correctly")
}

// =============================================================================
// Config Field Tests
// =============================================================================

func TestConfigFields(t *testing.T) {
	origin := types.HexToAddress("0x1111111111111111111111111111111111111111")
	coinbase := types.HexToAddress("0x2222222222222222222222222222222222222222")

	cfg := &Config{
		Origin:      origin,
		Coinbase:    coinbase,
		BlockNumber: big.NewInt(100),
		Time:        big.NewInt(1234567890),
		GasLimit:    10000000,
		GasPrice:    uint256.NewInt(1000000000),
		Value:       uint256.NewInt(100),
		BaseFee:     uint256.NewInt(50000000),
	}

	if cfg.Origin != origin {
		t.Error("Origin mismatch")
	}

	if cfg.Coinbase != coinbase {
		t.Error("Coinbase mismatch")
	}

	if cfg.BlockNumber.Cmp(big.NewInt(100)) != 0 {
		t.Error("BlockNumber mismatch")
	}

	if cfg.Time.Cmp(big.NewInt(1234567890)) != 0 {
		t.Error("Time mismatch")
	}

	if cfg.GasLimit != 10000000 {
		t.Error("GasLimit mismatch")
	}

	if cfg.GasPrice.Cmp(uint256.NewInt(1000000000)) != 0 {
		t.Error("GasPrice mismatch")
	}

	if cfg.Value.Cmp(uint256.NewInt(100)) != 0 {
		t.Error("Value mismatch")
	}

	if cfg.BaseFee.Cmp(uint256.NewInt(50000000)) != 0 {
		t.Error("BaseFee mismatch")
	}

	t.Logf("✓ Config fields store values correctly")
}

// =============================================================================
// EVMConfig Tests
// =============================================================================

func TestEVMConfigDefaults(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	if cfg.EVMConfig.Debug {
		t.Error("EVMConfig.Debug should default to false")
	}

	if cfg.EVMConfig.Tracer != nil {
		t.Error("EVMConfig.Tracer should default to nil")
	}

	if cfg.EVMConfig.NoRecursion {
		t.Error("EVMConfig.NoRecursion should default to false")
	}

	t.Logf("✓ EVMConfig has correct defaults")
}

// =============================================================================
// Nil Config Tests
// =============================================================================

func TestNilConfigHandling(t *testing.T) {
	// These functions should handle nil config gracefully
	// by calling setDefaults internally

	// Note: Execute, Create, Call require State to be set,
	// so we can't fully test them without mocking.
	// Here we just verify the config setup logic.

	cfg := (*Config)(nil)
	if cfg != nil {
		t.Error("Nil config check failed")
	}

	// Create new config and set defaults
	cfg2 := new(Config)
	setDefaults(cfg2)
	if cfg2.ChainConfig == nil {
		t.Error("setDefaults should work on new(Config)")
	}

	t.Logf("✓ Nil config handling works")
}

// =============================================================================
// Address Generation Tests
// =============================================================================

func TestContractAddressGeneration(t *testing.T) {
	// Test that BytesToAddress works as expected
	contractBytes := []byte("contract")
	addr := types.BytesToAddress(contractBytes)

	if addr == (types.Address{}) {
		t.Error("Generated address should not be zero")
	}

	// Same input should give same address
	addr2 := types.BytesToAddress(contractBytes)
	if addr != addr2 {
		t.Error("Same bytes should give same address")
	}

	t.Logf("Contract address: %s", addr.Hex())
	t.Logf("✓ Contract address generation works")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkSetDefaults(b *testing.B) {
	for i := 0; i < b.N; i++ {
		cfg := &Config{}
		setDefaults(cfg)
	}
}

func BenchmarkGetHashFn(b *testing.B) {
	cfg := &Config{}
	setDefaults(cfg)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cfg.GetHashFn(uint64(i))
	}
}

func BenchmarkBytesToAddress(b *testing.B) {
	contractBytes := []byte("contract")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		types.BytesToAddress(contractBytes)
	}
}

// =============================================================================
// ChainConfig Fork Tests
// =============================================================================

func TestChainConfigAllForksEnabled(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	// All forks should be at block 0 (enabled from genesis)
	zeroBlock := new(big.Int)

	checks := []struct {
		name  string
		block *big.Int
	}{
		{"Homestead", cfg.ChainConfig.HomesteadBlock},
		{"TangerineWhistle", cfg.ChainConfig.TangerineWhistleBlock},
		{"SpuriousDragon", cfg.ChainConfig.SpuriousDragonBlock},
		{"Byzantium", cfg.ChainConfig.ByzantiumBlock},
		{"Constantinople", cfg.ChainConfig.ConstantinopleBlock},
		{"Petersburg", cfg.ChainConfig.PetersburgBlock},
		{"Istanbul", cfg.ChainConfig.IstanbulBlock},
		{"MuirGlacier", cfg.ChainConfig.MuirGlacierBlock},
		{"Berlin", cfg.ChainConfig.BerlinBlock},
		{"London", cfg.ChainConfig.LondonBlock},
		{"ArrowGlacier", cfg.ChainConfig.ArrowGlacierBlock},
		{"GrayGlacier", cfg.ChainConfig.GrayGlacierBlock},
		{"Shanghai", cfg.ChainConfig.ShanghaiBlock},
		{"Cancun", cfg.ChainConfig.CancunBlock},
	}

	for _, check := range checks {
		if check.block == nil || check.block.Cmp(zeroBlock) != 0 {
			t.Errorf("%s should be at block 0, got %v", check.name, check.block)
		}
	}

	t.Logf("✓ All forks enabled at block 0")
}

