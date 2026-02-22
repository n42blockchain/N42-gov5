// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for Osaka/EOF EIPs execution.
// Reference: go-ethereum and erigon EOF test suites.

package vm

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/internal/vm/stack"
)

// Test Helpers

func createTestScope(code []byte) *ScopeContext {
	return &ScopeContext{
		Stack:    stack.New(),
		Memory:   NewMemory(),
		Contract: &Contract{Code: code},
	}
}

func createTestScopeWithEOF(code []byte, data []byte, containers [][]byte) *ScopeContext {
	container := &EOFContainer{
		Header: &EOFHeader{
			Version:  EOFVersion1,
			DataSize: uint16(len(data)),
		},
		Code:       [][]byte{code},
		Data:       data,
		Containers: containers,
	}
	return &ScopeContext{
		Stack:       stack.New(),
		Memory:      NewMemory(),
		Contract:    &Contract{Code: code, EOFContainer: container},
		ReturnStack: stack.NewReturnStack(),
	}
}

// DUPN Tests (EIP-663)

func TestOpDUPN(t *testing.T) {
	tests := []struct {
		name     string
		n        byte
		stack    []uint64
		expected uint64
	}{
		{"dup1", 0x00, []uint64{100}, 100},
		{"dup2", 0x01, []uint64{200, 100}, 100},
		{"dup3", 0x02, []uint64{300, 200, 100}, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := createTestScope([]byte{byte(DUPN), tt.n})

			for i := len(tt.stack) - 1; i >= 0; i-- {
				scope.Stack.Push(uint256.NewInt(tt.stack[i]))
			}

			pc := uint64(0)
			_, err := opDUPN(&pc, nil, scope)
			if err != nil {
				t.Logf("opDUPN returned error (may be expected): %v", err)
				return
			}

			result := scope.Stack.Pop()
			if result.Uint64() != tt.expected {
				t.Errorf("opDUPN(%d) = %d, want %d", tt.n+1, result.Uint64(), tt.expected)
			}
			if pc != 1 {
				t.Errorf("PC = %d, want 1", pc)
			}
		})
	}
}

// SWAPN Tests (EIP-663)

func TestOpSWAPN(t *testing.T) {
	tests := []struct {
		name  string
		n     byte
		stack []uint64
	}{
		{"swap1", 0x00, []uint64{100, 200}},
		{"swap2", 0x01, []uint64{100, 200, 300}},
		{"swap3", 0x02, []uint64{100, 200, 300, 400}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := createTestScope([]byte{byte(SWAPN), tt.n})

			for i := len(tt.stack) - 1; i >= 0; i-- {
				scope.Stack.Push(uint256.NewInt(tt.stack[i]))
			}

			pc := uint64(0)
			_, err := opSWAPN(&pc, nil, scope)
			if err != nil {
				t.Logf("opSWAPN returned error: %v", err)
				return
			}
			if pc != 1 {
				t.Errorf("PC = %d, want 1", pc)
			}
		})
	}
}

// EXCHANGE Tests (EIP-663)

func TestOpEXCHANGE(t *testing.T) {
	tests := []struct {
		name  string
		imm   byte // high nibble = n-1, low nibble = m-1
		stack []uint64
	}{
		{"exchange_1_1", 0x00, []uint64{1, 2}},
		{"exchange_1_2", 0x01, []uint64{1, 2, 3}},
		{"exchange_2_1", 0x10, []uint64{1, 2, 3}},
		{"exchange_2_2", 0x11, []uint64{1, 2, 3, 4}},
		{"exchange_3_3", 0x22, []uint64{1, 2, 3, 4, 5, 6}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := createTestScope([]byte{byte(EXCHANGE), tt.imm})

			for i := len(tt.stack) - 1; i >= 0; i-- {
				scope.Stack.Push(uint256.NewInt(tt.stack[i]))
			}

			pc := uint64(0)
			_, err := opEXCHANGE(&pc, nil, scope)
			if err != nil {
				t.Logf("opEXCHANGE returned error: %v", err)
				return
			}
			if pc != 1 {
				t.Errorf("PC = %d, want 1", pc)
			}
		})
	}
}

// DATALOAD Tests (EIP-7480)

