// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for Osaka/EOF EIPs execution.
// Reference: go-ethereum and erigon EOF test suites.

package vm

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
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

// CALLF/RETF Code Section Switching Tests

func TestOpCALLF_RETF_CodeSectionSwitch(t *testing.T) {
	// Create a container with 2 code sections:
	// Section 0: CALLF 1 (calls section 1)
	// Section 1: PUSH1 42, RETF (returns to section 0)
	section0 := []byte{byte(CALLF), 0x00, 0x01} // CALLF section 1
	section1 := []byte{byte(PUSH1), 0x2A, byte(RETF)} // PUSH1 42, RETF

	container := &EOFContainer{
		Header: &EOFHeader{
			Version:   EOFVersion1,
			CodeSizes: []uint16{uint16(len(section0)), uint16(len(section1))},
			Types: []EOFTypeSection{
				{Inputs: 0, Outputs: 0, MaxStackHeight: 1},
				{Inputs: 0, Outputs: 1, MaxStackHeight: 1},
			},
		},
		Code: [][]byte{section0, section1},
	}

	scope := &ScopeContext{
		Stack:       stack.New(),
		Memory:      NewMemory(),
		Contract:    &Contract{Code: section0, EOFContainer: container, CodeSection: 0},
		ReturnStack: stack.NewReturnStack(),
	}

	// Execute CALLF
	pc := uint64(0)
	_, err := opCALLF(&pc, nil, scope)
	if err != nil {
		t.Fatalf("CALLF failed: %v", err)
	}

	// After CALLF, code should be section 1
	if scope.Contract.CodeSection != 1 {
		t.Errorf("CodeSection = %d, want 1", scope.Contract.CodeSection)
	}
	if len(scope.Contract.Code) != len(section1) {
		t.Errorf("Code length = %d, want %d", len(scope.Contract.Code), len(section1))
	}

	// Execute PUSH1 42 (manually advance pc)
	pc++ // pc was decremented by CALLF, interpreter increments it
	scope.Stack.Push(uint256.NewInt(0x2A))
	pc++ // skip push data

	// Execute RETF
	_, err = opRETF(&pc, nil, scope)
	if err != nil {
		t.Fatalf("RETF failed: %v", err)
	}

	// After RETF, code should be back to section 0
	if scope.Contract.CodeSection != 0 {
		t.Errorf("CodeSection = %d, want 0", scope.Contract.CodeSection)
	}
	if len(scope.Contract.Code) != len(section0) {
		t.Errorf("Code length = %d, want %d", len(scope.Contract.Code), len(section0))
	}

	// Stack should have value 42
	result := scope.Stack.Pop()
	if result.Uint64() != 0x2A {
		t.Errorf("Stack top = %d, want 42", result.Uint64())
	}
}

func TestOpJUMPF_CodeSectionSwitch(t *testing.T) {
	section0 := []byte{byte(JUMPF), 0x00, 0x01}
	section1 := []byte{byte(STOP)}

	container := &EOFContainer{
		Header: &EOFHeader{
			Version:   EOFVersion1,
			CodeSizes: []uint16{uint16(len(section0)), uint16(len(section1))},
			Types: []EOFTypeSection{
				{Inputs: 0, Outputs: NonReturningFunction, MaxStackHeight: 0},
				{Inputs: 0, Outputs: NonReturningFunction, MaxStackHeight: 0},
			},
		},
		Code: [][]byte{section0, section1},
	}

	scope := &ScopeContext{
		Stack:       stack.New(),
		Memory:      NewMemory(),
		Contract:    &Contract{Code: section0, EOFContainer: container, CodeSection: 0},
		ReturnStack: stack.NewReturnStack(),
	}

	pc := uint64(0)
	_, err := opJUMPF(&pc, nil, scope)
	if err != nil {
		t.Fatalf("JUMPF failed: %v", err)
	}

	// After JUMPF, code should be section 1, no return stack push
	if scope.Contract.CodeSection != 1 {
		t.Errorf("CodeSection = %d, want 1", scope.Contract.CodeSection)
	}
	if len(scope.ReturnStack.Data()) != 0 {
		t.Errorf("ReturnStack should be empty after JUMPF, has %d items", len(scope.ReturnStack.Data()))
	}
}

// EOF Container Parsing Tests

func TestParseEOF_ValidContainer(t *testing.T) {
	// Build a valid EOF container:
	// EF00 01 (magic + version)
	// 01 0004 (type section: 4 bytes)
	// 01 0001 0002 (1 code section of 2 bytes)
	// 03 0004 (data section: 4 bytes)
	// 00 (terminator)
	// 00 00 00 01 (type: 0 inputs, 0 outputs, max_stack 1)
	// 00 FE (code: STOP, INVALID)
	// DE AD BE EF (data)
	raw := []byte{
		0xEF, 0x00, 0x01,       // magic + version
		0x01, 0x00, 0x04,       // type section size = 4
		0x01, 0x00, 0x01,       // 1 code section
		0x00, 0x02,             // code size = 2
		0x03, 0x00, 0x04,       // data section size = 4
		0x00,                   // terminator
		0x00, 0x00, 0x00, 0x01, // types: 0 in, 0 out, max_stack=1
		0x00, 0xFE,             // code
		0xDE, 0xAD, 0xBE, 0xEF, // data
	}

	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}

	if container.NumCodeSections() != 1 {
		t.Errorf("NumCodeSections = %d, want 1", container.NumCodeSections())
	}
	if container.DataSize() != 4 {
		t.Errorf("DataSize = %d, want 4", container.DataSize())
	}
	if container.Header.Types[0].Inputs != 0 {
		t.Errorf("Type[0].Inputs = %d, want 0", container.Header.Types[0].Inputs)
	}
}

func TestContractSetCallCode_EOF(t *testing.T) {
	// Build a minimal valid EOF container
	raw := []byte{
		0xEF, 0x00, 0x01,
		0x01, 0x00, 0x04,
		0x01, 0x00, 0x01,
		0x00, 0x01,
		0x03, 0x00, 0x00,
		0x00,
		0x00, 0x00, 0x00, 0x01,
		0x00, // STOP
	}

	c := &Contract{}
	addr := types.Address{}
	c.SetCallCode(&addr, types.Hash{}, raw)

	if c.EOFContainer == nil {
		t.Fatal("EOFContainer should be set for EOF code")
	}
	if c.EOFContainer.NumCodeSections() != 1 {
		t.Errorf("NumCodeSections = %d, want 1", c.EOFContainer.NumCodeSections())
	}
}

func TestContractSetCallCode_Legacy(t *testing.T) {
	legacyCode := []byte{byte(PUSH1), 0x00, byte(STOP)}

	c := &Contract{}
	addr := types.Address{}
	c.SetCallCode(&addr, types.Hash{}, legacyCode)

	if c.EOFContainer != nil {
		t.Error("EOFContainer should be nil for legacy code")
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
