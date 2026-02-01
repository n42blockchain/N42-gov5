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

package vm

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// EIP-7907: Contract Code Size Tests
// =============================================================================

func TestMaxCodeSizeFusaka(t *testing.T) {
	// Verify Fusaka code size limit is 48KB
	if MaxCodeSizeFusaka != 48*1024 {
		t.Errorf("MaxCodeSizeFusaka = %d, want %d", MaxCodeSizeFusaka, 48*1024)
	}
}

func TestMaxInitCodeSizeFusaka(t *testing.T) {
	// Verify Fusaka init code size limit is 96KB (2x code size)
	if MaxInitCodeSizeFusaka != 2*MaxCodeSizeFusaka {
		t.Errorf("MaxInitCodeSizeFusaka = %d, want %d", MaxInitCodeSizeFusaka, 2*MaxCodeSizeFusaka)
	}
}

func TestCalcCodeSizeGas(t *testing.T) {
	testCases := []struct {
		name     string
		codeSize uint64
		wantGas  uint64
	}{
		{
			name:     "below_threshold",
			codeSize: 20 * 1024, // 20KB
			wantGas:  0,
		},
		{
			name:     "at_threshold",
			codeSize: CodeSizeLimit7907, // 24KB
			wantGas:  0,
		},
		{
			name:     "above_threshold",
			codeSize: 30 * 1024, // 30KB
			wantGas:  (30*1024 - CodeSizeLimit7907) * CodeSizeMeteringBaseGas,
		},
		{
			name:     "max_fusaka_size",
			codeSize: MaxCodeSizeFusaka, // 48KB
			wantGas:  (MaxCodeSizeFusaka - CodeSizeLimit7907) * CodeSizeMeteringBaseGas,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gas := CalcCodeSizeGas(tc.codeSize)
			if gas != tc.wantGas {
				t.Errorf("CalcCodeSizeGas(%d) = %d, want %d", tc.codeSize, gas, tc.wantGas)
			}
		})
	}
}

func TestValidateCodeSize(t *testing.T) {
	testCases := []struct {
		name     string
		codeSize uint64
		isFusaka bool
		wantErr  bool
	}{
		{
			name:     "pre_fusaka_valid",
			codeSize: uint64(params.MaxCodeSize),
			isFusaka: false,
			wantErr:  false,
		},
		{
			name:     "pre_fusaka_invalid",
			codeSize: uint64(params.MaxCodeSize) + 1,
			isFusaka: false,
			wantErr:  true,
		},
		{
			name:     "fusaka_valid_old_limit",
			codeSize: uint64(params.MaxCodeSize),
			isFusaka: true,
			wantErr:  false,
		},
		{
			name:     "fusaka_valid_new_limit",
			codeSize: MaxCodeSizeFusaka,
			isFusaka: true,
			wantErr:  false,
		},
		{
			name:     "fusaka_invalid",
			codeSize: MaxCodeSizeFusaka + 1,
			isFusaka: true,
			wantErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCodeSize(tc.codeSize, tc.isFusaka)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateCodeSize(%d, %v) error = %v, wantErr %v",
					tc.codeSize, tc.isFusaka, err, tc.wantErr)
			}
		})
	}
}

// =============================================================================
// EIP-7823: MODEXP Upper Bound Tests
// =============================================================================

func TestModExpMaxInputLen(t *testing.T) {
	// Verify MODEXP max input length is 1024 bytes (8192 bits)
	if ModExpMaxInputLen != 1024 {
		t.Errorf("ModExpMaxInputLen = %d, want %d", ModExpMaxInputLen, 1024)
	}
}

