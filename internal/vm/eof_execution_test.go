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

// EOF execution-level tests: builds EOF containers and runs them through the
// interpreter to validate actual bytecode execution semantics.

package vm

import (
	"encoding/binary"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/params"
)

// ---------------------------------------------------------------------------
// Helper: build a raw EOF container from parts
// ---------------------------------------------------------------------------

// EOFTypeInfo holds the 4-byte type descriptor for one code section.
type EOFTypeInfo struct {
	Inputs         uint8
	Outputs        uint8
	MaxStackHeight uint16
}

// buildEOFContainer constructs a valid EOF1 binary from code sections, type
// descriptors and an optional data section.
func buildEOFContainer(codeSections [][]byte, types_ []EOFTypeInfo, dataSection []byte) []byte {
	numCode := len(codeSections)
	if len(types_) != numCode {
		panic("codeSections and types_ length mismatch")
	}

	// --- header ---
	var hdr []byte
	// magic + version
	hdr = append(hdr, 0xEF, 0x00, 0x01)
	// type section header: kind=0x01, size=numCode*4
	typeSize := uint16(numCode * 4)
	hdr = append(hdr, 0x01)
	hdr = append(hdr, byte(typeSize>>8), byte(typeSize&0xff))
	// code section header: kind=0x01, count, then each size
	hdr = append(hdr, 0x01)
	hdr = append(hdr, byte(numCode>>8), byte(numCode&0xff))
	for _, cs := range codeSections {
		sz := uint16(len(cs))
		hdr = append(hdr, byte(sz>>8), byte(sz&0xff))
	}
	// data section header
	dsz := uint16(len(dataSection))
	hdr = append(hdr, 0x03)
	hdr = append(hdr, byte(dsz>>8), byte(dsz&0xff))
	// terminator
	hdr = append(hdr, 0x00)

	// --- body ---
	// type section body (4 bytes per code section)
	for _, ti := range types_ {
		hdr = append(hdr, ti.Inputs, ti.Outputs)
		hdr = append(hdr, byte(ti.MaxStackHeight>>8), byte(ti.MaxStackHeight&0xff))
	}
	// code section bodies
	for _, cs := range codeSections {
		hdr = append(hdr, cs...)
	}
	// data section body
	hdr = append(hdr, dataSection...)
	return hdr
}

// ---------------------------------------------------------------------------
// Helper: create an EVMInterpreter with the Osaka jump table so that all EOF
// opcodes are available, without needing a full EVM / StateDB.
// ---------------------------------------------------------------------------

func newOsakaInterpreter() *EVMInterpreter {
	jt := withEOF(newOsakaInstructionSet())
	return &EVMInterpreter{
		VM: &VM{
			evm: &mockVMInterpreter{},
			cfg: Config{},
		},
		jt: &jt,
	}
}

// mockVMInterpreter satisfies the VMInterpreter interface with minimal stubs
// so that the interpreter loop can run for opcodes that do not touch state.
type mockVMInterpreter struct{}

func (m *mockVMInterpreter) Call(caller ContractRef, addr types.Address, input []byte, gas uint64, value *uint256.Int, bailout bool) ([]byte, uint64, error) {
	return nil, gas, nil
}
func (m *mockVMInterpreter) CallCode(caller ContractRef, addr types.Address, input []byte, gas uint64, value *uint256.Int) ([]byte, uint64, error) {
	return nil, gas, nil
}
func (m *mockVMInterpreter) DelegateCall(caller ContractRef, addr types.Address, input []byte, gas uint64) ([]byte, uint64, error) {
	return nil, gas, nil
}
func (m *mockVMInterpreter) StaticCall(caller ContractRef, addr types.Address, input []byte, gas uint64) ([]byte, uint64, error) {
	return nil, gas, nil
}
func (m *mockVMInterpreter) Create(caller ContractRef, code []byte, gas uint64, endowment *uint256.Int) ([]byte, types.Address, uint64, error) {
	return nil, types.Address{}, gas, nil
}
func (m *mockVMInterpreter) Create2(caller ContractRef, code []byte, gas uint64, endowment *uint256.Int, salt *uint256.Int) ([]byte, types.Address, uint64, error) {
	return nil, types.Address{}, gas, nil
}
func (m *mockVMInterpreter) EOFCreate2(caller ContractRef, code []byte, gas uint64, endowment *uint256.Int, salt *uint256.Int) ([]byte, types.Address, uint64, error) {
	return nil, types.Address{}, gas, nil
}
func (m *mockVMInterpreter) ChainRules() *params.Rules {
	// IsEOF: these tests exercise EOF-container execution, which now only
	// activates through the explicit EOFTime gate (EOF left the Osaka scope).
	return &params.Rules{IsOsaka: true, IsEOF: true, IsPectra: true, IsCancun: true, IsShanghai: true, IsLondon: true}
}
func (m *mockVMInterpreter) ChainConfig() *params.ChainConfig {
	return &params.ChainConfig{ChainID: big.NewInt(1)}
}
func (m *mockVMInterpreter) IntraBlockState() evmtypes.IntraBlockState { return nil }
func (m *mockVMInterpreter) Context() evmtypes.BlockContext {
	return evmtypes.BlockContext{
		Difficulty: big.NewInt(1),
		BaseFee:    uint256.NewInt(1),
	}
}
func (m *mockVMInterpreter) TxContext() evmtypes.TxContext {
	return evmtypes.TxContext{GasPrice: uint256.NewInt(1)}
}
func (m *mockVMInterpreter) Config() Config                                     { return Config{} }
func (m *mockVMInterpreter) SetCallGasTemp(uint64)                              {}
func (m *mockVMInterpreter) CallGasTemp() uint64                                { return 0 }
func (m *mockVMInterpreter) Cancelled() bool                                    { return false }
func (m *mockVMInterpreter) Reset(evmtypes.TxContext, evmtypes.IntraBlockState) {}

// ---------------------------------------------------------------------------
// Helper: run EOF bytecode through the interpreter
// ---------------------------------------------------------------------------

// runEOF builds an EOF contract, sets it up on an interpreter, and runs it.
// Returns the stack after execution and any error.
func runEOF(t *testing.T, codeSections [][]byte, types_ []EOFTypeInfo, dataSection []byte, gas uint64) (*stack.Stack, error) {
	t.Helper()
	raw := buildEOFContainer(codeSections, types_, dataSection)

	contract := &Contract{
		Code:  raw,
		Gas:   gas,
		value: uint256.NewInt(0),
		self:  AccountRef(types.Address{}),
	}

	// SetCallCode triggers EOF parsing
	addr := types.Address{}
	contract.SetCallCode(&addr, types.Hash{}, raw)
	if contract.EOFContainer == nil {
		t.Fatal("EOF container was not parsed from raw bytecode")
	}

	interp := newOsakaInterpreter()
	_, err := interp.Run(contract, nil, false)

	// Reconstruct stack from contract state is not directly possible after Run
	// since Run returns only ([]byte, error). For verification we use
	// opcode-level tests below.
	return nil, err
}

