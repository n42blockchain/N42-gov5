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

package params

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params/networkname"
)

func TestChainConfigString(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:             big.NewInt(1),
		HomesteadBlock:      big.NewInt(0),
		ByzantiumBlock:      big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
	}

	str := cfg.String()
	if str == "" {
		t.Error("ChainConfig.String() returned empty string")
	}
}

func TestChainConfigDescription(t *testing.T) {
	cfg := &ChainConfig{
		ChainID: big.NewInt(94),
	}

	desc := cfg.Description()
	if desc == "" {
		t.Error("ChainConfig.Description() returned empty string")
	}
}

func TestIsForkedMethods(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(10),
		DAOForkBlock:          big.NewInt(20),
		TangerineWhistleBlock: big.NewInt(30),
		SpuriousDragonBlock:   big.NewInt(40),
		ByzantiumBlock:        big.NewInt(50),
		ConstantinopleBlock:   big.NewInt(60),
		PetersburgBlock:       big.NewInt(70),
		IstanbulBlock:         big.NewInt(80),
		MuirGlacierBlock:      big.NewInt(90),
		BerlinBlock:           big.NewInt(100),
		LondonBlock:           big.NewInt(110),
		ArrowGlacierBlock:     big.NewInt(120),
		GrayGlacierBlock:      big.NewInt(130),
		ShanghaiBlock:         big.NewInt(140),
		CancunBlock:           big.NewInt(150),
		PragueTime:            big.NewInt(1000),
		PectraTime:            big.NewInt(2000),
		OsakaTime:             big.NewInt(3000),
		FusakaTime:            big.NewInt(4000),
	}

	tests := []struct {
		name   string
		check  func(uint64) bool
		block  uint64
		expect bool
	}{
		{"IsHomestead before", cfg.IsHomestead, 5, false},
		{"IsHomestead at", cfg.IsHomestead, 10, true},
		{"IsHomestead after", cfg.IsHomestead, 15, true},
		{"IsDAOFork before", cfg.IsDAOFork, 15, false},
		{"IsDAOFork at", cfg.IsDAOFork, 20, true},
		{"IsTangerineWhistle", cfg.IsTangerineWhistle, 35, true},
		{"IsSpuriousDragon", cfg.IsSpuriousDragon, 45, true},
		{"IsByzantium", cfg.IsByzantium, 55, true},
		{"IsConstantinople", cfg.IsConstantinople, 65, true},
		{"IsPetersburg", cfg.IsPetersburg, 75, true},
		{"IsIstanbul", cfg.IsIstanbul, 85, true},
		{"IsMuirGlacier", cfg.IsMuirGlacier, 95, true},
		{"IsBerlin", cfg.IsBerlin, 105, true},
		{"IsLondon", cfg.IsLondon, 115, true},
		{"IsArrowGlacier", cfg.IsArrowGlacier, 125, true},
		{"IsGrayGlacier", cfg.IsGrayGlacier, 135, true},
		{"IsShanghai", cfg.IsShanghai, 145, true},
		{"IsCancun", cfg.IsCancun, 155, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.check(tt.block)
			if result != tt.expect {
				t.Errorf("%s(%d) = %v, want %v", tt.name, tt.block, result, tt.expect)
			}
		})
	}

	// Test timestamp-based forks
	if !cfg.IsPrague(1500) {
		t.Error("IsPrague should return true for timestamp >= 1000")
	}
	if cfg.IsPrague(500) {
		t.Error("IsPrague should return false for timestamp < 1000")
	}

	if !cfg.IsPectra(2500) {
		t.Error("IsPectra should return true for timestamp >= 2000")
	}

	if !cfg.IsOsaka(3500) {
		t.Error("IsOsaka should return true for timestamp >= 3000")
	}

	if !cfg.IsFusaka(4500) {
		t.Error("IsFusaka should return true for timestamp >= 4000")
	}
}

func TestIsForkedWithNilBlock(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:        big.NewInt(1),
		HomesteadBlock: nil, // nil means not forked
	}

	if cfg.IsHomestead(1000) {
		t.Error("IsHomestead should return false when block is nil")
	}
}