func TestValidateModExpInput(t *testing.T) {
	testCases := []struct {
		name    string
		baseLen uint64
		expLen  uint64
		modLen  uint64
		wantErr error
	}{
		{
			name:    "valid_small",
			baseLen: 32,
			expLen:  32,
			modLen:  32,
			wantErr: nil,
		},
		{
			name:    "valid_at_limit",
			baseLen: ModExpMaxInputLen,
			expLen:  ModExpMaxInputLen,
			modLen:  ModExpMaxInputLen,
			wantErr: nil,
		},
		{
			name:    "base_too_large",
			baseLen: ModExpMaxInputLen + 1,
			expLen:  32,
			modLen:  32,
			wantErr: ErrModExpBaseTooLarge,
		},
		{
			name:    "exp_too_large",
			baseLen: 32,
			expLen:  ModExpMaxInputLen + 1,
			modLen:  32,
			wantErr: ErrModExpExpTooLarge,
		},
		{
			name:    "mod_too_large",
			baseLen: 32,
			expLen:  32,
			modLen:  ModExpMaxInputLen + 1,
			wantErr: ErrModExpModTooLarge,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModExpInput(tc.baseLen, tc.expLen, tc.modLen)
			if err != tc.wantErr {
				t.Errorf("ValidateModExpInput() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// =============================================================================
// EIP-7883: ModExp Gas Cost Tests
// =============================================================================

func TestModExpGasMultiplier(t *testing.T) {
	// Verify the multiplier is 3/2 (1.5x)
	ratio := float64(ModExpGasMultiplier7883Num) / float64(ModExpGasMultiplier7883Den)
	if ratio != 1.5 {
		t.Errorf("ModExp multiplier ratio = %f, want 1.5", ratio)
	}
}

func TestCalcModExpGas7883(t *testing.T) {
	testCases := []struct {
		name    string
		baseLen *big.Int
		expLen  *big.Int
		modLen  *big.Int
		expHead *big.Int
	}{
		{
			name:    "small_inputs",
			baseLen: big.NewInt(32),
			expLen:  big.NewInt(32),
			modLen:  big.NewInt(32),
			expHead: big.NewInt(1),
		},
		{
			name:    "medium_inputs",
			baseLen: big.NewInt(128),
			expLen:  big.NewInt(128),
			modLen:  big.NewInt(128),
			expHead: big.NewInt(255),
		},
		{
			name:    "large_inputs",
			baseLen: big.NewInt(512),
			expLen:  big.NewInt(512),
			modLen:  big.NewInt(512),
			expHead: big.NewInt(0),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			gas := CalcModExpGas7883(tc.baseLen, tc.expLen, tc.modLen, tc.expHead)
			
			// Verify gas is within reasonable bounds
			if gas < ModExpMinGas7883 {
				t.Errorf("CalcModExpGas7883() = %d, want >= %d", gas, ModExpMinGas7883)
			}
			if gas > ModExpMaxGas {
				t.Errorf("CalcModExpGas7883() = %d, want <= %d", gas, ModExpMaxGas)
			}
		})
	}
}

// =============================================================================
// EIP-7825: Transaction Gas Limit Tests
// =============================================================================

func TestTxGasLimitFusaka(t *testing.T) {
	// Verify transaction gas limit is 30M
	if TxGasLimitFusaka != 30_000_000 {
		t.Errorf("TxGasLimitFusaka = %d, want %d", TxGasLimitFusaka, 30_000_000)
	}
}

func TestValidateTxGasLimit(t *testing.T) {
	testCases := []struct {
		name     string
		gasLimit uint64
		isFusaka bool
		wantErr  bool
	}{
		{
			name:     "pre_fusaka_high",
			gasLimit: 100_000_000,
			isFusaka: false,
			wantErr:  false, // No limit before Fusaka
		},
		{
			name:     "fusaka_valid",
			gasLimit: TxGasLimitFusaka,
			isFusaka: true,
			wantErr:  false,
		},
		{
			name:     "fusaka_invalid",
			gasLimit: TxGasLimitFusaka + 1,
			isFusaka: true,
			wantErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTxGasLimit(tc.gasLimit, tc.isFusaka)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateTxGasLimit(%d, %v) error = %v, wantErr %v",
					tc.gasLimit, tc.isFusaka, err, tc.wantErr)
			}
		})
	}
}

// =============================================================================
// EIP-7892: Blob Parameters Tests
// =============================================================================

func TestFusakaBlobParams(t *testing.T) {
	params := DefaultFusakaBlobParams()

	// Verify blob parameters
	if params.MaxBlobsPerBlock != 12 {
		t.Errorf("MaxBlobsPerBlock = %d, want 12", params.MaxBlobsPerBlock)
	}
	if params.TargetBlobsPerBlock != 8 {
		t.Errorf("TargetBlobsPerBlock = %d, want 8", params.TargetBlobsPerBlock)
	}
}

// =============================================================================
// EIP-7918: Blob Base Fee Cap Tests
// =============================================================================

func TestBlobBaseFeeCap(t *testing.T) {
	// Verify the cap is 8192 Gwei
	if BlobBaseFeeCap7918 != 8192 {
		t.Errorf("BlobBaseFeeCap7918 = %d, want 8192", BlobBaseFeeCap7918)
	}
}

func TestCalcBlobBaseFeeFusaka(t *testing.T) {
	testCases := []struct {
		name          string
		excessBlobGas uint64
	}{
		{
			name:          "zero_excess",
			excessBlobGas: 0,
		},
		{
			name:          "small_excess",
			excessBlobGas: 100000,
		},
		{
			name:          "moderate_excess",
			excessBlobGas: 1_000_000,
		},
		{
			name:          "large_excess",
			excessBlobGas: 100_000_000_000, // Very high
		},
	}

	capWei := new(big.Int).Mul(big.NewInt(BlobBaseFeeCap7918), big.NewInt(1e9))

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fee := CalcBlobBaseFeeFusaka(tc.excessBlobGas)
			
			// Fee must be positive
			if fee.Sign() <= 0 {
				t.Errorf("CalcBlobBaseFeeFusaka(%d) = %s, want positive fee",
					tc.excessBlobGas, fee.String())
			}
			
			// Fee should never exceed cap
			if fee.Cmp(capWei) > 0 {
				t.Errorf("CalcBlobBaseFeeFusaka(%d) = %s, exceeds cap %s",
					tc.excessBlobGas, fee.String(), capWei.String())
			}
		})
	}
}