// ---------------------------------------------------------------------------
// Test: RJUMP — forward and backward relative jumps
// ---------------------------------------------------------------------------

func TestEOF_RJUMP(t *testing.T) {
	t.Run("forward_jump", func(t *testing.T) {
		// Code: RJUMP +2, INVALID, INVALID, STOP
		// Expected: jump over the two INVALID bytes to STOP
		code := []byte{
			byte(RJUMP), 0x00, 0x02, // RJUMP +2 (skip 2 bytes)
			byte(INVALID), // skipped
			byte(INVALID), // skipped
			byte(STOP),    // land here
		}
		scope := createTestScopeWithEOF(code, nil, nil)
		pc := uint64(0)
		_, err := opRJUMP(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMP forward failed: %v", err)
		}
		// After RJUMP, pc should point to STOP (index 5). The handler sets
		// pc = pc + 3 + offset - 1 = 0 + 3 + 2 - 1 = 4. The interpreter loop
		// then increments pc to 5.
		if pc != 4 {
			t.Errorf("pc = %d, want 4 (interpreter will increment to 5)", pc)
		}
	})

	t.Run("backward_jump", func(t *testing.T) {
		// Code layout (10 bytes total):
		//   [0] PUSH1 0x01
		//   [2] POP
		//   [3] RJUMP -4   (target = 3+3+(-4)-1 = 1, interpreter increments to 2... wait)
		//   Let's simplify: build code where RJUMP jumps back to itself (infinite
		//   loop in real execution). We just verify the PC arithmetic.
		// Code: STOP, RJUMP -4
		// At pc=1: newPC = 1 + 3 + (-4) - 1 = -1? That's out of bounds.
		// Better: RJUMP at offset 3, jumps back to offset 0.
		// pc=3: newPC = 3+3+(-6)-1 = -1. Still bad.
		// Correct approach: RJUMP backwards to the start of the code.
		// [0] NOP [1] NOP [2] NOP [3] RJUMP -3
		//   newPC = 3 + 3 + (-3) - 1 = 2. Interpreter increments to 3 → loop.
		// Actually, RJUMP offset -6 from pc=3: newPC = 3+3+(-6)-1 = -1 → invalid.
		// Let's do: pc=3, offset = -3: newPC = 3+3+(-3)-1 = 2 → points to code[2].
		code := make([]byte, 10)
		code[0] = byte(STOP) // 0
		code[1] = byte(STOP) // 1
		code[2] = byte(STOP) // 2
		code[3] = byte(RJUMP)
		// Encode int16(-3) as big-endian uint16
		code[4] = 0xFF
		code[5] = 0xFD // -3 in two's complement
		// remaining bytes are NOPs/STOP

		scope := createTestScopeWithEOF(code, nil, nil)
		pc := uint64(3)
		_, err := opRJUMP(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMP backward failed: %v", err)
		}
		// newPC = 3 + 3 + (-3) - 1 = 2
		if pc != 2 {
			t.Errorf("pc = %d, want 2", pc)
		}
	})

	t.Run("zero_offset", func(t *testing.T) {
		// RJUMP +0 at pc=0 means newPC = 0+3+0-1 = 2, interpreter increments
		// to 3, which is the byte right after the instruction.
		code := make([]byte, 10)
		code[0] = byte(RJUMP)
		code[1] = 0x00
		code[2] = 0x00

		scope := createTestScopeWithEOF(code, nil, nil)
		pc := uint64(0)
		_, err := opRJUMP(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMP zero offset failed: %v", err)
		}
		if pc != 2 {
			t.Errorf("pc = %d, want 2", pc)
		}
	})

	t.Run("out_of_bounds_positive", func(t *testing.T) {
		code := []byte{byte(RJUMP), 0x7F, 0xFF} // offset = +32767
		scope := createTestScopeWithEOF(code, nil, nil)
		pc := uint64(0)
		_, err := opRJUMP(&pc, nil, scope)
		if err == nil {
			t.Error("RJUMP with out-of-bounds offset should fail")
		}
	})

	t.Run("out_of_bounds_negative", func(t *testing.T) {
		code := make([]byte, 10)
		code[0] = byte(RJUMP)
		// Encode int16(-10) as big-endian
		code[1] = 0xFF
		code[2] = 0xF6 // -10 in two's complement
		scope := createTestScopeWithEOF(code, nil, nil)
		pc := uint64(0)
		_, err := opRJUMP(&pc, nil, scope)
		if err == nil {
			t.Error("RJUMP with negative out-of-bounds offset should fail")
		}
	})
}

// ---------------------------------------------------------------------------
// Test: RJUMPI — conditional relative jump (taken and not-taken)
// ---------------------------------------------------------------------------

func TestEOF_RJUMPI(t *testing.T) {
	t.Run("taken_nonzero", func(t *testing.T) {
		// RJUMPI +4, condition=1 → should jump
		code := make([]byte, 20)
		code[0] = byte(RJUMPI)
		binary.BigEndian.PutUint16(code[1:], uint16(int16(4)))

		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(1)) // nonzero → taken

		pc := uint64(0)
		_, err := opRJUMPI(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMPI (taken) failed: %v", err)
		}
		// newPC = 0+3+4-1 = 6
		if pc != 6 {
			t.Errorf("pc = %d, want 6", pc)
		}
	})

	t.Run("not_taken_zero", func(t *testing.T) {
		// RJUMPI +4, condition=0 → should NOT jump, just skip offset
		code := make([]byte, 20)
		code[0] = byte(RJUMPI)
		binary.BigEndian.PutUint16(code[1:], uint16(int16(4)))

		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(0)) // zero → not taken

		pc := uint64(0)
		_, err := opRJUMPI(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMPI (not taken) failed: %v", err)
		}
		// Not taken: pc += 2 → pc = 2. Interpreter will increment to 3.
		if pc != 2 {
			t.Errorf("pc = %d, want 2", pc)
		}
	})

	t.Run("taken_large_value", func(t *testing.T) {
		// Condition = 0xFFFFFFFF (large nonzero) → should be taken
		code := make([]byte, 20)
		code[0] = byte(RJUMPI)
		binary.BigEndian.PutUint16(code[1:], uint16(int16(2)))

		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(0xFFFFFFFF))

		pc := uint64(0)
		_, err := opRJUMPI(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMPI (large value) failed: %v", err)
		}
		// newPC = 0+3+2-1 = 4
		if pc != 4 {
			t.Errorf("pc = %d, want 4", pc)
		}
	})

	t.Run("backward_taken", func(t *testing.T) {
		// RJUMPI at pc=5, offset=-3, condition=1 → jump backward
		code := make([]byte, 20)
		code[5] = byte(RJUMPI)
		// Encode int16(-3) as big-endian
		code[6] = 0xFF
		code[7] = 0xFD

		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(1))

		pc := uint64(5)
		_, err := opRJUMPI(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMPI (backward taken) failed: %v", err)
		}
		// newPC = 5+3+(-3)-1 = 4
		if pc != 4 {
			t.Errorf("pc = %d, want 4", pc)
		}
	})
}

