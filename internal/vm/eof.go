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

// EOF (EVM Object Format) Implementation
// Reference implementations from go-ethereum and erigon
//
// EIPs implemented:
// - EIP-3540: EOF - EVM Object Format v1
// - EIP-3670: EOF - Code Validation
// - EIP-4200: EOF - Static relative jumps (RJUMP, RJUMPI, RJUMPV)
// - EIP-4750: EOF - Functions (CALLF, RETF, JUMPF)
// - EIP-5450: EOF - Stack Validation
// - EIP-6206: EOF - JUMPF and non-returning functions
// - EIP-7480: EOF - Data section access instructions (DATALOAD, DATALOADN, DATASIZE, DATACOPY)
// - EIP-7620: EOF - Contract creation instructions (EOFCREATE, RETURNCONTRACT)
// - EIP-7698: EOF - Creation transaction

package vm

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// =============================================================================
// EOF Magic and Version Constants
// =============================================================================

const (
	// EOFMagic is the magic bytes at the start of EOF containers
	EOFMagic = 0xEF00

	// EOFVersion1 is EOF version 1
	EOFVersion1 = 0x01

	// EOFFormatByte is the EOF format identifier
	EOFFormatByte = 0xEF
)

// Section type identifiers
const (
	EOFSectionTypeCode    = 0x01 // Code section
	EOFSectionContainer   = 0x02 // Container section (for EOFCREATE)
	EOFSectionData        = 0x03 // Data section
	EOFSectionTerminator  = 0x00 // Section header terminator
)

// =============================================================================
// EOF Header Structure
// =============================================================================

// EOFHeader represents the header of an EOF container
type EOFHeader struct {
	Version       uint8            // EOF version
	TypeSize      uint16           // Size of type section
	CodeSizes     []uint16         // Sizes of code sections
	ContainerSizes []uint16        // Sizes of container sections
	DataSize      uint16           // Size of data section
	Types         []EOFTypeSection // Type information for each code section
}

// EOFTypeSection represents type information for a code section
type EOFTypeSection struct {
	Inputs      uint8  // Number of stack inputs
	Outputs     uint8  // Number of stack outputs (0x80 = non-returning)
	MaxStackHeight uint16 // Maximum stack height during execution
}

// EOFContainer represents a parsed EOF container
type EOFContainer struct {
	Header     *EOFHeader
	TypesData  []byte
	Code       [][]byte // Code sections
	Containers [][]byte // Sub-containers
	Data       []byte   // Data section
}

// =============================================================================
// EOF New Opcodes
// =============================================================================

// EOF-specific opcodes
const (
	// EIP-4200: Static relative jumps
	RJUMP  OpCode = 0xE0 // Relative jump
	RJUMPI OpCode = 0xE1 // Conditional relative jump
	RJUMPV OpCode = 0xE2 // Jump table (switch)

	// EIP-4750: Functions
	CALLF OpCode = 0xE3 // Call function
	RETF  OpCode = 0xE4 // Return from function
	JUMPF OpCode = 0xE5 // Jump to function (tail call)

	// EIP-7480: Data section access
	DATALOAD  OpCode = 0xD0 // Load 32 bytes from data section
	DATALOADN OpCode = 0xD1 // Load 32 bytes from data section (immediate offset)
	DATASIZE  OpCode = 0xD2 // Get data section size
	DATACOPY  OpCode = 0xD3 // Copy from data section to memory

	// EIP-7620: Contract creation
	EOFCREATE      OpCode = 0xEC // Create contract from EOF container
	RETURNCONTRACT OpCode = 0xEE // Return new contract from initcode
	RETURNDATALOAD OpCode = 0xF7 // Load 32 bytes from return data

	// EIP-663: Unlimited SWAP and DUP
	DUPN  OpCode = 0xE6 // DUP with immediate operand
	SWAPN OpCode = 0xE7 // SWAP with immediate operand
	EXCHANGE OpCode = 0xE8 // Exchange two stack items
)

// NonReturningFunction indicates a function that doesn't return
const NonReturningFunction = 0x80

// =============================================================================
// EOF Validation
// =============================================================================

