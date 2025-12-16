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
	"testing"
)

// =============================================================================
// EOF Magic and Version Tests
// =============================================================================

func TestEOFMagic(t *testing.T) {
	if EOFMagic != 0xEF00 {
		t.Errorf("EOFMagic = 0x%04x, want 0xEF00", EOFMagic)
	}
}

func TestEOFVersion(t *testing.T) {
	if EOFVersion1 != 0x01 {
		t.Errorf("EOFVersion1 = 0x%02x, want 0x01", EOFVersion1)
	}
}

func TestEOFFormatByte(t *testing.T) {
	if EOFFormatByte != 0xEF {
		t.Errorf("EOFFormatByte = 0x%02x, want 0xEF", EOFFormatByte)
	}
}

// =============================================================================
// IsEOF Tests
// =============================================================================

func TestIsEOF(t *testing.T) {
	tests := []struct {
		name   string
		code   []byte
		expect bool
	}{
		{"empty", []byte{}, false},
		{"too short", []byte{0xEF}, false},
		{"valid prefix", []byte{0xEF, 0x00}, true},
		{"invalid prefix", []byte{0xEF, 0x01}, false},
		{"wrong first byte", []byte{0x00, 0x00}, false},
		{"valid with version", []byte{0xEF, 0x00, 0x01}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsEOF(tt.code)
			if got != tt.expect {
				t.Errorf("IsEOF() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestIsValidEOFVersion(t *testing.T) {
	tests := []struct {
		name   string
		code   []byte
		expect bool
	}{
		{"empty", []byte{}, false},
		{"too short", []byte{0xEF, 0x00}, false},
		{"version 1", []byte{0xEF, 0x00, 0x01}, true},
		{"version 0", []byte{0xEF, 0x00, 0x00}, false},
		{"version 2", []byte{0xEF, 0x00, 0x02}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsValidEOFVersion(tt.code)
			if got != tt.expect {
				t.Errorf("IsValidEOFVersion() = %v, want %v", got, tt.expect)
			}
		})
	}
}

func TestHasEOFMagic(t *testing.T) {
	tests := []struct {
		code   []byte
		expect bool
	}{
		{[]byte{}, false},
		{[]byte{0xEF}, false},
		{[]byte{0xEF, 0x00}, true},
		{[]byte{0xEF, 0x01}, false},
		{[]byte{0x00, 0x00}, false},
	}

	for _, tt := range tests {
		got := HasEOFMagic(tt.code)
		if got != tt.expect {
			t.Errorf("HasEOFMagic(%x) = %v, want %v", tt.code, got, tt.expect)
		}
	}
}

// =============================================================================
// EOF Opcode Size Tests
// =============================================================================

func TestEOFOpcodeSize(t *testing.T) {
	tests := []struct {
		op   OpCode
		code []byte
		want int
	}{
		{STOP, []byte{0x00}, 1},
		{PUSH1, []byte{0x60, 0x00}, 2},
		{PUSH32, make([]byte, 33), 33},
		{RJUMP, []byte{0xE0, 0x00, 0x00}, 3},
		{RJUMPI, []byte{0xE1, 0x00, 0x00}, 3},
		{CALLF, []byte{0xE3, 0x00, 0x00}, 3},
		{RETF, []byte{0xE4}, 1},
		{JUMPF, []byte{0xE5, 0x00, 0x00}, 3},
		{DATALOADN, []byte{0xD1, 0x00, 0x00}, 3},
		{DUPN, []byte{0xE6, 0x00}, 2},
		{SWAPN, []byte{0xE7, 0x00}, 2},
		{EXCHANGE, []byte{0xE8, 0x00}, 2},
	}

	for _, tt := range tests {
		got := eofOpcodeSize(tt.op, tt.code)
		if got != tt.want {
			t.Errorf("eofOpcodeSize(%v) = %d, want %d", tt.op, got, tt.want)
		}
	}
}

func TestEOFOpcodeSizeRJUMPV(t *testing.T) {
	// RJUMPV with 3 cases: 2 (header) + 3*2 (offsets) = 8
	code := []byte{byte(RJUMPV), 0x03, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	got := eofOpcodeSize(RJUMPV, code)
	want := 2 + 3*2
	if got != want {
		t.Errorf("eofOpcodeSize(RJUMPV) = %d, want %d", got, want)
	}
}

// =============================================================================
// isValidEOFOpcode Tests
// =============================================================================

func TestIsValidEOFOpcode(t *testing.T) {
	// Valid opcodes
	validOpcodes := []OpCode{STOP, ADD, SUB, MUL, DIV, PUSH1, POP}
	for _, op := range validOpcodes {
		if !isValidEOFOpcode(op) {
			t.Errorf("isValidEOFOpcode(%v) = false, want true", op)
		}
	}

	// Invalid opcodes in EOF
	invalidOpcodes := []OpCode{JUMP, JUMPI, PC, JUMPDEST, CALLCODE, SELFDESTRUCT}
	for _, op := range invalidOpcodes {
		if isValidEOFOpcode(op) {
			t.Errorf("isValidEOFOpcode(%v) = true, want false", op)
		}
	}
}

// =============================================================================
// EOF Section Types Tests
// =============================================================================

func TestEOFSectionTypes(t *testing.T) {
	if EOFSectionTypeCode != 0x01 {
		t.Errorf("EOFSectionTypeCode = 0x%02x, want 0x01", EOFSectionTypeCode)
	}
	if EOFSectionContainer != 0x02 {
		t.Errorf("EOFSectionContainer = 0x%02x, want 0x02", EOFSectionContainer)
	}
	if EOFSectionData != 0x03 {
		t.Errorf("EOFSectionData = 0x%02x, want 0x03", EOFSectionData)
	}
	if EOFSectionTerminator != 0x00 {
		t.Errorf("EOFSectionTerminator = 0x%02x, want 0x00", EOFSectionTerminator)
	}
}

// =============================================================================
// EOF Container Methods Tests
// =============================================================================

func TestEOFContainerGetCodeSection(t *testing.T) {
	container := &EOFContainer{
		Code: [][]byte{
			{0x01, 0x02},
			{0x03, 0x04},
		},
	}

	// Valid index
	code := container.GetCodeSection(0)
	if len(code) != 2 || code[0] != 0x01 {
		t.Error("GetCodeSection(0) returned wrong data")
	}

	// Invalid index
	code = container.GetCodeSection(10)
	if code != nil {
		t.Error("GetCodeSection(10) should return nil")
	}

	// Negative index
	code = container.GetCodeSection(-1)
	if code != nil {
		t.Error("GetCodeSection(-1) should return nil")
	}
}

func TestEOFContainerGetContainer(t *testing.T) {
	container := &EOFContainer{
		Containers: [][]byte{
			{0xEF, 0x00, 0x01},
		},
	}

	sub := container.GetContainer(0)
	if sub == nil {
		t.Error("GetContainer(0) should not return nil")
	}

	sub = container.GetContainer(1)
	if sub != nil {
		t.Error("GetContainer(1) should return nil")
	}
}

func TestEOFContainerNumCodeSections(t *testing.T) {
	container := &EOFContainer{
		Code: [][]byte{{}, {}, {}},
	}

	if container.NumCodeSections() != 3 {
		t.Errorf("NumCodeSections() = %d, want 3", container.NumCodeSections())
	}
}

func TestEOFContainerNumContainers(t *testing.T) {
	container := &EOFContainer{
		Containers: [][]byte{{}, {}},
	}

	if container.NumContainers() != 2 {
		t.Errorf("NumContainers() = %d, want 2", container.NumContainers())
	}
}

// =============================================================================
// EOF New Opcodes Constants Tests
// =============================================================================

func TestEOFNewOpcodes(t *testing.T) {
	// EIP-4200
	if RJUMP != 0xE0 {
		t.Errorf("RJUMP = 0x%02x, want 0xE0", RJUMP)
	}
	if RJUMPI != 0xE1 {
		t.Errorf("RJUMPI = 0x%02x, want 0xE1", RJUMPI)
	}
	if RJUMPV != 0xE2 {
		t.Errorf("RJUMPV = 0x%02x, want 0xE2", RJUMPV)
	}

	// EIP-4750
	if CALLF != 0xE3 {
		t.Errorf("CALLF = 0x%02x, want 0xE3", CALLF)
	}
	if RETF != 0xE4 {
		t.Errorf("RETF = 0x%02x, want 0xE4", RETF)
	}
	if JUMPF != 0xE5 {
		t.Errorf("JUMPF = 0x%02x, want 0xE5", JUMPF)
	}

	// EIP-7480
	if DATALOAD != 0xD0 {
		t.Errorf("DATALOAD = 0x%02x, want 0xD0", DATALOAD)
	}
	if DATALOADN != 0xD1 {
		t.Errorf("DATALOADN = 0x%02x, want 0xD1", DATALOADN)
	}
	if DATASIZE != 0xD2 {
		t.Errorf("DATASIZE = 0x%02x, want 0xD2", DATASIZE)
	}
	if DATACOPY != 0xD3 {
		t.Errorf("DATACOPY = 0x%02x, want 0xD3", DATACOPY)
	}
}

// =============================================================================
// NonReturningFunction Tests
// =============================================================================

func TestNonReturningFunction(t *testing.T) {
	if NonReturningFunction != 0x80 {
		t.Errorf("NonReturningFunction = 0x%02x, want 0x80", NonReturningFunction)
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkIsEOF(b *testing.B) {
	code := []byte{0xEF, 0x00, 0x01, 0x00}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IsEOF(code)
	}
}

func BenchmarkHasEOFMagic(b *testing.B) {
	code := []byte{0xEF, 0x00, 0x01}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		HasEOFMagic(code)
	}
}

func BenchmarkEOFOpcodeSize(b *testing.B) {
	code := []byte{byte(PUSH32)}
	code = append(code, make([]byte, 32)...)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		eofOpcodeSize(PUSH32, code)
	}
}

func BenchmarkIsValidEOFOpcode(b *testing.B) {
	for i := 0; i < b.N; i++ {
		isValidEOFOpcode(ADD)
	}
}

