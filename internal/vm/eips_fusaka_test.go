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
	"bytes"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/params"
)

// EIP-7907: Contract Code Size

func TestMaxCodeSizeFusaka(t *testing.T) {
	if MaxCodeSizeFusaka != 48*1024 {
		t.Errorf("MaxCodeSizeFusaka = %d, want %d", MaxCodeSizeFusaka, 48*1024)
	}
}

func TestMaxInitCodeSizeFusaka(t *testing.T) {
	if MaxInitCodeSizeFusaka != 2*MaxCodeSizeFusaka {
		t.Errorf("MaxInitCodeSizeFusaka = %d, want %d", MaxInitCodeSizeFusaka, 2*MaxCodeSizeFusaka)
	}
}

func TestCalcCodeSizeGas(t *testing.T) {
	tests := []struct {
		name     string
		codeSize uint64
		wantGas  uint64
	}{
		{"below_threshold", 20 * 1024, 0},
		{"at_threshold", CodeSizeLimit7907, 0},
		{"above_threshold", 30 * 1024, (30*1024 - CodeSizeLimit7907) * CodeSizeMeteringBaseGas},
		{"max_fusaka_size", MaxCodeSizeFusaka, (MaxCodeSizeFusaka - CodeSizeLimit7907) * CodeSizeMeteringBaseGas},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gas := CalcCodeSizeGas(tc.codeSize)
			if gas != tc.wantGas {
				t.Errorf("CalcCodeSizeGas(%d) = %d, want %d", tc.codeSize, gas, tc.wantGas)
			}
		})
	}
}

func TestValidateCodeSize(t *testing.T) {
	tests := []struct {
		name     string
		codeSize uint64
		isFusaka bool
		wantErr  bool
	}{
		{"pre_fusaka_valid", uint64(params.MaxCodeSize), false, false},
		{"pre_fusaka_invalid", uint64(params.MaxCodeSize) + 1, false, true},
		{"fusaka_valid_old_limit", uint64(params.MaxCodeSize), true, false},
		{"fusaka_valid_new_limit", MaxCodeSizeFusaka, true, false},
		{"fusaka_invalid", MaxCodeSizeFusaka + 1, true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCodeSize(tc.codeSize, tc.isFusaka)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateCodeSize(%d, %v) error = %v, wantErr %v",
					tc.codeSize, tc.isFusaka, err, tc.wantErr)
			}
		})
	}
}

// EIP-7823: MODEXP Upper Bound

func TestModExpMaxInputLen(t *testing.T) {
	if ModExpMaxInputLen != 1024 {
		t.Errorf("ModExpMaxInputLen = %d, want 1024", ModExpMaxInputLen)
	}
}

func TestValidateModExpInput(t *testing.T) {
	tests := []struct {
		name    string
		baseLen uint64
		expLen  uint64
		modLen  uint64
		wantErr error
	}{
		{"valid_small", 32, 32, 32, nil},
		{"valid_at_limit", ModExpMaxInputLen, ModExpMaxInputLen, ModExpMaxInputLen, nil},
		{"base_too_large", ModExpMaxInputLen + 1, 32, 32, ErrModExpBaseTooLarge},
		{"exp_too_large", 32, ModExpMaxInputLen + 1, 32, ErrModExpExpTooLarge},
		{"mod_too_large", 32, 32, ModExpMaxInputLen + 1, ErrModExpModTooLarge},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateModExpInput(tc.baseLen, tc.expLen, tc.modLen)
			if err != tc.wantErr {
				t.Errorf("ValidateModExpInput() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// EIP-7883: ModExp Gas Cost

func TestModExpGasMultiplier(t *testing.T) {
	ratio := float64(ModExpGasMultiplier7883Num) / float64(ModExpGasMultiplier7883Den)
	if ratio != 1.5 {
		t.Errorf("ModExp multiplier ratio = %f, want 1.5", ratio)
	}
}

func TestCalcModExpGas7883(t *testing.T) {
	tests := []struct {
		name    string
		baseLen *big.Int
		expLen  *big.Int
		modLen  *big.Int
		expHead *big.Int
	}{
		{"small_inputs", big.NewInt(32), big.NewInt(32), big.NewInt(32), big.NewInt(1)},
		{"medium_inputs", big.NewInt(128), big.NewInt(128), big.NewInt(128), big.NewInt(255)},
		{"large_inputs", big.NewInt(512), big.NewInt(512), big.NewInt(512), big.NewInt(0)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gas := CalcModExpGas7883(tc.baseLen, tc.expLen, tc.modLen, tc.expHead)
			if gas < ModExpMinGas7883 {
				t.Errorf("CalcModExpGas7883() = %d, want >= %d", gas, ModExpMinGas7883)
			}
			if gas > ModExpMaxGas {
				t.Errorf("CalcModExpGas7883() = %d, want <= %d", gas, ModExpMaxGas)
			}
		})
	}
}

// EIP-7825: Transaction Gas Limit

func TestTxGasLimitFusaka(t *testing.T) {
	if TxGasLimitFusaka != 30_000_000 {
		t.Errorf("TxGasLimitFusaka = %d, want 30000000", TxGasLimitFusaka)
	}
}

func TestValidateTxGasLimit(t *testing.T) {
	tests := []struct {
		name     string
		gasLimit uint64
		isFusaka bool
		wantErr  bool
	}{
		{"pre_fusaka_high", 100_000_000, false, false},
		{"fusaka_valid", TxGasLimitFusaka, true, false},
		{"fusaka_invalid", TxGasLimitFusaka + 1, true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTxGasLimit(tc.gasLimit, tc.isFusaka)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateTxGasLimit(%d, %v) error = %v, wantErr %v",
					tc.gasLimit, tc.isFusaka, err, tc.wantErr)
			}
		})
	}
}