// EOF validation errors
var (
	ErrEOFInvalidMagic           = errors.New("invalid EOF magic")
	ErrEOFInvalidVersion         = errors.New("invalid EOF version")
	ErrEOFInvalidSectionKind     = errors.New("invalid section kind")
	ErrEOFMissingTypeSection     = errors.New("missing type section")
	ErrEOFMissingCodeSection     = errors.New("missing code section")
	ErrEOFMissingDataSection     = errors.New("missing data section")
	ErrEOFMissingTerminator      = errors.New("missing header terminator")
	ErrEOFZeroSectionSize        = errors.New("zero section size")
	ErrEOFInvalidTypeSize        = errors.New("invalid type section size")
	ErrEOFTooManyInputs          = errors.New("too many inputs")
	ErrEOFTooManyOutputs         = errors.New("too many outputs")
	ErrEOFInvalidMaxStackHeight  = errors.New("invalid max stack height")
	ErrEOFInvalidFirstCode       = errors.New("first code section must have 0 inputs and non-zero outputs")
	ErrEOFUndefinedInstruction   = errors.New("undefined instruction")
	ErrEOFTruncatedInstruction   = errors.New("truncated instruction")
	ErrEOFInvalidJumpDest        = errors.New("invalid jump destination")
	ErrEOFInvalidRJUMPVCount     = errors.New("invalid RJUMPV count")
	ErrEOFStackUnderflow         = errors.New("stack underflow")
	ErrEOFStackOverflow          = errors.New("stack overflow")
	ErrEOFInvalidCallF           = errors.New("invalid CALLF target")
	ErrEOFInvalidRetF            = errors.New("invalid RETF in non-returning function")
	ErrEOFUnreachableCode        = errors.New("unreachable code")
	ErrEOFInvalidContainer       = errors.New("invalid container")
	ErrEOFInvalidDataOffset      = errors.New("invalid data offset")
)

// IsEOF checks if the code starts with EOF magic bytes
func IsEOF(code []byte) bool {
	return len(code) >= 2 && code[0] == EOFFormatByte && code[1] == 0x00
}

// IsValidEOFVersion checks if the EOF version is valid
func IsValidEOFVersion(code []byte) bool {
	return len(code) >= 3 && code[2] == EOFVersion1
}

// HasEOFMagic checks if code has the EOF magic prefix (0xEF00)
func HasEOFMagic(code []byte) bool {
	return len(code) >= 2 && binary.BigEndian.Uint16(code[:2]) == EOFMagic
}

// ParseEOF parses an EOF container and validates its structure
func ParseEOF(code []byte) (*EOFContainer, error) {
	if len(code) < 7 {
		return nil, ErrEOFInvalidMagic
	}

	// Check magic
	if code[0] != EOFFormatByte || code[1] != 0x00 {
		return nil, ErrEOFInvalidMagic
	}

	// Check version
	if code[2] != EOFVersion1 {
		return nil, ErrEOFInvalidVersion
	}

	container := &EOFContainer{
		Header: &EOFHeader{
			Version: code[2],
		},
	}

	pos := 3

	// Parse sections
	for pos < len(code) {
		if pos >= len(code) {
			return nil, ErrEOFMissingTerminator
		}

		sectionKind := code[pos]
		pos++

		switch sectionKind {
		case EOFSectionTerminator:
			// End of header
			return parseEOFBody(code, pos, container)

		case EOFSectionTypeCode:
			// Type section - just read size for now
			if pos+2 > len(code) {
				return nil, ErrEOFMissingTypeSection
			}
			container.Header.TypeSize = binary.BigEndian.Uint16(code[pos:])
			pos += 2

			// Code sections follow
			if pos >= len(code) || code[pos] != EOFSectionTypeCode {
				return nil, ErrEOFMissingCodeSection
			}
			pos++

			// Read number of code sections
			if pos+2 > len(code) {
				return nil, ErrEOFMissingCodeSection
			}
			numCode := binary.BigEndian.Uint16(code[pos:])
			pos += 2

			// Read code section sizes
			container.Header.CodeSizes = make([]uint16, numCode)
			for i := uint16(0); i < numCode; i++ {
				if pos+2 > len(code) {
					return nil, ErrEOFMissingCodeSection
				}
				container.Header.CodeSizes[i] = binary.BigEndian.Uint16(code[pos:])
				pos += 2
				if container.Header.CodeSizes[i] == 0 {
					return nil, ErrEOFZeroSectionSize
				}
			}

		case EOFSectionContainer:
			// Container section (for EOFCREATE)
			if pos+2 > len(code) {
				return nil, ErrEOFInvalidContainer
			}
			numContainers := binary.BigEndian.Uint16(code[pos:])
			pos += 2

			container.Header.ContainerSizes = make([]uint16, numContainers)
			for i := uint16(0); i < numContainers; i++ {
				if pos+2 > len(code) {
					return nil, ErrEOFInvalidContainer
				}
				container.Header.ContainerSizes[i] = binary.BigEndian.Uint16(code[pos:])
				pos += 2
			}

		case EOFSectionData:
			// Data section
			if pos+2 > len(code) {
				return nil, ErrEOFMissingDataSection
			}
			container.Header.DataSize = binary.BigEndian.Uint16(code[pos:])
			pos += 2

		default:
			return nil, ErrEOFInvalidSectionKind
		}
	}

	return nil, ErrEOFMissingTerminator
}