// =============================================================================
// EIP-7935: Default Gas Limit Tests
// =============================================================================

func TestDefaultBlockGasLimitFusaka(t *testing.T) {
	// Verify default gas limit is 60M
	if DefaultBlockGasLimitFusaka != 60_000_000 {
		t.Errorf("DefaultBlockGasLimitFusaka = %d, want %d",
			DefaultBlockGasLimitFusaka, 60_000_000)
	}
}

func TestGetDefaultBlockGasLimit(t *testing.T) {
	preFusaka := GetDefaultBlockGasLimit(false)
	if preFusaka != params.GenesisGasLimit {
		t.Errorf("GetDefaultBlockGasLimit(false) = %d, want %d",
			preFusaka, params.GenesisGasLimit)
	}

	fusaka := GetDefaultBlockGasLimit(true)
	if fusaka != DefaultBlockGasLimitFusaka {
		t.Errorf("GetDefaultBlockGasLimit(true) = %d, want %d",
			fusaka, DefaultBlockGasLimitFusaka)
	}
}

// =============================================================================
// EIP-7934: RLP Block Size Tests
// =============================================================================

func TestMaxRLPBlockSizeFusaka(t *testing.T) {
	// Verify max RLP block size is 10MB
	if MaxRLPBlockSizeFusaka != 10*1024*1024 {
		t.Errorf("MaxRLPBlockSizeFusaka = %d, want %d",
			MaxRLPBlockSizeFusaka, 10*1024*1024)
	}
}