// ---------------------------------------------------------------------------
// Test: CALLF + RETF — call a second code section and return
// ---------------------------------------------------------------------------

func TestEOF_CALLF_RETF(t *testing.T) {
	t.Run("call_and_return", func(t *testing.T) {
		// Section 0: CALLF 1   (bytecode: E3 00 01)
		// Section 1: RETF      (bytecode: E4)
		// CALLF should push return info, switch to section 1.
		// RETF should pop return info, switch back to section 0.
		section0 := []byte{byte(CALLF), 0x00, 0x01}
		section1 := []byte{byte(RETF)}

		container := &EOFContainer{
			Header: &EOFHeader{
				Version:   EOFVersion1,
				CodeSizes: []uint16{3, 1},
				Types: []EOFTypeSection{
					{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
					{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
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

		// Execute CALLF at pc=0
		pc := uint64(0)
		_, err := opCALLF(&pc, nil, scope)
		if err != nil {
			t.Fatalf("CALLF failed: %v", err)
		}

		// Verify state after CALLF
		if scope.Contract.CodeSection != 1 {
			t.Errorf("after CALLF: CodeSection = %d, want 1", scope.Contract.CodeSection)
		}
		if len(scope.ReturnStack.Data()) != 1 {
			t.Fatalf("after CALLF: ReturnStack has %d entries, want 1", len(scope.ReturnStack.Data()))
		}
		// Return info should encode section=0, returnPC=3
		retInfo := scope.ReturnStack.Data()[0]
		wantSection := uint32(0)
		wantRetPC := uint32(3) // CALLF at pc=0 pushes returnPC = 0+3 = 3
		if retInfo>>16 != wantSection {
			t.Errorf("return info section = %d, want %d", retInfo>>16, wantSection)
		}
		if retInfo&0xFFFF != wantRetPC {
			t.Errorf("return info PC = %d, want %d", retInfo&0xFFFF, wantRetPC)
		}

		// pc was set to -1 (will be incremented to 0 by interpreter loop)
		// Simulate interpreter increment
		pc++
		if pc != 0 {
			// pc was set to uint64(-1) which is MaxUint64, pc++ wraps to 0
			// Actually opCALLF sets *pc = 0; *pc-- which is MaxUint64.
			// After increment it becomes 0.
		}

		// Execute RETF at pc=0 in section 1
		pc = 0
		_, err = opRETF(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RETF failed: %v", err)
		}

		// Verify state after RETF
		if scope.Contract.CodeSection != 0 {
			t.Errorf("after RETF: CodeSection = %d, want 0", scope.Contract.CodeSection)
		}
		if len(scope.ReturnStack.Data()) != 0 {
			t.Errorf("after RETF: ReturnStack has %d entries, want 0", len(scope.ReturnStack.Data()))
		}
		// RETF sets pc = returnPC - 1 = 3 - 1 = 2
		if pc != 2 {
			t.Errorf("after RETF: pc = %d, want 2", pc)
		}
	})

	t.Run("nested_callf", func(t *testing.T) {
		// Section 0: CALLF 1
		// Section 1: CALLF 2
		// Section 2: RETF
		// Then RETF from section 1
		section0 := []byte{byte(CALLF), 0x00, 0x01}
		section1 := []byte{byte(CALLF), 0x00, 0x02, byte(RETF)}
		section2 := []byte{byte(RETF)}

		container := &EOFContainer{
			Header: &EOFHeader{
				Version:   EOFVersion1,
				CodeSizes: []uint16{3, 4, 1},
				Types: []EOFTypeSection{
					{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
					{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
					{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
				},
			},
			Code: [][]byte{section0, section1, section2},
		}

		scope := &ScopeContext{
			Stack:       stack.New(),
			Memory:      NewMemory(),
			Contract:    &Contract{Code: section0, EOFContainer: container, CodeSection: 0},
			ReturnStack: stack.NewReturnStack(),
		}

		// CALLF 1 from section 0
		pc := uint64(0)
		_, err := opCALLF(&pc, nil, scope)
		if err != nil {
			t.Fatalf("CALLF 1 failed: %v", err)
		}
		if scope.Contract.CodeSection != 1 {
			t.Fatalf("after first CALLF: section = %d, want 1", scope.Contract.CodeSection)
		}

		// CALLF 2 from section 1, pc=0
		pc = 0
		_, err = opCALLF(&pc, nil, scope)
		if err != nil {
			t.Fatalf("CALLF 2 failed: %v", err)
		}
		if scope.Contract.CodeSection != 2 {
			t.Fatalf("after second CALLF: section = %d, want 2", scope.Contract.CodeSection)
		}
		if len(scope.ReturnStack.Data()) != 2 {
			t.Fatalf("return stack should have 2 entries, has %d", len(scope.ReturnStack.Data()))
		}

		// RETF from section 2 → back to section 1
		pc = 0
		_, err = opRETF(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RETF from section 2 failed: %v", err)
		}
		if scope.Contract.CodeSection != 1 {
			t.Errorf("after first RETF: section = %d, want 1", scope.Contract.CodeSection)
		}

		// RETF from section 1 → back to section 0
		// The return PC from section 1 call was pc=3 (CALLF 2 at offset 0, returnPC=3).
		// RETF sets pc = 3-1 = 2. Interpreter increments to 3 which is RETF in section1.
		pc = 3 // simulate interpreter arriving at the RETF in section 1
		_, err = opRETF(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RETF from section 1 failed: %v", err)
		}
		if scope.Contract.CodeSection != 0 {
			t.Errorf("after second RETF: section = %d, want 0", scope.Contract.CodeSection)
		}
		if len(scope.ReturnStack.Data()) != 0 {
			t.Errorf("return stack should be empty, has %d entries", len(scope.ReturnStack.Data()))
		}
	})

	t.Run("callf_invalid_section", func(t *testing.T) {
		// CALLF to non-existent section index 5
		section0 := []byte{byte(CALLF), 0x00, 0x05}
		container := &EOFContainer{
			Header: &EOFHeader{
				Version:   EOFVersion1,
				CodeSizes: []uint16{3},
				Types:     []EOFTypeSection{{Inputs: 0, Outputs: 0, MaxStackHeight: 0}},
			},
			Code: [][]byte{section0},
		}

		scope := &ScopeContext{
			Stack:       stack.New(),
			Memory:      NewMemory(),
			Contract:    &Contract{Code: section0, EOFContainer: container, CodeSection: 0},
			ReturnStack: stack.NewReturnStack(),
		}

		pc := uint64(0)
		_, err := opCALLF(&pc, nil, scope)
		if err == nil {
			t.Error("CALLF to invalid section should return error")
		}
	})

	t.Run("retf_empty_stack", func(t *testing.T) {
		scope := &ScopeContext{
			Stack:       stack.New(),
			Memory:      NewMemory(),
			Contract:    &Contract{Code: []byte{byte(RETF)}},
			ReturnStack: stack.NewReturnStack(),
		}

		pc := uint64(0)
		_, err := opRETF(&pc, nil, scope)
		if err == nil {
			t.Error("RETF with empty return stack should return error")
		}
	})
}

// ---------------------------------------------------------------------------
// Test: JUMPF — tail call to another code section
// ---------------------------------------------------------------------------

func TestEOF_JUMPF(t *testing.T) {
	t.Run("basic_jumpf", func(t *testing.T) {
		// Section 0: JUMPF 1 → switches to section 1 without pushing return stack
		// Section 1: STOP
		section0 := []byte{byte(JUMPF), 0x00, 0x01}
		section1 := []byte{byte(STOP)}

		container := &EOFContainer{
			Header: &EOFHeader{
				Version:   EOFVersion1,
				CodeSizes: []uint16{3, 1},
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

		if scope.Contract.CodeSection != 1 {
			t.Errorf("CodeSection = %d, want 1", scope.Contract.CodeSection)
		}
		// JUMPF is a tail call — return stack must remain empty
		if len(scope.ReturnStack.Data()) != 0 {
			t.Errorf("ReturnStack should be empty, has %d items", len(scope.ReturnStack.Data()))
		}
	})

	t.Run("jumpf_chain", func(t *testing.T) {
		// Section 0: JUMPF 1
		// Section 1: JUMPF 2
		// Section 2: STOP
		section0 := []byte{byte(JUMPF), 0x00, 0x01}
		section1 := []byte{byte(JUMPF), 0x00, 0x02}
		section2 := []byte{byte(STOP)}

		container := &EOFContainer{
			Header: &EOFHeader{
				Version:   EOFVersion1,
				CodeSizes: []uint16{3, 3, 1},
				Types: []EOFTypeSection{
					{Inputs: 0, Outputs: NonReturningFunction, MaxStackHeight: 0},
					{Inputs: 0, Outputs: NonReturningFunction, MaxStackHeight: 0},
					{Inputs: 0, Outputs: NonReturningFunction, MaxStackHeight: 0},
				},
			},
			Code: [][]byte{section0, section1, section2},
		}

		scope := &ScopeContext{
			Stack:       stack.New(),
			Memory:      NewMemory(),
			Contract:    &Contract{Code: section0, EOFContainer: container, CodeSection: 0},
			ReturnStack: stack.NewReturnStack(),
		}

		// First JUMPF: 0 → 1
		pc := uint64(0)
		_, err := opJUMPF(&pc, nil, scope)
		if err != nil {
			t.Fatalf("JUMPF 0→1 failed: %v", err)
		}
		if scope.Contract.CodeSection != 1 {
			t.Fatalf("section = %d, want 1", scope.Contract.CodeSection)
		}

		// Second JUMPF: 1 → 2
		pc = 0 // simulate interpreter resetting pc after increment
		// Actually opJUMPF sets pc to MaxUint64, increment wraps to 0
		_, err = opJUMPF(&pc, nil, scope)
		if err != nil {
			t.Fatalf("JUMPF 1→2 failed: %v", err)
		}
		if scope.Contract.CodeSection != 2 {
			t.Fatalf("section = %d, want 2", scope.Contract.CodeSection)
		}

		// Return stack should still be empty (no return addresses pushed)
		if len(scope.ReturnStack.Data()) != 0 {
			t.Errorf("ReturnStack should be empty after chain of JUMPFs, has %d items", len(scope.ReturnStack.Data()))
		}
	})

	t.Run("jumpf_invalid_section", func(t *testing.T) {
		section0 := []byte{byte(JUMPF), 0x00, 0x0A} // section 10 doesn't exist
		container := &EOFContainer{
			Header: &EOFHeader{
				Version:   EOFVersion1,
				CodeSizes: []uint16{3},
				Types:     []EOFTypeSection{{Inputs: 0, Outputs: NonReturningFunction, MaxStackHeight: 0}},
			},
			Code: [][]byte{section0},
		}

		scope := &ScopeContext{
			Stack:       stack.New(),
			Memory:      NewMemory(),
			Contract:    &Contract{Code: section0, EOFContainer: container, CodeSection: 0},
			ReturnStack: stack.NewReturnStack(),
		}

		pc := uint64(0)
		_, err := opJUMPF(&pc, nil, scope)
		if err == nil {
			t.Error("JUMPF to invalid section should return error")
		}
	})
}

// ---------------------------------------------------------------------------
// Test: DATALOAD — load 32 bytes from data section
// ---------------------------------------------------------------------------

func TestEOF_DATALOAD(t *testing.T) {
	t.Run("load_at_offset_0", func(t *testing.T) {
		data := make([]byte, 64)
		// Fill with recognizable pattern
		for i := range data {
			data[i] = byte(i)
		}

		scope := createTestScopeWithEOF([]byte{byte(DATALOAD)}, data, nil)
		scope.Stack.Push(uint256.NewInt(0))

		pc := uint64(0)
		_, err := opDATALOAD(&pc, nil, scope)
		if err != nil {
			t.Fatalf("DATALOAD failed: %v", err)
		}

		result := scope.Stack.Peek()
		// First 32 bytes: 0x00010203...1f
		resultBytes := result.Bytes32()
		for i := 0; i < 32; i++ {
			if resultBytes[i] != byte(i) {
				t.Errorf("byte %d = 0x%02x, want 0x%02x", i, resultBytes[i], byte(i))
				break
			}
		}
	})

	t.Run("load_at_offset_32", func(t *testing.T) {
		data := make([]byte, 64)
		for i := range data {
			data[i] = byte(i)
		}

		scope := createTestScopeWithEOF([]byte{byte(DATALOAD)}, data, nil)
		scope.Stack.Push(uint256.NewInt(32))

		pc := uint64(0)
		_, err := opDATALOAD(&pc, nil, scope)
		if err != nil {
			t.Fatalf("DATALOAD failed: %v", err)
		}

		result := scope.Stack.Peek()
		resultBytes := result.Bytes32()
		for i := 0; i < 32; i++ {
			if resultBytes[i] != byte(i+32) {
				t.Errorf("byte %d = 0x%02x, want 0x%02x", i, resultBytes[i], byte(i+32))
				break
			}
		}
	})

	t.Run("out_of_bounds_returns_zero", func(t *testing.T) {
		data := make([]byte, 32) // only 32 bytes
		scope := createTestScopeWithEOF([]byte{byte(DATALOAD)}, data, nil)
		scope.Stack.Push(uint256.NewInt(10)) // offset 10: only 22 bytes left, need 32

		pc := uint64(0)
		_, err := opDATALOAD(&pc, nil, scope)
		if err != nil {
			t.Fatalf("DATALOAD failed: %v", err)
		}

		result := scope.Stack.Peek()
		if !result.IsZero() {
			t.Errorf("out-of-bounds DATALOAD should return 0, got %v", result)
		}
	})

	t.Run("no_container_is_invalid_opcode", func(t *testing.T) {
		scope := &ScopeContext{
			Stack:    stack.New(),
			Memory:   NewMemory(),
			Contract: &Contract{Code: []byte{byte(DATALOAD)}},
		}
		scope.Stack.Push(uint256.NewInt(0))

		pc := uint64(0)
		_, err := opDATALOAD(&pc, nil, scope)
		if err == nil {
			t.Fatal("DATALOAD without container should return invalid opcode")
		}
		invalid, ok := err.(*ErrInvalidOpCode)
		if !ok {
			t.Fatalf("expected ErrInvalidOpCode, got %T: %v", err, err)
		}
		if invalid.opcode != DATALOAD {
			t.Fatalf("invalid opcode = %s, want %s", invalid.opcode, DATALOAD)
		}
	})
}

// ---------------------------------------------------------------------------
// Test: DATASIZE — get data section size
// ---------------------------------------------------------------------------

func TestEOF_DATASIZE(t *testing.T) {
	tests := []struct {
		name     string
		dataLen  int
		expected uint64
	}{
		{"empty", 0, 0},
		{"32_bytes", 32, 32},
		{"100_bytes", 100, 100},
		{"max_uint16", 65535, 65535},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := make([]byte, tt.dataLen)
			scope := createTestScopeWithEOF([]byte{byte(DATASIZE)}, data, nil)

			pc := uint64(0)
			_, err := opDATASIZE(&pc, nil, scope)
			if err != nil {
				t.Fatalf("DATASIZE failed: %v", err)
			}

			result := scope.Stack.Pop()
			if result.Uint64() != tt.expected {
				t.Errorf("DATASIZE = %d, want %d", result.Uint64(), tt.expected)
			}
		})
	}

	t.Run("no_container_is_invalid_opcode", func(t *testing.T) {
		scope := &ScopeContext{
			Stack:    stack.New(),
			Memory:   NewMemory(),
			Contract: &Contract{Code: []byte{byte(DATASIZE)}},
		}

		pc := uint64(0)
		_, err := opDATASIZE(&pc, nil, scope)
		if err == nil {
			t.Fatal("DATASIZE without container should return invalid opcode")
		}
		invalid, ok := err.(*ErrInvalidOpCode)
		if !ok {
			t.Fatalf("expected ErrInvalidOpCode, got %T: %v", err, err)
		}
		if invalid.opcode != DATASIZE {
			t.Fatalf("invalid opcode = %s, want %s", invalid.opcode, DATASIZE)
		}
	})
}

// ---------------------------------------------------------------------------
// Test: DUPN and SWAPN — extended dup/swap with immediate operands
// ---------------------------------------------------------------------------

func TestEOF_DUPN_SWAPN(t *testing.T) {
	t.Run("dupn_0_duplicates_top", func(t *testing.T) {
		// DUPN 0x00 means duplicate stack[0] (top), equivalent to DUP1
		code := []byte{byte(DUPN), 0x00}
		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(42))

		pc := uint64(0)
		_, err := opDUPN(&pc, nil, scope)
		if err != nil {
			t.Fatalf("DUPN failed: %v", err)
		}

		top := scope.Stack.Pop()
		second := scope.Stack.Pop()
		if top.Uint64() != 42 || second.Uint64() != 42 {
			t.Errorf("DUPN(0): top=%d, second=%d, both should be 42", top.Uint64(), second.Uint64())
		}
		if pc != 1 {
			t.Errorf("pc = %d, want 1", pc)
		}
	})

	t.Run("dupn_deep", func(t *testing.T) {
		// DUPN 0x04 means duplicate stack[4] (5th from top)
		code := []byte{byte(DUPN), 0x04}
		scope := createTestScopeWithEOF(code, nil, nil)
		// Push 5 values: bottom=100, ..., top=500
		for i := 1; i <= 5; i++ {
			scope.Stack.Push(uint256.NewInt(uint64(i * 100)))
		}

		pc := uint64(0)
		_, err := opDUPN(&pc, nil, scope)
		if err != nil {
			t.Fatalf("DUPN deep failed: %v", err)
		}

		result := scope.Stack.Pop()
		// n = 0x04 + 1 = 5, Back(4) returns 5th from top = value 100
		if result.Uint64() != 100 {
			t.Errorf("DUPN(4) = %d, want 100", result.Uint64())
		}
	})

	t.Run("dupn_underflow", func(t *testing.T) {
		// DUPN 0x10 = dup 17th element, but only 3 on stack
		code := []byte{byte(DUPN), 0x10}
		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(1))
		scope.Stack.Push(uint256.NewInt(2))
		scope.Stack.Push(uint256.NewInt(3))

		pc := uint64(0)
		_, err := opDUPN(&pc, nil, scope)
		if err == nil {
			t.Error("DUPN with insufficient stack should return error")
		}
	})

	t.Run("swapn_1_swaps_top_two", func(t *testing.T) {
		// SWAPN 0x01 means n=2, swap top with 2nd from top (like SWAP1 in legacy)
		// Note: SWAPN 0x00 → n=1 → Swap(1) is identity (swaps top with itself)
		code := []byte{byte(SWAPN), 0x01}
		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(10)) // bottom
		scope.Stack.Push(uint256.NewInt(20)) // top

		pc := uint64(0)
		_, err := opSWAPN(&pc, nil, scope)
		if err != nil {
			t.Fatalf("SWAPN failed: %v", err)
		}

		top := scope.Stack.Pop()
		bottom := scope.Stack.Pop()
		if top.Uint64() != 10 || bottom.Uint64() != 20 {
			t.Errorf("SWAPN(1): top=%d, bottom=%d; want top=10, bottom=20", top.Uint64(), bottom.Uint64())
		}
		if pc != 1 {
			t.Errorf("pc = %d, want 1", pc)
		}
	})

	t.Run("swapn_deep", func(t *testing.T) {
		// SWAPN 0x03 means n=4, Swap(4) swaps Data[Len()-4] with Data[Len()-1]
		// Stack data array: [100, 200, 300, 400, 500] (500 on top = Data[4])
		// Swap(4): swap Data[1]=200 with Data[4]=500
		// Result: [100, 500, 300, 400, 200]
		code := []byte{byte(SWAPN), 0x03}
		scope := createTestScopeWithEOF(code, nil, nil)
		for i := 1; i <= 5; i++ {
			scope.Stack.Push(uint256.NewInt(uint64(i * 100)))
		}

		pc := uint64(0)
		_, err := opSWAPN(&pc, nil, scope)
		if err != nil {
			t.Fatalf("SWAPN deep failed: %v", err)
		}

		top := scope.Stack.Pop()
		if top.Uint64() != 200 {
			t.Errorf("after SWAPN(3), top = %d, want 200", top.Uint64())
		}
	})

	t.Run("swapn_underflow", func(t *testing.T) {
		code := []byte{byte(SWAPN), 0x10} // n=17
		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(1))
		scope.Stack.Push(uint256.NewInt(2))

		pc := uint64(0)
		_, err := opSWAPN(&pc, nil, scope)
		if err == nil {
			t.Error("SWAPN with insufficient stack should return error")
		}
	})
}

// ---------------------------------------------------------------------------
// Test: EXCHANGE — exchange two non-top stack items
// ---------------------------------------------------------------------------

func TestEOF_EXCHANGE(t *testing.T) {
	t.Run("exchange_1_1", func(t *testing.T) {
		// imm = 0x00: n=1, m=1 → exchange stack[0] and stack[1]
		code := []byte{byte(EXCHANGE), 0x00}
		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(10))
		scope.Stack.Push(uint256.NewInt(20))

		pc := uint64(0)
		_, err := opEXCHANGE(&pc, nil, scope)
		if err != nil {
			t.Fatalf("EXCHANGE failed: %v", err)
		}

		a := scope.Stack.Pop()
		b := scope.Stack.Pop()
		if a.Uint64() != 10 || b.Uint64() != 20 {
			t.Errorf("EXCHANGE(0x00): got [%d, %d], want [10, 20]", a.Uint64(), b.Uint64())
		}
		if pc != 1 {
			t.Errorf("pc = %d, want 1", pc)
		}
	})

	t.Run("exchange_2_3", func(t *testing.T) {
		// imm = 0x12: n=2, m=3 → exchange stack[1] and stack[4]
		// n = (imm>>4)+1 = 2, m = (imm&0x0f)+1 = 3
		// a = Back(n-1) = Back(1), b = Back(n+m-1) = Back(4)
		code := []byte{byte(EXCHANGE), 0x12}
		scope := createTestScopeWithEOF(code, nil, nil)
		// Push [10, 20, 30, 40, 50] (50 on top)
		for i := 1; i <= 5; i++ {
			scope.Stack.Push(uint256.NewInt(uint64(i * 10)))
		}

		pc := uint64(0)
		_, err := opEXCHANGE(&pc, nil, scope)
		if err != nil {
			t.Fatalf("EXCHANGE failed: %v", err)
		}

		// Back(1) = 2nd from top = 40, Back(4) = 5th from top = 10
		// After exchange: swap 40 and 10
		// Stack should be [40, 20, 30, 10, 50]
		v5 := scope.Stack.Pop() // 50 (top, unchanged)
		v4 := scope.Stack.Pop() // 10 (was 40, swapped with 10)
		_ = scope.Stack.Pop()   // 30 (unchanged)
		_ = scope.Stack.Pop()   // 20 (unchanged)
		v1 := scope.Stack.Pop() // 40 (was 10, swapped with 40)

		if v5.Uint64() != 50 {
			t.Errorf("top = %d, want 50", v5.Uint64())
		}
		if v4.Uint64() != 10 {
			t.Errorf("2nd = %d, want 10 (was 40 before swap)", v4.Uint64())
		}
		if v1.Uint64() != 40 {
			t.Errorf("5th = %d, want 40 (was 10 before swap)", v1.Uint64())
		}
	})

	t.Run("exchange_underflow", func(t *testing.T) {
		// imm = 0xFF: n=16, m=16 → needs 32 items
		code := []byte{byte(EXCHANGE), 0xFF}
		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(1))
		scope.Stack.Push(uint256.NewInt(2))

		pc := uint64(0)
		_, err := opEXCHANGE(&pc, nil, scope)
		if err == nil {
			t.Error("EXCHANGE with insufficient stack should return error")
		}
	})
}

// ---------------------------------------------------------------------------
// Test: Full interpreter execution of EOF bytecode
// ---------------------------------------------------------------------------

func TestEOF_InterpreterRun_STOP(t *testing.T) {
	// Simplest EOF program: single STOP instruction
	code := []byte{byte(STOP)}
	types_ := []EOFTypeInfo{{Inputs: 0, Outputs: 0, MaxStackHeight: 0}}
	raw := buildEOFContainer([][]byte{code}, types_, nil)

	// Verify it parses
	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}
	if container.NumCodeSections() != 1 {
		t.Fatalf("expected 1 code section, got %d", container.NumCodeSections())
	}

	// Run through interpreter
	contract := NewContract(
		AccountRef(types.Address{}),
		AccountRef(types.Address{1}),
		uint256.NewInt(0),
		1_000_000,
		false,
	)
	contract.Code = raw
	contract.EOFContainer = container

	interp := newOsakaInterpreter()
	_, err = interp.Run(contract, nil, false)
	if err != nil {
		t.Fatalf("interpreter.Run STOP failed: %v", err)
	}
}

func TestEOF_InterpreterRun_RJUMP_Forward(t *testing.T) {
	// EOF program: RJUMP +1, INVALID, STOP
	// Should skip the INVALID and execute STOP
	code := []byte{
		byte(RJUMP), 0x00, 0x01, // RJUMP +1
		byte(INVALID), // skipped
		byte(STOP),    // executed
	}
	types_ := []EOFTypeInfo{{Inputs: 0, Outputs: 0, MaxStackHeight: 0}}
	raw := buildEOFContainer([][]byte{code}, types_, nil)

	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}

	contract := NewContract(
		AccountRef(types.Address{}),
		AccountRef(types.Address{1}),
		uint256.NewInt(0),
		1_000_000,
		false,
	)
	contract.Code = raw
	contract.EOFContainer = container

	interp := newOsakaInterpreter()
	_, err = interp.Run(contract, nil, false)
	if err != nil {
		t.Fatalf("interpreter.Run RJUMP forward failed: %v", err)
	}
}