// parseEOFBody parses the body sections of an EOF container
func parseEOFBody(code []byte, pos int, container *EOFContainer) (*EOFContainer, error) {
	// Parse type section
	typeSize := int(container.Header.TypeSize)
	if pos+typeSize > len(code) {
		return nil, ErrEOFMissingTypeSection
	}
	container.TypesData = code[pos : pos+typeSize]
	pos += typeSize

	// Parse type section into structures
	numCodes := len(container.Header.CodeSizes)
	if typeSize != numCodes*4 {
		return nil, ErrEOFInvalidTypeSize
	}

	container.Header.Types = make([]EOFTypeSection, numCodes)
	for i := 0; i < numCodes; i++ {
		offset := i * 4
		container.Header.Types[i] = EOFTypeSection{
			Inputs:         container.TypesData[offset],
			Outputs:        container.TypesData[offset+1],
			MaxStackHeight: binary.BigEndian.Uint16(container.TypesData[offset+2:]),
		}
	}

	// Validate first code section
	if numCodes > 0 {
		if container.Header.Types[0].Inputs != 0 {
			return nil, ErrEOFInvalidFirstCode
		}
		if container.Header.Types[0].Outputs == NonReturningFunction {
			return nil, ErrEOFInvalidFirstCode
		}
	}

	// Parse code sections
	container.Code = make([][]byte, numCodes)
	for i, size := range container.Header.CodeSizes {
		if pos+int(size) > len(code) {
			return nil, ErrEOFMissingCodeSection
		}
		container.Code[i] = code[pos : pos+int(size)]
		pos += int(size)
	}

	// Parse container sections
	container.Containers = make([][]byte, len(container.Header.ContainerSizes))
	for i, size := range container.Header.ContainerSizes {
		if pos+int(size) > len(code) {
			return nil, ErrEOFInvalidContainer
		}
		container.Containers[i] = code[pos : pos+int(size)]
		pos += int(size)
	}

	// Parse data section
	dataSize := int(container.Header.DataSize)
	if pos+dataSize > len(code) {
		// Data section can be truncated for deploy containers
		container.Data = code[pos:]
	} else {
		container.Data = code[pos : pos+dataSize]
	}

	return container, nil
}

// ValidateEOF validates an EOF container completely
func ValidateEOF(code []byte) error {
	container, err := ParseEOF(code)
	if err != nil {
		return err
	}

	// Validate each code section
	for i, codeSection := range container.Code {
		typeInfo := container.Header.Types[i]
		if err := validateCodeSection(codeSection, typeInfo, container); err != nil {
			return fmt.Errorf("code section %d: %w", i, err)
		}
	}

	// Validate sub-containers recursively
	for i, subContainer := range container.Containers {
		if err := ValidateEOF(subContainer); err != nil {
			return fmt.Errorf("container %d: %w", i, err)
		}
	}

	return nil
}

// validateCodeSection validates a single code section
func validateCodeSection(code []byte, typeInfo EOFTypeSection, container *EOFContainer) error {
	if len(code) == 0 {
		return ErrEOFZeroSectionSize
	}

	// Validate each instruction
	pos := 0
	for pos < len(code) {
		op := OpCode(code[pos])

		// Check for undefined instructions
		if !isValidEOFOpcode(op) {
			return ErrEOFUndefinedInstruction
		}

		// Get instruction size
		size := eofOpcodeSize(op, code[pos:])
		if pos+size > len(code) {
			return ErrEOFTruncatedInstruction
		}

		// Validate specific instructions
		switch op {
		case RJUMP, RJUMPI:
			offset := int16(binary.BigEndian.Uint16(code[pos+1:]))
			target := pos + 3 + int(offset)
			if target < 0 || target >= len(code) {
				return ErrEOFInvalidJumpDest
			}

		case RJUMPV:
			if pos+1 >= len(code) {
				return ErrEOFTruncatedInstruction
			}
			count := int(code[pos+1])
			if count == 0 {
				return ErrEOFInvalidRJUMPVCount
			}

		case CALLF:
			if pos+2 >= len(code) {
				return ErrEOFTruncatedInstruction
			}
			funcIdx := binary.BigEndian.Uint16(code[pos+1:])
			if int(funcIdx) >= len(container.Header.Types) {
				return ErrEOFInvalidCallF
			}

		case JUMPF:
			if pos+2 >= len(code) {
				return ErrEOFTruncatedInstruction
			}
			funcIdx := binary.BigEndian.Uint16(code[pos+1:])
			if int(funcIdx) >= len(container.Header.Types) {
				return ErrEOFInvalidCallF
			}

		case DATALOADN:
			if pos+2 >= len(code) {
				return ErrEOFTruncatedInstruction
			}
			offset := binary.BigEndian.Uint16(code[pos+1:])
			if int(offset)+32 > int(container.Header.DataSize) {
				return ErrEOFInvalidDataOffset
			}
		}

		pos += size
	}

	return nil
}

