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

package runtime

import (
	"encoding/binary"
	"math/big"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Config Tests
// =============================================================================

func TestSetDefaults(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	if cfg.ChainConfig == nil {
		t.Error("ChainConfig should be set")
	}
	if cfg.Difficulty == nil {
		t.Error("Difficulty should be set")
	}
	if cfg.Time == nil {
		t.Error("Time should be set")
	}
	if cfg.GasLimit == 0 {
		t.Error("GasLimit should be set")
	}
	if cfg.GasPrice == nil {
		t.Error("GasPrice should be set")
	}
	if cfg.Value == nil {
		t.Error("Value should be set")
	}
	if cfg.BlockNumber == nil {
		t.Error("BlockNumber should be set")
	}
	if cfg.GetHashFn == nil {
		t.Error("GetHashFn should be set")
	}
}

func TestSetDefaultsChainConfig(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

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

	if cfg.ChainConfig.ChainID.Cmp(customChainID) != 0 {
		t.Error("Custom ChainID should be preserved")
	}
	if cfg.Difficulty.Cmp(customDifficulty) != 0 {
		t.Error("Custom Difficulty should be preserved")
	}
	if cfg.GasLimit != customGasLimit {
		t.Error("Custom GasLimit should be preserved")
	}
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
}

func TestGetHashFn(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)

	// GetHashFn must be deterministic.
	hash1 := cfg.GetHashFn(100)
	hash2 := cfg.GetHashFn(100)
	if hash1 != hash2 {
		t.Error("GetHashFn should return same hash for same input")
	}

	// Different inputs should produce different hashes.
	hash3 := cfg.GetHashFn(101)
	if hash1 == hash3 {
		t.Error("GetHashFn should return different hash for different input")
	}
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
}

// =============================================================================
// Nil Config Tests
// =============================================================================

func TestNilConfigHandling(t *testing.T) {
	// Execute, Create, and Call handle nil config by allocating a new one
	// internally. Here we verify setDefaults works on a zero-value Config.
	cfg := new(Config)
	setDefaults(cfg)
	if cfg.ChainConfig == nil {
		t.Error("setDefaults should work on new(Config)")
	}
}

// =============================================================================
// Address Generation Tests
// =============================================================================

func TestContractAddressGeneration(t *testing.T) {
	contractBytes := []byte("contract")
	addr := types.BytesToAddress(contractBytes)

	if addr == (types.Address{}) {
		t.Error("Generated address should not be zero")
	}

	// Same input must produce the same address (deterministic).
	addr2 := types.BytesToAddress(contractBytes)
	if addr != addr2 {
		t.Error("Same bytes should give same address")
	}
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

	// All forks should be at block 0 (enabled from genesis).
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
}

func buildRuntimeModExpInput(baseLen, expLen, modLen uint64, base, exponent, modulus []byte) []byte {
	input := make([]byte, 96+baseLen+expLen+modLen)
	binary.BigEndian.PutUint64(input[24:32], baseLen)
	binary.BigEndian.PutUint64(input[56:64], expLen)
	binary.BigEndian.PutUint64(input[88:96], modLen)

	offset := 96
	copy(input[offset:offset+int(baseLen)], base)
	offset += int(baseLen)
	copy(input[offset:offset+int(expLen)], exponent)
	offset += int(expLen)
	copy(input[offset:offset+int(modLen)], modulus)
	return input
}

func testRuntimeChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1),
		HomesteadBlock:        new(big.Int),
		TangerineWhistleBlock: new(big.Int),
		SpuriousDragonBlock:   new(big.Int),
		ByzantiumBlock:        new(big.Int),
		ConstantinopleBlock:   new(big.Int),
		PetersburgBlock:       new(big.Int),
		IstanbulBlock:         new(big.Int),
		MuirGlacierBlock:      new(big.Int),
		BerlinBlock:           new(big.Int),
		LondonBlock:           new(big.Int),
		ArrowGlacierBlock:     new(big.Int),
		GrayGlacierBlock:      new(big.Int),
		ShanghaiBlock:         new(big.Int),
		CancunBlock:           new(big.Int),
		PragueTime:            new(big.Int),
		OsakaTime:             big.NewInt(1),
	}
}