func TestEOF_InterpreterRun_RJUMPI(t *testing.T) {
	// EOF program that uses PUSH1 + RJUMPI:
	// PUSH1 0x01, RJUMPI +1, INVALID, STOP
	// Condition is 1 (nonzero) so RJUMPI should be taken, skipping INVALID.
	code := []byte{
		byte(PUSH1), 0x01, // push 1
		byte(RJUMPI), 0x00, 0x01, // RJUMPI +1
		byte(INVALID), // skipped
		byte(STOP),    // executed
	}
	types_ := []EOFTypeInfo{{Inputs: 0, Outputs: 1, MaxStackHeight: 1}}
	raw := buildEOFContainer([][]byte{code}, types_, nil)

	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}

	contract := NewContract(
		AccountRef(types.Address{}),
		AccountRef(types.Address{1}),
		uint256.NewInt(0),
		1_000_000,
		false,
	)
	contract.Code = raw
	contract.EOFContainer = container

	interp := newOsakaInterpreter()
	_, err = interp.Run(contract, nil, false)
	if err != nil {
		t.Fatalf("interpreter.Run RJUMPI (taken) failed: %v", err)
	}
}

func TestEOF_InterpreterRun_RJUMPI_NotTaken(t *testing.T) {
	// PUSH1 0x00, RJUMPI +1, STOP, INVALID
	// Condition is 0 so RJUMPI not taken → falls through to STOP
	code := []byte{
		byte(PUSH1), 0x00, // push 0
		byte(RJUMPI), 0x00, 0x01, // RJUMPI +1 (not taken)
		byte(STOP),    // executed
		byte(INVALID), // never reached
	}
	types_ := []EOFTypeInfo{{Inputs: 0, Outputs: 1, MaxStackHeight: 1}}
	raw := buildEOFContainer([][]byte{code}, types_, nil)

	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}

	contract := NewContract(
		AccountRef(types.Address{}),
		AccountRef(types.Address{1}),
		uint256.NewInt(0),
		1_000_000,
		false,
	)
	contract.Code = raw
	contract.EOFContainer = container

	interp := newOsakaInterpreter()
	_, err = interp.Run(contract, nil, false)
	if err != nil {
		t.Fatalf("interpreter.Run RJUMPI (not taken) failed: %v", err)
	}
}