// EIP-7892: Blob Parameters

func TestFusakaBlobParams(t *testing.T) {
	p := DefaultFusakaBlobParams()
	if p.MaxBlobsPerBlock != 12 {
		t.Errorf("MaxBlobsPerBlock = %d, want 12", p.MaxBlobsPerBlock)
	}
	if p.TargetBlobsPerBlock != 8 {
		t.Errorf("TargetBlobsPerBlock = %d, want 8", p.TargetBlobsPerBlock)
	}
}

// EIP-7918: Blob Base Fee Cap

func TestBlobBaseFeeCap(t *testing.T) {
	if BlobBaseFeeCap7918 != 8192 {
		t.Errorf("BlobBaseFeeCap7918 = %d, want 8192", BlobBaseFeeCap7918)
	}
}

func TestCalcBlobBaseFeeFusaka(t *testing.T) {
	tests := []struct {
		name          string
		excessBlobGas uint64
	}{
		{"zero_excess", 0},
		{"small_excess", 100000},
		{"moderate_excess", 1_000_000},
		{"large_excess", 100_000_000_000},
	}

	capWei := new(big.Int).Mul(big.NewInt(BlobBaseFeeCap7918), big.NewInt(1e9))

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fee := CalcBlobBaseFeeFusaka(tc.excessBlobGas)
			if fee.Sign() <= 0 {
				t.Errorf("CalcBlobBaseFeeFusaka(%d) = %s, want positive", tc.excessBlobGas, fee)
			}
			if fee.Cmp(capWei) > 0 {
				t.Errorf("CalcBlobBaseFeeFusaka(%d) = %s, exceeds cap %s", tc.excessBlobGas, fee, capWei)
			}
		})
	}
}

// EIP-7935: Default Gas Limit

func TestDefaultBlockGasLimitFusaka(t *testing.T) {
	if DefaultBlockGasLimitFusaka != 60_000_000 {
		t.Errorf("DefaultBlockGasLimitFusaka = %d, want 60000000", DefaultBlockGasLimitFusaka)
	}
}

func TestGetDefaultBlockGasLimit(t *testing.T) {
	if got := GetDefaultBlockGasLimit(false); got != params.GenesisGasLimit {
		t.Errorf("GetDefaultBlockGasLimit(false) = %d, want %d", got, params.GenesisGasLimit)
	}
	if got := GetDefaultBlockGasLimit(true); got != DefaultBlockGasLimitFusaka {
		t.Errorf("GetDefaultBlockGasLimit(true) = %d, want %d", got, DefaultBlockGasLimitFusaka)
	}
}

// EIP-7934: RLP Block Size

func TestMaxRLPBlockSizeFusaka(t *testing.T) {
	if MaxRLPBlockSizeFusaka != 10*1024*1024 {
		t.Errorf("MaxRLPBlockSizeFusaka = %d, want %d", MaxRLPBlockSizeFusaka, 10*1024*1024)
	}
}

func TestValidateRLPBlockSize(t *testing.T) {
	tests := []struct {
		name     string
		size     uint64
		isFusaka bool
		wantErr  bool
	}{
		{"pre_fusaka_large", 100 * 1024 * 1024, false, false},
		{"fusaka_valid", MaxRLPBlockSizeFusaka, true, false},
		{"fusaka_invalid", MaxRLPBlockSizeFusaka + 1, true, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRLPBlockSize(tc.size, tc.isFusaka)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateRLPBlockSize(%d, %v) error = %v, wantErr %v",
					tc.size, tc.isFusaka, err, tc.wantErr)
			}
		})
	}
}

// EIP-7951: P-256 Precompile