func TestCallUsesOsakaModExpGasByTimestamp(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))

	base := make([]byte, 1024)
	for i := range base {
		base[i] = 0x01
	}
	input := buildRuntimeModExpInput(1024, 1, 1, base, []byte{0x00}, []byte{0x02})
	modexpAddr := types.BytesToAddress([]byte{5})

	suppliedGas := uint64(500000)
	osakaCfg := &Config{
		ChainConfig: testRuntimeChainConfig(),
		Origin:      types.HexToAddress("0x1000000000000000000000000000000000000001"),
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(1),
		GasLimit:    suppliedGas,
		GasPrice:    uint256.NewInt(0),
		Value:       uint256.NewInt(0),
		State:       ibs,
	}

	_, remainingGas, err := Call(modexpAddr, input, osakaCfg)
	if err != nil {
		t.Fatalf("Osaka runtime.Call failed: %v", err)
	}
	osakaGasUsed := suppliedGas - remainingGas
	if osakaGasUsed != 32768 {
		t.Fatalf("Osaka runtime.Call gas used = %d, want 32768", osakaGasUsed)
	}

	pragueCfg := &Config{
		ChainConfig: testRuntimeChainConfig(),
		Origin:      types.HexToAddress("0x1000000000000000000000000000000000000002"),
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(0),
		GasLimit:    suppliedGas,
		GasPrice:    uint256.NewInt(0),
		Value:       uint256.NewInt(0),
		State:       state.New(state.NewPlainState(tx, 1)),
	}

	_, remainingGas, err = Call(modexpAddr, input, pragueCfg)
	if err != nil {
		t.Fatalf("Pre-Osaka runtime.Call failed: %v", err)
	}
	pragueGasUsed := suppliedGas - remainingGas
	if pragueGasUsed != 5461 {
		t.Fatalf("Pre-Osaka runtime.Call gas used = %d, want 5461", pragueGasUsed)
	}
}

func TestCallAllowsPreOsakaModExpOverflowingExponentLengthWithZeroModulus(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)

	modexpAddr := types.BytesToAddress([]byte{5})
	input := make([]byte, 96)
	input[32] = 0x80 // exponent length = 2^255, baseLen=0, modLen=0
	suppliedGas := uint64(90000)

	preOsakaCfg := &Config{
		ChainConfig: testRuntimeChainConfig(),
		Origin:      types.HexToAddress("0x1000000000000000000000000000000000000003"),
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(0),
		GasLimit:    suppliedGas,
		GasPrice:    uint256.NewInt(0),
		Value:       uint256.NewInt(0),
		State:       state.New(state.NewPlainState(tx, 1)),
	}

	ret, remainingGas, err := Call(modexpAddr, input, preOsakaCfg)
	if err != nil {
		t.Fatalf("Pre-Osaka runtime.Call failed: %v", err)
	}
	if len(ret) != 0 {
		t.Fatalf("Pre-Osaka runtime.Call result = %x, want empty output", ret)
	}
	if gasUsed := suppliedGas - remainingGas; gasUsed != 200 {
		t.Fatalf("Pre-Osaka runtime.Call gas used = %d, want 200", gasUsed)
	}

	osakaCfg := &Config{
		ChainConfig: testRuntimeChainConfig(),
		Origin:      types.HexToAddress("0x1000000000000000000000000000000000000004"),
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(1),
		GasLimit:    suppliedGas,
		GasPrice:    uint256.NewInt(0),
		Value:       uint256.NewInt(0),
		State:       state.New(state.NewPlainState(tx, 1)),
	}

	if _, _, err := Call(modexpAddr, input, osakaCfg); err != vm.ErrOutOfGas {
		t.Fatalf("Osaka runtime.Call error = %v, want %v", err, vm.ErrOutOfGas)
	}
}

func TestCallResolvesDelegationCodeInPectra(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))

	delegatedAddr := types.HexToAddress("0x10000000000000000000000000000000000000aa")
	targetAddr := types.HexToAddress("0x20000000000000000000000000000000000000bb")

	// ADDRESS; PUSH1 0x00; MSTORE; PUSH1 0x20; PUSH1 0x00; RETURN
	targetCode := []byte{byte(vm.ADDRESS), byte(vm.PUSH1), 0x00, byte(vm.MSTORE), byte(vm.PUSH1), 0x20, byte(vm.PUSH1), 0x00, byte(vm.RETURN)}

	ibs.CreateAccount(targetAddr, true)
	ibs.SetCode(targetAddr, targetCode)
	ibs.CreateAccount(delegatedAddr, true)
	ibs.SetCode(delegatedAddr, vm.AddressToDelegation(targetAddr))

	cfg := &Config{
		ChainConfig: testRuntimeChainConfig(),
		Origin:      types.HexToAddress("0x30000000000000000000000000000000000000cc"),
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(1),
		GasLimit:    100000,
		GasPrice:    uint256.NewInt(0),
		Value:       uint256.NewInt(0),
		State:       ibs,
	}

	ret, _, err := Call(delegatedAddr, nil, cfg)
	if err != nil {
		t.Fatalf("runtime.Call via delegated account failed: %v", err)
	}
	if len(ret) != 32 {
		t.Fatalf("delegated call return length = %d, want 32", len(ret))
	}
	if got := types.BytesToAddress(ret[12:32]); got != delegatedAddr {
		t.Fatalf("delegated call ADDRESS = %s, want %s", got, delegatedAddr)
	}
}