func TestEOF_InterpreterRun_DATALOAD_DATASIZE(t *testing.T) {
	// EOF program: DATASIZE, PUSH1 0, DATALOAD, POP, POP, STOP
	// This exercises data section access at the interpreter level.
	data := make([]byte, 64)
	for i := range data {
		data[i] = byte(i + 1) // non-zero data
	}

	code := []byte{
		byte(DATASIZE),    // push data size (64)
		byte(PUSH1), 0x00, // push 0 (offset)
		byte(DATALOAD), // load 32 bytes from offset 0
		byte(POP),      // discard DATALOAD result
		byte(POP),      // discard DATASIZE result
		byte(STOP),
	}
	types_ := []EOFTypeInfo{{Inputs: 0, Outputs: 2, MaxStackHeight: 2}}
	raw := buildEOFContainer([][]byte{code}, types_, data)

	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}

	contract := NewContract(
		AccountRef(types.Address{}),
		AccountRef(types.Address{1}),
		uint256.NewInt(0),
		1_000_000,
		false,
	)
	contract.Code = raw
	contract.EOFContainer = container

	interp := newOsakaInterpreter()
	_, err = interp.Run(contract, nil, false)
	if err != nil {
		t.Fatalf("interpreter.Run DATALOAD/DATASIZE failed: %v", err)
	}
}