func TestIsPetersburgSpecialCase(t *testing.T) {
	// Petersburg nil but Constantinople set
	cfg := &ChainConfig{
		ConstantinopleBlock: big.NewInt(10),
		PetersburgBlock:     nil,
	}

	if !cfg.IsPetersburg(15) {
		t.Error("IsPetersburg should return true when nil but Constantinople is active")
	}
}

func TestRules(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:             big.NewInt(1),
		HomesteadBlock:      big.NewInt(0),
		ByzantiumBlock:      big.NewInt(0),
		ConstantinopleBlock: big.NewInt(0),
		PetersburgBlock:     big.NewInt(0),
		IstanbulBlock:       big.NewInt(0),
		BerlinBlock:         big.NewInt(0),
		LondonBlock:         big.NewInt(0),
		ShanghaiBlock:       big.NewInt(0),
	}

	rules := cfg.Rules(100)
	if rules == nil {
		t.Fatal("Rules() returned nil")
	}

	if rules.ChainID.Cmp(big.NewInt(1)) != 0 {
		t.Error("Rules.ChainID incorrect")
	}

	if !rules.IsHomestead {
		t.Error("Rules.IsHomestead should be true")
	}
	if !rules.IsByzantium {
		t.Error("Rules.IsByzantium should be true")
	}
}

func TestRulesWithNilChainID(t *testing.T) {
	cfg := &ChainConfig{
		ChainID: nil,
	}

	rules := cfg.Rules(100)
	if rules.ChainID == nil {
		t.Error("Rules.ChainID should not be nil")
	}
	if rules.ChainID.Cmp(big.NewInt(0)) != 0 {
		t.Error("Rules.ChainID should be 0 for nil ChainID")
	}
}

func TestRulesWithTimestamp(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:        big.NewInt(1),
		HomesteadBlock: big.NewInt(0),
		PragueTime:     big.NewInt(1000),
		PectraTime:     big.NewInt(2000),
	}

	// Test with block 100, timestamp 500 (before Prague)
	rules := cfg.RulesWithTimestamp(100, 500)
	if rules.IsPrague {
		t.Error("IsPrague should be false for timestamp 500")
	}

	// Test with block 100, timestamp 1500 (after Prague, before Pectra)
	rules = cfg.RulesWithTimestamp(100, 1500)
	if !rules.IsPrague {
		t.Error("IsPrague should be true for timestamp 1500")
	}
	if rules.IsPectra {
		t.Error("IsPectra should be false for timestamp 1500")
	}
}

func TestIsParisAtIncludesMergeAndLaterForks(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:            big.NewInt(1),
		LondonBlock:        big.NewInt(0),
		MergeNetsplitBlock: big.NewInt(10),
		ShanghaiTime:       big.NewInt(15_000),
	}

	if cfg.IsParisAt(5, 5) {
		t.Fatal("IsParisAt should be false before merge netsplit/shanghai activation")
	}
	if !cfg.IsParisAt(10, 5) {
		t.Fatal("IsParisAt should be true at merge netsplit activation")
	}
	if !cfg.IsParisAt(11, 15_000) {
		t.Fatal("IsParisAt should remain true for Shanghai+ semantics")
	}

	rules := cfg.RulesWithTimestamp(10, 5)
	if !rules.IsParis {
		t.Fatal("RulesWithTimestamp should mark Paris active at merge netsplit")
	}
}

func TestRulesWithTimestampSupportsShanghaiCancunTime(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:      big.NewInt(1),
		ShanghaiTime: big.NewInt(10),
		CancunTime:   big.NewInt(20),
	}

	rules := cfg.RulesWithTimestamp(0, 9)
	if rules.IsShanghai || rules.IsCancun {
		t.Fatal("Shanghai/Cancun should be inactive before timestamp activation")
	}

	rules = cfg.RulesWithTimestamp(0, 10)
	if !rules.IsShanghai {
		t.Fatal("Shanghai should activate at shanghaiTime")
	}
	if rules.IsCancun {
		t.Fatal("Cancun should remain inactive before cancunTime")
	}

	rules = cfg.RulesWithTimestamp(0, 20)
	if !rules.IsShanghai || !rules.IsCancun {
		t.Fatal("Shanghai and Cancun should both be active at cancunTime")
	}
}