func appendPush(code []byte, value uint64) []byte {
	if value == 0 {
		return append(code, byte(vm.PUSH0))
	}
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], value)
	i := 0
	for i < len(buf) && buf[i] == 0 {
		i++
	}
	n := len(buf) - i
	code = append(code, byte(vm.PUSH1)+byte(n-1))
	return append(code, buf[i:]...)
}

func appendPushAddress(code []byte, addr types.Address) []byte {
	code = append(code, byte(vm.PUSH20))
	return append(code, addr.Bytes()...)
}

func appendCall(code []byte, op vm.OpCode, addr types.Address) []byte {
	code = appendPush(code, 0) // outSize
	code = appendPush(code, 0) // outOffset
	code = appendPush(code, 0) // inSize
	code = appendPush(code, 0) // inOffset
	if op == vm.CALL || op == vm.CALLCODE {
		code = appendPush(code, 0) // value
	}
	code = appendPushAddress(code, addr)
	code = append(code, byte(vm.GAS), byte(op))
	return code
}

func appendSstore(code []byte, slot uint64) []byte {
	code = appendPush(code, slot)
	return append(code, byte(vm.SSTORE))
}

func TestCallIntoChainDelegatingSetCodeReturnsFailureWithoutInvalidatingOuterCall(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))

	authSigner1 := types.HexToAddress("0x1000000000000000000000000000000000000011")
	authSigner2 := types.HexToAddress("0x2000000000000000000000000000000000000022")
	entry := types.HexToAddress("0x3000000000000000000000000000000000000033")
	origin := types.HexToAddress("0x4000000000000000000000000000000000000044")

	ibs.CreateAccount(authSigner1, true)
	ibs.CreateAccount(authSigner2, true)
	ibs.CreateAccount(entry, true)
	ibs.SetCode(authSigner1, vm.AddressToDelegation(authSigner2))
	ibs.SetCode(authSigner2, vm.AddressToDelegation(authSigner1))
	entryCode := appendCall(nil, vm.CALL, authSigner1)
	entryCode = appendSstore(entryCode, 0)
	entryCode = append(entryCode, byte(vm.STOP))
	ibs.SetCode(entry, entryCode)

	cfg := &Config{
		ChainConfig: testRuntimeChainConfig(),
		Origin:      origin,
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(1),
		GasLimit:    10_000_000,
		GasPrice:    uint256.NewInt(0),
		Value:       uint256.NewInt(0),
		State:       ibs,
	}
	rules := cfg.ChainConfig.Rules(1)
	ibs.PrepareAccessList(origin, &entry, vm.ActivePrecompiles(rules), nil)
	ibs.AddAddressToAccessList(authSigner1)
	ibs.AddAddressToAccessList(authSigner2)

	if _, _, err := Call(entry, nil, cfg); err != nil {
		t.Fatalf("runtime.Call failed: %v", err)
	}
	var got uint256.Int
	slot := types.Hash{}
	ibs.GetState(entry, &slot, &got)
	if got.Sign() != 0 {
		t.Fatalf("entry storage[0] = %d, want 0", got.Uint64())
	}
}