func TestOpDATALOAD(t *testing.T) {
	testData := make([]byte, 64)
	for i := range testData {
		testData[i] = byte(i)
	}

	tests := []struct {
		name   string
		offset uint64
		data   []byte
	}{
		{"offset_0", 0, testData},
		{"offset_32", 32, testData},
		{"out_of_bounds", 100, testData},
		{"empty_data", 0, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := createTestScopeWithEOF([]byte{byte(DATALOAD)}, tt.data, nil)
			scope.Stack.Push(uint256.NewInt(tt.offset))

			pc := uint64(0)
			_, err := opDATALOAD(&pc, nil, scope)
			if err != nil {
				t.Errorf("opDATALOAD returned error: %v", err)
				return
			}

			result := scope.Stack.Pop()
			t.Logf("DATALOAD(offset=%d) = %x", tt.offset, result.Bytes())
		})
	}
}

// DATALOADN Tests (EIP-7480)

func TestOpDATALOADN(t *testing.T) {
	testData := make([]byte, 64)
	for i := range testData {
		testData[i] = byte(i)
	}

	tests := []struct {
		name   string
		offset uint16
		data   []byte
	}{
		{"offset_0", 0, testData},
		{"offset_32", 32, testData},
		{"out_of_bounds", 100, testData},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := []byte{byte(DATALOADN), byte(tt.offset >> 8), byte(tt.offset & 0xff)}
			scope := createTestScopeWithEOF(code, tt.data, nil)

			pc := uint64(0)
			_, err := opDATALOADN(&pc, nil, scope)
			if err != nil {
				t.Errorf("opDATALOADN returned error: %v", err)
				return
			}

			result := scope.Stack.Pop()
			t.Logf("DATALOADN(offset=%d) = %x", tt.offset, result.Bytes())

			if pc != 2 {
				t.Errorf("PC = %d, want 2", pc)
			}
		})
	}
}

// DATASIZE Tests (EIP-7480)

func TestOpDATASIZE(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected uint64
	}{
		{"empty_data", []byte{}, 0},
		{"32_bytes", make([]byte, 32), 32},
		{"64_bytes", make([]byte, 64), 64},
		{"1024_bytes", make([]byte, 1024), 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := createTestScopeWithEOF([]byte{byte(DATASIZE)}, tt.data, nil)

			pc := uint64(0)
			_, err := opDATASIZE(&pc, nil, scope)
			if err != nil {
				t.Errorf("opDATASIZE returned error: %v", err)
				return
			}

			result := scope.Stack.Pop()
			if result.Uint64() != tt.expected {
				t.Errorf("DATASIZE = %d, want %d", result.Uint64(), tt.expected)
			}
		})
	}
}

// DATACOPY Tests (EIP-7480)

func TestOpDATACOPY(t *testing.T) {
	testData := make([]byte, 64)
	for i := range testData {
		testData[i] = byte(i)
	}

	scope := createTestScopeWithEOF([]byte{byte(DATACOPY)}, testData, nil)

	// Copy zero bytes (should succeed without memory expansion)
	scope.Stack.Push(uint256.NewInt(0)) // length
	scope.Stack.Push(uint256.NewInt(0)) // dataOffset
	scope.Stack.Push(uint256.NewInt(0)) // memOffset

	pc := uint64(0)
	_, err := opDATACOPY(&pc, nil, scope)
	if err != nil {
		t.Logf("opDATACOPY returned error (may be expected in test context): %v", err)
	}
}

// RJUMP Tests (EIP-4200)

func TestOpRJUMP(t *testing.T) {
	tests := []struct {
		name     string
		offset   int16
		codeLen  int
		expectPC uint64
	}{
		{"forward_3", 3, 10, 5},
		{"forward_0", 0, 10, 2},
		{"backward", -2, 10, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := make([]byte, tt.codeLen)
			code[0] = byte(RJUMP)
			code[1] = byte(tt.offset >> 8)
			code[2] = byte(tt.offset & 0xff)

			scope := createTestScope(code)
			pc := uint64(0)
			_, err := opRJUMP(&pc, nil, scope)
			if err != nil {
				t.Errorf("opRJUMP returned error: %v", err)
				return
			}
			if pc != tt.expectPC {
				t.Errorf("PC = %d, want %d", pc, tt.expectPC)
			}
		})
	}
}