func TestRulesWithTimestampForkInheritance(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:   big.NewInt(1),
		OsakaTime: big.NewInt(0),
	}

	rules := cfg.RulesWithTimestamp(0, 0)

	if !rules.IsOsaka {
		t.Fatal("IsOsaka should be true at Osaka activation")
	}
	for name, enabled := range map[string]bool{
		"IsPectra":           rules.IsPectra,
		"IsPrague":           rules.IsPrague,
		"IsCancun":           rules.IsCancun,
		"IsShanghai":         rules.IsShanghai,
		"IsLondon":           rules.IsLondon,
		"IsBerlin":           rules.IsBerlin,
		"IsIstanbul":         rules.IsIstanbul,
		"IsPetersburg":       rules.IsPetersburg,
		"IsConstantinople":   rules.IsConstantinople,
		"IsByzantium":        rules.IsByzantium,
		"IsSpuriousDragon":   rules.IsSpuriousDragon,
		"IsTangerineWhistle": rules.IsTangerineWhistle,
		"IsHomestead":        rules.IsHomestead,
	} {
		if !enabled {
			t.Fatalf("%s should inherit as active at Osaka", name)
		}
	}
}

func TestRulesWithTimestampCancunInheritsAllEarlierForks(t *testing.T) {
	// Simulate HIVE: only CancunTime set, no block-based forks.
	// This is the exact scenario for EEST blockchain_test_engine tests.
	cfg := &ChainConfig{
		ChainID:    big.NewInt(1),
		CancunTime: big.NewInt(0),
	}

	rules := cfg.RulesWithTimestamp(0, 0)

	if !rules.IsCancun {
		t.Fatal("IsCancun should be true")
	}
	for name, enabled := range map[string]bool{
		"IsShanghai":         rules.IsShanghai,
		"IsLondon":           rules.IsLondon,
		"IsBerlin":           rules.IsBerlin,
		"IsIstanbul":         rules.IsIstanbul,
		"IsPetersburg":       rules.IsPetersburg,
		"IsConstantinople":   rules.IsConstantinople,
		"IsByzantium":        rules.IsByzantium,
		"IsSpuriousDragon":   rules.IsSpuriousDragon,
		"IsTangerineWhistle": rules.IsTangerineWhistle,
		"IsHomestead":        rules.IsHomestead,
	} {
		if !enabled {
			t.Fatalf("%s should inherit as active at Cancun (IsCancun implies all earlier forks)", name)
		}
	}
	// Must NOT imply later forks
	if rules.IsPrague {
		t.Fatal("IsPrague should NOT be set when only CancunTime is configured")
	}
	if rules.IsOsaka {
		t.Fatal("IsOsaka should NOT be set when only CancunTime is configured")
	}
}

func TestRulesWithTimestampShanghaiInheritsAllEarlierForks(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:      big.NewInt(1),
		ShanghaiTime: big.NewInt(0),
	}

	rules := cfg.RulesWithTimestamp(0, 0)

	if !rules.IsShanghai {
		t.Fatal("IsShanghai should be true")
	}
	if !rules.IsBerlin {
		t.Fatal("IsBerlin should inherit from IsShanghai")
	}
	if !rules.IsHomestead {
		t.Fatal("IsHomestead should inherit from IsShanghai")
	}
	if rules.IsCancun {
		t.Fatal("IsCancun should NOT be set when only ShanghaiTime is configured")
	}
}

func TestRulesWithTimestampDoesNotInferBlockForksFromLaterBlockForks(t *testing.T) {
	cfg := &ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(100),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(200),
		BerlinBlock:           big.NewInt(300),
		LondonBlock:           big.NewInt(0),
	}

	rules := cfg.RulesWithTimestamp(150, 150)
	if !rules.IsLondon {
		t.Fatal("IsLondon should follow explicit block activation")
	}
	if rules.IsIstanbul {
		t.Fatal("IsIstanbul should not be inferred from London for block-based schedules")
	}
	if rules.IsBerlin {
		t.Fatal("IsBerlin should not be inferred from London for block-based schedules")
	}
}

func TestChainConfigByChainName(t *testing.T) {
	mainnet := ChainConfigByChainName(networkname.MainnetChainName)
	if mainnet == nil {
		t.Error("ChainConfigByChainName(mainnet) returned nil")
	}

	testnet := ChainConfigByChainName(networkname.TestnetChainName)
	if testnet == nil {
		t.Error("ChainConfigByChainName(testnet) returned nil")
	}

	unknown := ChainConfigByChainName("unknown")
	if unknown != nil {
		t.Error("ChainConfigByChainName(unknown) should return nil")
	}
}