func TestValidateRLPBlockSize(t *testing.T) {
	testCases := []struct {
		name     string
		size     uint64
		isFusaka bool
		wantErr  bool
	}{
		{
			name:     "pre_fusaka_large",
			size:     100 * 1024 * 1024, // 100MB
			isFusaka: false,
			wantErr:  false, // No limit before Fusaka
		},
		{
			name:     "fusaka_valid",
			size:     MaxRLPBlockSizeFusaka,
			isFusaka: true,
			wantErr:  false,
		},
		{
			name:     "fusaka_invalid",
			size:     MaxRLPBlockSizeFusaka + 1,
			isFusaka: true,
			wantErr:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRLPBlockSize(tc.size, tc.isFusaka)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateRLPBlockSize(%d, %v) error = %v, wantErr %v",
					tc.size, tc.isFusaka, err, tc.wantErr)
			}
		})
	}
}

// =============================================================================
// EIP-7951: P-256 Precompile Tests
// =============================================================================

func TestP256VerifyAddress(t *testing.T) {
	// Verify P256VERIFY precompile address
	expectedAddr := "0x0000000000000000000000000000000000000100"
	if P256VerifyAddress.Hex() != expectedAddr {
		t.Errorf("P256VerifyAddress = %s, want %s",
			P256VerifyAddress.Hex(), expectedAddr)
	}
}

func TestP256VerifyGasFusaka(t *testing.T) {
	// Verify P256VERIFY gas cost for Fusaka
	if P256VerifyGasFusaka != 3450 {
		t.Errorf("P256VerifyGasFusaka = %d, want 3450", P256VerifyGasFusaka)
	}
}

// =============================================================================
// Fusaka Instruction Set Tests
// =============================================================================

func TestFusakaInstructionSetExists(t *testing.T) {
	jt := GetFusakaInstructionSet()
	
	// Verify basic opcodes exist
	if jt[ADD] == nil {
		t.Error("Fusaka instruction set missing ADD opcode")
	}
	if jt[MUL] == nil {
		t.Error("Fusaka instruction set missing MUL opcode")
	}
	
	// Verify EOF opcodes exist (inherited from Osaka)
	if jt[RJUMP] == nil {
		t.Error("Fusaka instruction set missing RJUMP opcode")
	}
	if jt[CALLF] == nil {
		t.Error("Fusaka instruction set missing CALLF opcode")
	}
	
	// Verify Prague opcodes exist
	if jt[CLZ] == nil {
		t.Error("Fusaka instruction set missing CLZ opcode")
	}
}

// =============================================================================
// Fusaka Config Tests
// =============================================================================

func TestDefaultFusakaConfig(t *testing.T) {
	cfg := DefaultFusakaConfig()

	if cfg.MaxCodeSize != MaxCodeSizeFusaka {
		t.Errorf("FusakaConfig.MaxCodeSize = %d, want %d",
			cfg.MaxCodeSize, MaxCodeSizeFusaka)
	}
	if cfg.TxGasLimit != TxGasLimitFusaka {
		t.Errorf("FusakaConfig.TxGasLimit = %d, want %d",
			cfg.TxGasLimit, TxGasLimitFusaka)
	}
	if cfg.ModExpMaxGas != ModExpMaxGas {
		t.Errorf("FusakaConfig.ModExpMaxGas = %d, want %d",
			cfg.ModExpMaxGas, ModExpMaxGas)
	}
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkCalcCodeSizeGas(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalcCodeSizeGas(40 * 1024) // 40KB
	}
}

func BenchmarkCalcModExpGas7883(b *testing.B) {
	baseLen := big.NewInt(256)
	expLen := big.NewInt(256)
	modLen := big.NewInt(256)
	expHead := big.NewInt(1)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcModExpGas7883(baseLen, expLen, modLen, expHead)
	}
}

func BenchmarkCalcBlobBaseFeeFusaka(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalcBlobBaseFeeFusaka(1000000)
	}
}