func TestP256VerifyAddress(t *testing.T) {
	expectedAddr := "0x0000000000000000000000000000000000000100"
	if P256VerifyAddress.Hex() != expectedAddr {
		t.Errorf("P256VerifyAddress = %s, want %s", P256VerifyAddress.Hex(), expectedAddr)
	}
}

func TestP256VerifyGasFusaka(t *testing.T) {
	if P256VerifyGasFusaka != 3450 {
		t.Errorf("P256VerifyGasFusaka = %d, want 3450", P256VerifyGasFusaka)
	}
}

// Fusaka Instruction Set

func TestFusakaInstructionSetExists(t *testing.T) {
	jt := GetFusakaInstructionSet()

	basicOpcodes := []OpCode{ADD, MUL}
	for _, op := range basicOpcodes {
		if jt[op] == nil {
			t.Errorf("Fusaka instruction set missing %s opcode", op.String())
		}
	}

	// EOF opcodes (inherited from Osaka)
	eofOpcodes := []OpCode{RJUMP, CALLF}
	for _, op := range eofOpcodes {
		if jt[op] == nil {
			t.Errorf("Fusaka instruction set missing %s opcode", op.String())
		}
	}

	// Prague opcodes
	if jt[CLZ] == nil {
		t.Error("Fusaka instruction set missing CLZ opcode")
	}
}

// Fusaka Config

func TestDefaultFusakaConfig(t *testing.T) {
	cfg := DefaultFusakaConfig()

	if cfg.MaxCodeSize != MaxCodeSizeFusaka {
		t.Errorf("FusakaConfig.MaxCodeSize = %d, want %d", cfg.MaxCodeSize, MaxCodeSizeFusaka)
	}
	if cfg.TxGasLimit != TxGasLimitFusaka {
		t.Errorf("FusakaConfig.TxGasLimit = %d, want %d", cfg.TxGasLimit, TxGasLimitFusaka)
	}
	if cfg.ModExpMaxGas != ModExpMaxGas {
		t.Errorf("FusakaConfig.ModExpMaxGas = %d, want %d", cfg.ModExpMaxGas, ModExpMaxGas)
	}
}

// Benchmarks

func BenchmarkCalcCodeSizeGas(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalcCodeSizeGas(40 * 1024)
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

// EIP-7939: CLZ (Count Leading Zeros) Execution

func TestOpCLZExecution(t *testing.T) {
	allOnes := make([]byte, 32)
	for i := range allOnes {
		allOnes[i] = 0xff
	}

	tests := []struct {
		name     string
		input    []byte
		expected uint64
	}{
		{"zero_value", make([]byte, 32), 256},
		{"one", append(make([]byte, 31), 1), 255},
		{"two", append(make([]byte, 31), 2), 254},
		{"max_byte", append(make([]byte, 31), 0xff), 248},
		{"high_bit_set", append([]byte{0x80}, make([]byte, 31)...), 0},
		{"all_ones", allOnes, 0},
		{"mid_value", append(make([]byte, 16), append([]byte{0x01}, make([]byte, 15)...)...), 135},
		{"second_byte", append(make([]byte, 30), []byte{0x01, 0x00}...), 247},
		{"power_of_two", append(make([]byte, 28), []byte{0x10, 0x00, 0x00, 0x00}...), 227},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := []byte{byte(CLZ)}
			scope := &ScopeContext{
				Stack:    stack.New(),
				Memory:   NewMemory(),
				Contract: &Contract{Code: code},
			}

			scope.Stack.Push(new(uint256.Int).SetBytes(tt.input))

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
}

func TestOpCLZInstructionSet(t *testing.T) {
	jt := newFusakaInstructionSet()

	if jt[CLZ] == nil {
		t.Fatal("CLZ not in Fusaka instruction set")
	}
	if jt[CLZ].execute == nil {
		t.Error("CLZ execute function is nil")
	}
	if jt[CLZ].numPop != 1 {
		t.Errorf("CLZ numPop = %d, want 1", jt[CLZ].numPop)
	}
	if jt[CLZ].numPush != 1 {
		t.Errorf("CLZ numPush = %d, want 1", jt[CLZ].numPush)
	}
	if jt[CLZ].constantGas != GasFastStep {
		t.Errorf("CLZ gas = %d, want %d", jt[CLZ].constantGas, GasFastStep)
	}
}

func BenchmarkOpCLZ(b *testing.B) {
	code := []byte{byte(CLZ)}
	scope := &ScopeContext{
		Stack:    stack.New(),
		Memory:   NewMemory(),
		Contract: &Contract{Code: code},
	}

	value := new(uint256.Int).SetBytes(bytes.Repeat([]byte{0x00}, 32))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scope.Stack.Push(value.Clone())
		pc := uint64(0)
		opCLZ(&pc, nil, scope)
	}
}