func TestGenesisHashByChainName(t *testing.T) {
	mainnet := GenesisHashByChainName(networkname.MainnetChainName)
	if mainnet == nil {
		t.Error("GenesisHashByChainName(mainnet) returned nil")
	}

	testnet := GenesisHashByChainName(networkname.TestnetChainName)
	if testnet == nil {
		t.Error("GenesisHashByChainName(testnet) returned nil")
	}

	unknown := GenesisHashByChainName("unknown")
	if unknown != nil {
		t.Error("GenesisHashByChainName(unknown) should return nil")
	}
}

func TestChainConfigByGenesisHash(t *testing.T) {
	mainnet := ChainConfigByGenesisHash(MainnetGenesisHash)
	if mainnet == nil {
		t.Error("ChainConfigByGenesisHash(mainnet) returned nil")
	}

	testnet := ChainConfigByGenesisHash(TestnetGenesisHash)
	if testnet == nil {
		t.Error("ChainConfigByGenesisHash(testnet) returned nil")
	}

	unknown := ChainConfigByGenesisHash(types.Hash{})
	if unknown != nil {
		t.Error("ChainConfigByGenesisHash(unknown) should return nil")
	}
}

func TestNetworkIDByChainName(t *testing.T) {
	mainnetID := NetworkIDByChainName(networkname.MainnetChainName)
	if mainnetID != 97 {
		t.Errorf("NetworkIDByChainName(mainnet) = %d, want 97", mainnetID)
	}

	testnetID := NetworkIDByChainName(networkname.TestnetChainName)
	if testnetID != 10042 {
		t.Errorf("NetworkIDByChainName(testnet) = %d, want 10042", testnetID)
	}

	unknownID := NetworkIDByChainName("unknown")
	if unknownID != 0 {
		t.Errorf("NetworkIDByChainName(unknown) = %d, want 0", unknownID)
	}
}

func TestConsensusConfigStrings(t *testing.T) {
	ethash := &EthashConfig{}
	if ethash.String() != "ethash" {
		t.Errorf("EthashConfig.String() = %s, want ethash", ethash.String())
	}

	clique := &CliqueConfig{Period: 15, Epoch: 30000}
	if clique.String() != "clique" {
		t.Errorf("CliqueConfig.String() = %s, want clique", clique.String())
	}

	aura := &AuRaConfig{}
	if aura.String() != "aura" {
		t.Errorf("AuRaConfig.String() = %s, want aura", aura.String())
	}

	parlia := &ParliaConfig{}
	if parlia.String() != "parlia" {
		t.Errorf("ParliaConfig.String() = %s, want parlia", parlia.String())
	}

	bor := &BorConfig{}
	if bor.String() != "bor" {
		t.Errorf("BorConfig.String() = %s, want bor", bor.String())
	}

	apos := &APosConfig{
		Period:      10,
		Epoch:       200,
		RewardEpoch: 100,
		RewardLimit: big.NewInt(1000),
	}
	if apos.String() == "" {
		t.Error("APosConfig.String() returned empty string")
	}
}

func TestIsHeaderWithSeal(t *testing.T) {
	auraCfg := &ChainConfig{Consensus: AuRaConsensus}
	if !auraCfg.IsHeaderWithSeal() {
		t.Error("IsHeaderWithSeal() should return true for AuRa consensus")
	}

	ethashCfg := &ChainConfig{Consensus: EtHashConsensus}
	if ethashCfg.IsHeaderWithSeal() {
		t.Error("IsHeaderWithSeal() should return false for Ethash consensus")
	}
}

func TestConfigCompatError(t *testing.T) {
	err := &ConfigCompatError{
		What:         "test fork",
		StoredConfig: big.NewInt(100),
		NewConfig:    big.NewInt(200),
		RewindTo:     99,
	}

	errStr := err.Error()
	if errStr == "" {
		t.Error("ConfigCompatError.Error() returned empty string")
	}
}