func TestCallIntoDelegatedAuthorityKeepsTransientStorageScopedToAuthority(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))

	authSigner := types.HexToAddress("0x5000000000000000000000000000000000000055")
	target := types.HexToAddress("0x6000000000000000000000000000000000000066")
	entry := types.HexToAddress("0x7000000000000000000000000000000000000077")
	origin := types.HexToAddress("0x8000000000000000000000000000000000000088")

	ibs.CreateAccount(authSigner, true)
	ibs.CreateAccount(target, true)
	ibs.CreateAccount(entry, true)
	ibs.SetCode(authSigner, vm.AddressToDelegation(target))
	targetCode := appendPush(nil, 2)
	targetCode = append(targetCode, byte(vm.TLOAD))
	targetCode = appendSstore(targetCode, 1)
	targetCode = appendPush(targetCode, 3)
	targetCode = appendPush(targetCode, 2)
	targetCode = append(targetCode, byte(vm.TSTORE), byte(vm.STOP))
	ibs.SetCode(target, targetCode)
	entryCode := appendCall(nil, vm.CALL, authSigner)
	entryCode = append(entryCode, byte(vm.POP))
	entryCode = appendCall(entryCode, vm.CALL, target)
	entryCode = append(entryCode, byte(vm.POP), byte(vm.STOP))
	ibs.SetCode(entry, entryCode)

	cfg := &Config{
		ChainConfig: testRuntimeChainConfig(),
		Origin:      origin,
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(1),
		GasLimit:    100_000,
		GasPrice:    uint256.NewInt(0),
		Value:       uint256.NewInt(0),
		State:       ibs,
	}
	rules := cfg.ChainConfig.Rules(1)
	ibs.PrepareAccessList(origin, &entry, vm.ActivePrecompiles(rules), nil)
	ibs.AddAddressToAccessList(authSigner)
	ibs.AddAddressToAccessList(target)

	if _, _, err := Call(entry, nil, cfg); err != nil {
		t.Fatalf("runtime.Call failed: %v", err)
	}
	slot1 := types.Hash{}
	slot1[31] = 1
	var authorityStored, targetStored uint256.Int
	ibs.GetState(authSigner, &slot1, &authorityStored)
	ibs.GetState(target, &slot1, &targetStored)
	if authorityStored.Sign() != 0 {
		t.Fatalf("authority storage[1] = %d, want 0", authorityStored.Uint64())
	}
	if targetStored.Sign() != 0 {
		t.Fatalf("target storage[1] = %d, want 0", targetStored.Uint64())
	}
}

func TestCallWarmStateAcrossAuthorityAndDelegationTarget(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))

	authSigner := types.HexToAddress("0x9000000000000000000000000000000000000099")
	target := types.HexToAddress("0xa0000000000000000000000000000000000000aa")
	entry := types.HexToAddress("0xb0000000000000000000000000000000000000bb")
	origin := types.HexToAddress("0xc0000000000000000000000000000000000000cc")

	ibs.CreateAccount(authSigner, true)
	ibs.CreateAccount(target, true)
	ibs.CreateAccount(entry, true)
	ibs.SetCode(authSigner, vm.AddressToDelegation(target))
	ibs.SetCode(target, []byte{byte(vm.STOP)})
	entryCode := appendCall(nil, vm.CALL, authSigner)
	entryCode = appendSstore(entryCode, 1)
	entryCode = appendCall(entryCode, vm.CALL, target)
	entryCode = appendSstore(entryCode, 1)
	entryCode = appendPush(entryCode, 1)
	entryCode = appendSstore(entryCode, 2)
	entryCode = append(entryCode, byte(vm.STOP))
	ibs.SetCode(entry, entryCode)

	cfg := &Config{
		ChainConfig: testRuntimeChainConfig(),
		Origin:      origin,
		BlockNumber: big.NewInt(1),
		Time:        big.NewInt(1),
		GasLimit:    1_000_000,
		GasPrice:    uint256.NewInt(0),
		Value:       uint256.NewInt(0),
		State:       ibs,
	}
	rules := cfg.ChainConfig.Rules(1)
	ibs.PrepareAccessList(origin, &entry, vm.ActivePrecompiles(rules), nil)
	ibs.AddAddressToAccessList(authSigner)
	ibs.AddAddressToAccessList(target)

	if _, _, err := Call(entry, nil, cfg); err != nil {
		t.Fatalf("runtime.Call failed: %v", err)
	}
	slot1 := types.Hash{}
	slot1[31] = 1
	slot2 := types.Hash{}
	slot2[31] = 2
	var lastResult, success uint256.Int
	ibs.GetState(entry, &slot1, &lastResult)
	ibs.GetState(entry, &slot2, &success)
	if lastResult.Uint64() != 1 {
		t.Fatalf("entry storage[1] = %d, want 1", lastResult.Uint64())
	}
	if success.Uint64() != 1 {
		t.Fatalf("entry storage[2] = %d, want 1", success.Uint64())
	}
}