// isValidEOFOpcode checks if an opcode is valid in EOF
func isValidEOFOpcode(op OpCode) bool {
	// EOF disables some legacy opcodes
	switch op {
	case JUMP, JUMPI, PC, JUMPDEST:
		return false // Legacy jumps not allowed
	case CODESIZE, CODECOPY, EXTCODESIZE, EXTCODECOPY, EXTCODEHASH:
		return false // Code introspection not allowed
	case CALLCODE, SELFDESTRUCT:
		return false // Deprecated opcodes
	case CREATE, CREATE2:
		return false // Use EOFCREATE instead
	case GAS:
		return false // GAS opcode disabled in EOF
	}
	return true
}

// eofOpcodeSize returns the total size of an instruction including operands
func eofOpcodeSize(op OpCode, code []byte) int {
	switch op {
	case PUSH1:
		return 2
	case PUSH2:
		return 3
	case PUSH3:
		return 4
	case PUSH4:
		return 5
	case PUSH5:
		return 6
	case PUSH6:
		return 7
	case PUSH7:
		return 8
	case PUSH8:
		return 9
	case PUSH9:
		return 10
	case PUSH10:
		return 11
	case PUSH11:
		return 12
	case PUSH12:
		return 13
	case PUSH13:
		return 14
	case PUSH14:
		return 15
	case PUSH15:
		return 16
	case PUSH16:
		return 17
	case PUSH17:
		return 18
	case PUSH18:
		return 19
	case PUSH19:
		return 20
	case PUSH20:
		return 21
	case PUSH21:
		return 22
	case PUSH22:
		return 23
	case PUSH23:
		return 24
	case PUSH24:
		return 25
	case PUSH25:
		return 26
	case PUSH26:
		return 27
	case PUSH27:
		return 28
	case PUSH28:
		return 29
	case PUSH29:
		return 30
	case PUSH30:
		return 31
	case PUSH31:
		return 32
	case PUSH32:
		return 33
	case RJUMP, RJUMPI:
		return 3 // opcode + 2 byte offset
	case RJUMPV:
		if len(code) > 1 {
			return 2 + int(code[1])*2 // opcode + count + count*2 bytes
		}
		return 2
	case CALLF, JUMPF:
		return 3 // opcode + 2 byte function index
	case DATALOADN:
		return 3 // opcode + 2 byte offset
	case DUPN, SWAPN:
		return 2 // opcode + 1 byte operand
	case EXCHANGE:
		return 2 // opcode + 1 byte operand
	case EOFCREATE, RETURNCONTRACT:
		return 2 // opcode + 1 byte container index
	default:
		return 1
	}
}

// =============================================================================
// EOF Code Section Accessors
// =============================================================================

// GetCodeSection returns the code at the given section index
func (c *EOFContainer) GetCodeSection(idx int) []byte {
	if idx >= 0 && idx < len(c.Code) {
		return c.Code[idx]
	}
	return nil
}

// GetTypeInfo returns the type information for a code section
func (c *EOFContainer) GetTypeInfo(idx int) *EOFTypeSection {
	if idx >= 0 && idx < len(c.Header.Types) {
		return &c.Header.Types[idx]
	}
	return nil
}

// GetContainer returns a sub-container at the given index
func (c *EOFContainer) GetContainer(idx int) []byte {
	if idx >= 0 && idx < len(c.Containers) {
		return c.Containers[idx]
	}
	return nil
}

// GetData returns the data section
func (c *EOFContainer) GetData() []byte {
	return c.Data
}

// DataSize returns the declared data section size
func (c *EOFContainer) DataSize() int {
	return int(c.Header.DataSize)
}

// NumCodeSections returns the number of code sections
func (c *EOFContainer) NumCodeSections() int {
	return len(c.Code)
}

// NumContainers returns the number of sub-containers
func (c *EOFContainer) NumContainers() int {
	return len(c.Containers)
}