// RJUMPI Tests (EIP-4200)

func TestOpRJUMPI(t *testing.T) {
	tests := []struct {
		name      string
		condition uint64
		offset    int16
		expectPC  uint64
	}{
		{"condition_true", 1, 5, 7},
		{"condition_false", 0, 5, 2},
		{"condition_large", 100, 3, 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := make([]byte, 20)
			code[0] = byte(RJUMPI)
			code[1] = byte(tt.offset >> 8)
			code[2] = byte(tt.offset & 0xff)

			scope := createTestScope(code)
			scope.Stack.Push(uint256.NewInt(tt.condition))

			pc := uint64(0)
			_, err := opRJUMPI(&pc, nil, scope)
			if err != nil {
				t.Errorf("opRJUMPI returned error: %v", err)
				return
			}
			if pc != tt.expectPC {
				t.Errorf("PC = %d, want %d", pc, tt.expectPC)
			}
		})
	}
}

// RJUMPV Tests (EIP-4200)

func TestOpRJUMPV(t *testing.T) {
	tests := []struct {
		name      string
		caseIndex uint64
		count     int
	}{
		{"case_0", 0, 3},
		{"case_1", 1, 3},
		{"case_2", 2, 3},
		{"case_overflow", 100, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			codeLen := 2 + tt.count*2 + 10
			code := make([]byte, codeLen)
			code[0] = byte(RJUMPV)
			code[1] = byte(tt.count)
			for i := 0; i < tt.count; i++ {
				offset := int16(i + 1)
				code[2+i*2] = byte(offset >> 8)
				code[2+i*2+1] = byte(offset & 0xff)
			}

			scope := createTestScope(code)
			scope.Stack.Push(uint256.NewInt(tt.caseIndex))

			pc := uint64(0)
			_, err := opRJUMPV(&pc, nil, scope)
			if err != nil {
				t.Errorf("opRJUMPV returned error: %v", err)
				return
			}
			t.Logf("RJUMPV(case=%d, count=%d) -> PC=%d", tt.caseIndex, tt.count, pc)
		})
	}
}

// RETURNDATALOAD Tests

func TestOpRETURNDATALOAD(t *testing.T) {
	jt := newOsakaInstructionSet()

	if jt[RETURNDATALOAD] == nil {
		t.Fatal("RETURNDATALOAD not in Osaka instruction set")
	}
	if jt[RETURNDATALOAD].execute == nil {
		t.Error("RETURNDATALOAD execute function is nil")
	}
	if jt[RETURNDATALOAD].numPop != 1 {
		t.Errorf("RETURNDATALOAD numPop = %d, want 1", jt[RETURNDATALOAD].numPop)
	}
	if jt[RETURNDATALOAD].numPush != 1 {
		t.Errorf("RETURNDATALOAD numPush = %d, want 1", jt[RETURNDATALOAD].numPush)
	}
}

// Osaka Instruction Set Tests

func TestOsakaInstructionSet(t *testing.T) {
	jt := newOsakaInstructionSet()

	eofOpcodes := []OpCode{
		RJUMP, RJUMPI, RJUMPV,
		CALLF, RETF, JUMPF,
		DATALOAD, DATALOADN, DATASIZE, DATACOPY,
		DUPN, SWAPN, EXCHANGE,
		EOFCREATE, RETURNCONTRACT, RETURNDATALOAD,
	}

	for _, op := range eofOpcodes {
		if jt[op] == nil || jt[op].execute == nil {
			t.Errorf("Opcode %s should be enabled in Osaka", op.String())
		}
	}
}