func TestEOF_InterpreterRun_DUPN_SWAPN(t *testing.T) {
	// EOF program: PUSH1 10, PUSH1 20, DUPN 1, POP, SWAPN 0, POP, POP, STOP
	// DUPN 1 duplicates the 2nd item (10), then SWAPN 0 swaps top two.
	code := []byte{
		byte(PUSH1), 0x0A, // push 10
		byte(PUSH1), 0x14, // push 20
		byte(DUPN), 0x01, // dup 2nd from top (10)
		byte(POP),         // pop
		byte(SWAPN), 0x00, // swap top two
		byte(POP), // pop
		byte(POP), // pop
		byte(STOP),
	}
	types_ := []EOFTypeInfo{{Inputs: 0, Outputs: 2, MaxStackHeight: 3}}
	raw := buildEOFContainer([][]byte{code}, types_, nil)

	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}

	contract := NewContract(
		AccountRef(types.Address{}),
		AccountRef(types.Address{1}),
		uint256.NewInt(0),
		1_000_000,
		false,
	)
	contract.Code = raw
	contract.EOFContainer = container

	interp := newOsakaInterpreter()
	_, err = interp.Run(contract, nil, false)
	if err != nil {
		t.Fatalf("interpreter.Run DUPN/SWAPN failed: %v", err)
	}
}