func TestNewSnapshotConfig(t *testing.T) {
	cfg := NewSnapshotConfig(100, 10, 20, true, "")
	if cfg == nil {
		t.Fatal("NewSnapshotConfig() returned nil")
	}

	if cfg.CheckpointInterval != 100 {
		t.Errorf("CheckpointInterval = %d, want 100", cfg.CheckpointInterval)
	}
	if cfg.InmemorySnapshots != 10 {
		t.Errorf("InmemorySnapshots = %d, want 10", cfg.InmemorySnapshots)
	}
	if cfg.InmemorySignatures != 20 {
		t.Errorf("InmemorySignatures = %d, want 20", cfg.InmemorySignatures)
	}
	if !cfg.InMemory {
		t.Error("InMemory should be true")
	}
}

func TestTrustedCheckpoint(t *testing.T) {
	cp := &TrustedCheckpoint{
		SectionIndex: 100,
		SectionHead:  types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		CHTRoot:      types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		BloomRoot:    types.HexToHash("0x567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234"),
	}

	// Test Hash
	hash := cp.Hash()
	if hash == (types.Hash{}) {
		t.Error("TrustedCheckpoint.Hash() returned empty hash")
	}

	// Test Empty
	if cp.Empty() {
		t.Error("TrustedCheckpoint.Empty() should return false for non-empty checkpoint")
	}

	// Test HashEqual
	if !cp.HashEqual(hash) {
		t.Error("TrustedCheckpoint.HashEqual() should return true for same hash")
	}
	if cp.HashEqual(types.Hash{}) {
		t.Error("TrustedCheckpoint.HashEqual() should return false for different hash")
	}

	// Test empty checkpoint
	emptyCP := &TrustedCheckpoint{}
	if !emptyCP.Empty() {
		t.Error("Empty TrustedCheckpoint.Empty() should return true")
	}
	if !emptyCP.HashEqual(types.Hash{}) {
		t.Error("Empty TrustedCheckpoint.HashEqual() should return true for empty hash")
	}
}

func TestBorConfigMethods(t *testing.T) {
	borCfg := &BorConfig{
		Period: map[string]uint64{
			"0":   5,
			"100": 10,
		},
		BackupMultiplier: map[string]uint64{
			"0":   2,
			"100": 3,
		},
		JaipurBlock: 50,
	}

	// Test CalculatePeriod
	period := borCfg.CalculatePeriod(50)
	if period != 5 {
		t.Errorf("CalculatePeriod(50) = %d, want 5", period)
	}

	period = borCfg.CalculatePeriod(150)
	if period != 10 {
		t.Errorf("CalculatePeriod(150) = %d, want 10", period)
	}

	// Test CalculateBackupMultiplier
	mult := borCfg.CalculateBackupMultiplier(50)
	if mult != 2 {
		t.Errorf("CalculateBackupMultiplier(50) = %d, want 2", mult)
	}

	// Test IsJaipur
	if borCfg.IsJaipur(40) {
		t.Error("IsJaipur(40) should return false")
	}
	if !borCfg.IsJaipur(50) {
		t.Error("IsJaipur(50) should return true")
	}
	if !borCfg.IsJaipur(100) {
		t.Error("IsJaipur(100) should return true")
	}
}

func TestCheckConfigForkOrder(t *testing.T) {
	// Valid fork order
	validCfg := &ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
		ShanghaiBlock:         big.NewInt(0),
		CancunBlock:           big.NewInt(0),
	}

	err := validCfg.CheckConfigForkOrder()
	if err != nil {
		t.Errorf("CheckConfigForkOrder() for valid config returned error: %v", err)
	}
}

func TestCheckCompatible(t *testing.T) {
	cfg1 := &ChainConfig{
		ChainID:        big.NewInt(1),
		HomesteadBlock: big.NewInt(10),
	}

	cfg2 := &ChainConfig{
		ChainID:        big.NewInt(1),
		HomesteadBlock: big.NewInt(10),
	}

	// Compatible configs
	err := cfg1.CheckCompatible(cfg2, 100)
	if err != nil {
		t.Errorf("CheckCompatible() for compatible configs returned error: %v", err)
	}

	// Incompatible configs
	cfg3 := &ChainConfig{
		ChainID:        big.NewInt(1),
		HomesteadBlock: big.NewInt(200),
	}

	err = cfg1.CheckCompatible(cfg3, 100)
	if err == nil {
		t.Error("CheckCompatible() for incompatible configs should return error")
	}
}