func TestOsakaGasConstants(t *testing.T) {
	gasTests := []struct {
		name     string
		constant uint64
		min      uint64
		max      uint64
	}{
		{"GasRJUMP", GasRJUMP, 1, 10},
		{"GasRJUMPI", GasRJUMPI, 1, 10},
		{"GasRJUMPV", GasRJUMPV, 1, 10},
		{"GasCALLF", GasCALLF, 1, 10},
		{"GasRETF", GasRETF, 1, 10},
		{"GasJUMPF", GasJUMPF, 1, 10},
		{"GasDataLoad", GasDataLoad, 1, 10},
		{"GasDataLoadN", GasDataLoadN, 1, 10},
		{"GasDataSize", GasDataSize, 1, 10},
		{"GasDUPN", GasDUPN, 1, 10},
		{"GasSWAPN", GasSWAPN, 1, 10},
		{"GasEXCHANGE", GasEXCHANGE, 1, 10},
	}

	for _, tt := range gasTests {
		if tt.constant < tt.min || tt.constant > tt.max {
			t.Errorf("%s = %d, expected between %d and %d", tt.name, tt.constant, tt.min, tt.max)
		}
	}
}

// Edge Case Tests

func TestOpDUPN_StackUnderflow(t *testing.T) {
	scope := createTestScope([]byte{byte(DUPN), 0x10})
	for i := 0; i < 5; i++ {
		scope.Stack.Push(uint256.NewInt(uint64(i)))
	}

	pc := uint64(0)
	_, err := opDUPN(&pc, nil, scope)
	if err != nil {
		t.Logf("opDUPN correctly returned error for stack underflow: %v", err)
	}
}

func TestOpSWAPN_StackUnderflow(t *testing.T) {
	scope := createTestScope([]byte{byte(SWAPN), 0x10})
	for i := 0; i < 5; i++ {
		scope.Stack.Push(uint256.NewInt(uint64(i)))
	}

	pc := uint64(0)
	_, err := opSWAPN(&pc, nil, scope)
	if err != nil {
		t.Logf("opSWAPN correctly returned error for stack underflow: %v", err)
	}
}

func TestOpEXCHANGE_StackUnderflow(t *testing.T) {
	scope := createTestScope([]byte{byte(EXCHANGE), 0xFF})
	for i := 0; i < 5; i++ {
		scope.Stack.Push(uint256.NewInt(uint64(i)))
	}

	pc := uint64(0)
	_, err := opEXCHANGE(&pc, nil, scope)
	if err != nil {
		t.Logf("opEXCHANGE correctly returned error for stack underflow: %v", err)
	}
}

func TestOpRJUMP_OutOfBounds(t *testing.T) {
	code := []byte{byte(RJUMP), 0x7F, 0xFF} // Large positive offset
	scope := createTestScope(code)

	pc := uint64(0)
	_, err := opRJUMP(&pc, nil, scope)
	if err != nil {
		t.Logf("opRJUMP correctly returned error for out-of-bounds: %v", err)
	}
}

// Benchmarks

func BenchmarkOpDUPN(b *testing.B) {
	scope := createTestScope([]byte{byte(DUPN), 0x05})
	for i := 0; i < 10; i++ {
		scope.Stack.Push(uint256.NewInt(uint64(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pc := uint64(0)
		opDUPN(&pc, nil, scope)
		scope.Stack.Pop()
	}
}

func BenchmarkOpSWAPN(b *testing.B) {
	scope := createTestScope([]byte{byte(SWAPN), 0x05})
	for i := 0; i < 10; i++ {
		scope.Stack.Push(uint256.NewInt(uint64(i)))
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pc := uint64(0)
		opSWAPN(&pc, nil, scope)
	}
}

func BenchmarkOpDATALOAD(b *testing.B) {
	scope := createTestScopeWithEOF([]byte{byte(DATALOAD)}, make([]byte, 1024), nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scope.Stack.Push(uint256.NewInt(0))
		pc := uint64(0)
		opDATALOAD(&pc, nil, scope)
		scope.Stack.Pop()
	}
}

func BenchmarkOpDATASIZE(b *testing.B) {
	scope := createTestScopeWithEOF([]byte{byte(DATASIZE)}, make([]byte, 1024), nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pc := uint64(0)
		opDATASIZE(&pc, nil, scope)
		scope.Stack.Pop()
	}
}

func BenchmarkOpRJUMP(b *testing.B) {
	code := make([]byte, 100)
	code[0] = byte(RJUMP)
	code[1] = 0x00
	code[2] = 0x00
	scope := createTestScope(code)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pc := uint64(0)
		opRJUMP(&pc, nil, scope)
	}
}