// =============================================================================
// EIP-7939: CLZ (Count Leading Zeros) Execution Tests
// =============================================================================

func TestOpCLZExecution(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte // 32-byte big-endian value
		expected uint64
	}{
		{
			name:     "zero_value",
			input:    make([]byte, 32), // All zeros
			expected: 256,
		},
		{
			name:     "one",
			input:    append(make([]byte, 31), 1), // 0x00...01
			expected: 255,
		},
		{
			name:     "two",
			input:    append(make([]byte, 31), 2), // 0x00...02
			expected: 254,
		},
		{
			name:     "max_byte",
			input:    append(make([]byte, 31), 0xff), // 0x00...FF
			expected: 248,
		},
		{
			name:     "high_bit_set",
			input:    append([]byte{0x80}, make([]byte, 31)...), // 0x80...00
			expected: 0,
		},
		{
			name:     "all_ones",
			input:    func() []byte { b := make([]byte, 32); for i := range b { b[i] = 0xff }; return b }(),
			expected: 0,
		},
		{
			name:     "mid_value",
			input:    append(make([]byte, 16), append([]byte{0x01}, make([]byte, 15)...)...), // 0x00..01..00
			expected: 128 + 7, // 16 bytes of zeros + 7 leading zeros in 0x01
		},
		{
			name:     "second_byte",
			input:    append(make([]byte, 30), []byte{0x01, 0x00}...), // 0x00...0100
			expected: 247,
		},
		{
			name:     "power_of_two",
			input:    append(make([]byte, 28), []byte{0x10, 0x00, 0x00, 0x00}...), // 0x10000000
			expected: 224 + 3, // 28 bytes of zeros + 3 leading zeros in 0x10
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create scope with CLZ opcode
			code := []byte{byte(CLZ)}
			scope := &ScopeContext{
				Stack:    stack.New(),
				Memory:   NewMemory(),
				Contract: &Contract{Code: code},
			}

			// Push input value
			value := new(uint256.Int).SetBytes(tt.input)
			scope.Stack.Push(value)

			pc := uint64(0)
			_, err := opCLZ(&pc, nil, scope)

			if err != nil {
				t.Fatalf("opCLZ returned error: %v", err)
			}

			result := scope.Stack.Pop()
			if result.Uint64() != tt.expected {
				t.Errorf("opCLZ(%x) = %d, want %d", tt.input, result.Uint64(), tt.expected)
			}
		})
	}

	t.Log("CLZ execution tests passed")
}

func TestOpCLZInstructionSet(t *testing.T) {
	// Verify CLZ is properly configured in Fusaka
	jt := newFusakaInstructionSet()

	if jt[CLZ] == nil {
		t.Fatal("CLZ not in Fusaka instruction set")
	}

	// Check execute function
	if jt[CLZ].execute == nil {
		t.Error("CLZ execute function is nil")
	}

	// Check stack requirements
	if jt[CLZ].numPop != 1 {
		t.Errorf("CLZ numPop = %d, want 1", jt[CLZ].numPop)
	}
	if jt[CLZ].numPush != 1 {
		t.Errorf("CLZ numPush = %d, want 1", jt[CLZ].numPush)
	}

	// Check gas cost
	if jt[CLZ].constantGas != GasFastStep {
		t.Errorf("CLZ gas = %d, want %d", jt[CLZ].constantGas, GasFastStep)
	}

	t.Log("CLZ instruction set configuration verified")
}

func BenchmarkOpCLZ(b *testing.B) {
	code := []byte{byte(CLZ)}
	scope := &ScopeContext{
		Stack:    stack.New(),
		Memory:   NewMemory(),
		Contract: &Contract{Code: code},
	}

	// Use a value that exercises the algorithm
	value := new(uint256.Int).SetBytes(append(make([]byte, 16), make([]byte, 16)...))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scope.Stack.Push(value.Clone())
		pc := uint64(0)
		opCLZ(&pc, nil, scope)
	}
}