func TestEOF_InterpreterRun_CALLF_RETF(t *testing.T) {
	// Section 0: CALLF 1, STOP
	// Section 1: PUSH1 42, POP, RETF
	section0 := []byte{
		byte(CALLF), 0x00, 0x01, // call section 1
		byte(STOP), // return after call
	}
	section1 := []byte{
		byte(PUSH1), 0x2A, // push 42
		byte(POP),  // pop it
		byte(RETF), // return
	}

	types_ := []EOFTypeInfo{
		{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
		{Inputs: 0, Outputs: 0, MaxStackHeight: 1},
	}
	raw := buildEOFContainer([][]byte{section0, section1}, types_, nil)

	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}

	contract := NewContract(
		AccountRef(types.Address{}),
		AccountRef(types.Address{1}),
		uint256.NewInt(0),
		1_000_000,
		false,
	)
	contract.Code = raw
	contract.EOFContainer = container

	interp := newOsakaInterpreter()
	_, err = interp.Run(contract, nil, false)
	if err != nil {
		t.Fatalf("interpreter.Run CALLF/RETF failed: %v", err)
	}
}

func TestEOF_InterpreterRun_JUMPF(t *testing.T) {
	// Section 0: JUMPF 1 (tail call)
	// Section 1: STOP
	// Note: first section cannot be NonReturningFunction per EOF validation,
	// so we use Outputs=0 which is valid.
	section0 := []byte{
		byte(JUMPF), 0x00, 0x01,
	}
	section1 := []byte{
		byte(STOP),
	}

	types_ := []EOFTypeInfo{
		{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
		{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
	}
	raw := buildEOFContainer([][]byte{section0, section1}, types_, nil)

	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}

	contract := NewContract(
		AccountRef(types.Address{}),
		AccountRef(types.Address{1}),
		uint256.NewInt(0),
		1_000_000,
		false,
	)
	contract.Code = raw
	contract.EOFContainer = container

	interp := newOsakaInterpreter()
	_, err = interp.Run(contract, nil, false)
	if err != nil {
		t.Fatalf("interpreter.Run JUMPF failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: EOF container building and parsing round-trip
// ---------------------------------------------------------------------------

func TestEOF_BuildAndParse(t *testing.T) {
	code := []byte{byte(PUSH1), 0x00, byte(STOP)}
	data := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	types_ := []EOFTypeInfo{{Inputs: 0, Outputs: 1, MaxStackHeight: 1}}

	raw := buildEOFContainer([][]byte{code}, types_, data)

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
	if len(container.GetData()) != 4 {
		t.Errorf("GetData length = %d, want 4", len(container.GetData()))
	}

	cs := container.GetCodeSection(0)
	if len(cs) != 3 {
		t.Errorf("code section length = %d, want 3", len(cs))
	}
	if cs[0] != byte(PUSH1) || cs[1] != 0x00 || cs[2] != byte(STOP) {
		t.Errorf("code section mismatch: got %x", cs)
	}

	ti := container.GetTypeInfo(0)
	if ti.Inputs != 0 || ti.Outputs != 1 || ti.MaxStackHeight != 1 {
		t.Errorf("type info mismatch: inputs=%d outputs=%d maxStack=%d",
			ti.Inputs, ti.Outputs, ti.MaxStackHeight)
	}
}

func TestEOF_BuildMultipleSections(t *testing.T) {
	section0 := []byte{byte(CALLF), 0x00, 0x01, byte(STOP)}
	section1 := []byte{byte(RETF)}
	data := []byte{0x01, 0x02}

	types_ := []EOFTypeInfo{
		{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
		{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
	}

	raw := buildEOFContainer([][]byte{section0, section1}, types_, data)

	container, err := ParseEOF(raw)
	if err != nil {
		t.Fatalf("ParseEOF failed: %v", err)
	}

	if container.NumCodeSections() != 2 {
		t.Errorf("NumCodeSections = %d, want 2", container.NumCodeSections())
	}

	cs0 := container.GetCodeSection(0)
	if len(cs0) != 4 {
		t.Errorf("section 0 length = %d, want 4", len(cs0))
	}

	cs1 := container.GetCodeSection(1)
	if len(cs1) != 1 {
		t.Errorf("section 1 length = %d, want 1", len(cs1))
	}
	if cs1[0] != byte(RETF) {
		t.Errorf("section 1 should be RETF, got 0x%02x", cs1[0])
	}
}

// ---------------------------------------------------------------------------
// Test: DATALOADN — load with immediate offset
// ---------------------------------------------------------------------------

func TestEOF_DATALOADN_Execution(t *testing.T) {
	t.Run("valid_offset", func(t *testing.T) {
		data := make([]byte, 64)
		for i := range data {
			data[i] = byte(i + 0xA0)
		}
		// DATALOADN with offset 0
		code := []byte{byte(DATALOADN), 0x00, 0x00}
		scope := createTestScopeWithEOF(code, data, nil)

		pc := uint64(0)
		_, err := opDATALOADN(&pc, nil, scope)
		if err != nil {
			t.Fatalf("DATALOADN failed: %v", err)
		}

		result := scope.Stack.Pop()
		resultBytes := result.Bytes32()
		if resultBytes[0] != 0xA0 {
			t.Errorf("first byte = 0x%02x, want 0xA0", resultBytes[0])
		}
		if pc != 2 {
			t.Errorf("pc = %d, want 2", pc)
		}
	})

	t.Run("offset_32", func(t *testing.T) {
		data := make([]byte, 64)
		for i := range data {
			data[i] = byte(i)
		}
		// DATALOADN with offset 32
		code := []byte{byte(DATALOADN), 0x00, 0x20}
		scope := createTestScopeWithEOF(code, data, nil)

		pc := uint64(0)
		_, err := opDATALOADN(&pc, nil, scope)
		if err != nil {
			t.Fatalf("DATALOADN failed: %v", err)
		}

		result := scope.Stack.Pop()
		resultBytes := result.Bytes32()
		if resultBytes[0] != 0x20 {
			t.Errorf("first byte = 0x%02x, want 0x20", resultBytes[0])
		}
	})
}

// ---------------------------------------------------------------------------
// Test: RJUMPV — jump table (switch)
// ---------------------------------------------------------------------------

func TestEOF_RJUMPV_Execution(t *testing.T) {
	t.Run("select_case_0", func(t *testing.T) {
		// RJUMPV with 2 cases, selecting case 0
		// Format: RJUMPV count offset0 offset1
		code := make([]byte, 20)
		code[0] = byte(RJUMPV)
		code[1] = 0x02                                         // 2 cases
		binary.BigEndian.PutUint16(code[2:], uint16(int16(2))) // case 0: offset +2
		binary.BigEndian.PutUint16(code[4:], uint16(int16(4))) // case 1: offset +4

		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(0)) // select case 0

		pc := uint64(0)
		_, err := opRJUMPV(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMPV failed: %v", err)
		}

		// newPC = 0 + 2 + 2*2 + 2 - 1 = 7
		if pc != 7 {
			t.Errorf("pc = %d, want 7", pc)
		}
	})

	t.Run("select_case_1", func(t *testing.T) {
		code := make([]byte, 20)
		code[0] = byte(RJUMPV)
		code[1] = 0x02                                         // 2 cases
		binary.BigEndian.PutUint16(code[2:], uint16(int16(2))) // case 0: offset +2
		binary.BigEndian.PutUint16(code[4:], uint16(int16(4))) // case 1: offset +4

		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(1)) // select case 1

		pc := uint64(0)
		_, err := opRJUMPV(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMPV failed: %v", err)
		}

		// newPC = 0 + 2 + 2*2 + 4 - 1 = 9
		if pc != 9 {
			t.Errorf("pc = %d, want 9", pc)
		}
	})

	t.Run("default_case_overflow", func(t *testing.T) {
		// Case index >= count → falls through (default)
		code := make([]byte, 20)
		code[0] = byte(RJUMPV)
		code[1] = 0x02 // 2 cases
		binary.BigEndian.PutUint16(code[2:], uint16(int16(2)))
		binary.BigEndian.PutUint16(code[4:], uint16(int16(4)))

		scope := createTestScopeWithEOF(code, nil, nil)
		scope.Stack.Push(uint256.NewInt(99)) // way out of range

		pc := uint64(0)
		_, err := opRJUMPV(&pc, nil, scope)
		if err != nil {
			t.Fatalf("RJUMPV default failed: %v", err)
		}

		// default: pc += 2 + count*2 - 1 = 0 + 2 + 4 - 1 = 5
		if pc != 5 {
			t.Errorf("pc = %d, want 5 (default case)", pc)
		}
	})
}

// ---------------------------------------------------------------------------
// Benchmark: interpreter-level EOF execution
// ---------------------------------------------------------------------------

func BenchmarkEOF_InterpreterRun_CALLF_RETF(b *testing.B) {
	section0 := []byte{byte(CALLF), 0x00, 0x01, byte(STOP)}
	section1 := []byte{byte(RETF)}
	types_ := []EOFTypeInfo{
		{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
		{Inputs: 0, Outputs: 0, MaxStackHeight: 0},
	}
	raw := buildEOFContainer([][]byte{section0, section1}, types_, nil)
	container, _ := ParseEOF(raw)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contract := NewContract(
			AccountRef(types.Address{}),
			AccountRef(types.Address{1}),
			uint256.NewInt(0),
			1_000_000,
			false,
		)
		contract.Code = raw
		contract.EOFContainer = container

		interp := newOsakaInterpreter()
		interp.Run(contract, nil, false)
	}
}

func BenchmarkEOF_InterpreterRun_RJUMP(b *testing.B) {
	// RJUMP +0 (nop jump), then STOP
	code := []byte{byte(RJUMP), 0x00, 0x00, byte(STOP)}
	types_ := []EOFTypeInfo{{Inputs: 0, Outputs: 0, MaxStackHeight: 0}}
	raw := buildEOFContainer([][]byte{code}, types_, nil)
	container, _ := ParseEOF(raw)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		contract := NewContract(
			AccountRef(types.Address{}),
			AccountRef(types.Address{1}),
			uint256.NewInt(0),
			1_000_000,
			false,
		)
		contract.Code = raw
		contract.EOFContainer = container

		interp := newOsakaInterpreter()
		interp.Run(contract, nil, false)
	}
}
