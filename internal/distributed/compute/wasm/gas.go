// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Fuel-based gas metering helpers for the WASM engine. SafeGasAdd and
// SafeGasMul clamp arithmetic at math.MaxUint64 on overflow. GasTable
// maps instruction categories (BaseInstruction, MemoryLoad, MemoryGrow,
// BranchInstruction, CallInstruction) to fuel costs for wazero.

package wasm

import "math"

// SafeGasAdd returns a + b, capped at math.MaxUint64 on overflow.
func SafeGasAdd(a, b uint64) uint64 {
	sum := a + b
	if sum < a {
		return math.MaxUint64
	}
	return sum
}

// SafeGasMul returns a * b, capped at math.MaxUint64 on overflow.
func SafeGasMul(a, b uint64) uint64 {
	if a == 0 || b == 0 {
		return 0
	}
	result := a * b
	if result/a != b {
		return math.MaxUint64
	}
	return result
}

// GasTable defines gas costs for WASM operations.
// Uses the fuel-based metering model (compatible with wazero's fuel system).
// Each fuel unit maps to a configurable amount of gas via the engine's gasMultiplier.
type GasTable struct {
	// Instruction-level costs (fuel per WASM instruction category)
	BaseInstruction   uint64 // simple operations (i32.add, local.get, etc.)
	MemoryLoad        uint64 // memory.load variants
	MemoryStore       uint64 // memory.store variants
	MemoryGrow        uint64 // memory.grow (per page = 64KB)
	BranchInstruction uint64 // br, br_if, br_table
	CallInstruction   uint64 // call, call_indirect

	// Host function call costs (fuel per call)
	HostCASLoad    uint64 // cas_load: CAS content retrieval
	HostCASStore   uint64 // cas_store: CAS content storage
	HostKeccak256  uint64 // keccak256: base cost + per-byte
	HostKeccak256B uint64 // keccak256: per-byte cost
	HostLogMsg     uint64 // log_msg: base cost
	HostLogMsgB    uint64 // log_msg: per-byte cost

	// Memory allocation costs
	MemoryPageCost uint64 // cost per additional 64KB page
	MaxMemoryPages uint32 // hard limit on memory pages
}

// DefaultGasTable returns the standard gas pricing for WASM operations.
func DefaultGasTable() *GasTable {
	return &GasTable{
		// Instruction costs (fuel units)
		BaseInstruction:   1,
		MemoryLoad:        3,
		MemoryStore:       3,
		MemoryGrow:        1000,
		BranchInstruction: 2,
		CallInstruction:   5,

		// Host function costs
		HostCASLoad:    5000,
		HostCASStore:   20000,
		HostKeccak256:  30,
		HostKeccak256B: 6, // per byte
		HostLogMsg:     100,
		HostLogMsgB:    1, // per byte

		// Memory costs
		MemoryPageCost: 1000,
		MaxMemoryPages: 4096, // 256MB max (4096 * 64KB)
	}
}

// Keccak256Cost returns the total gas cost for hashing the given number of bytes.
func (g *GasTable) Keccak256Cost(dataLen int) uint64 {
	return SafeGasAdd(g.HostKeccak256, SafeGasMul(g.HostKeccak256B, uint64(dataLen)))
}

// LogMsgCost returns the total gas cost for logging a message of the given length.
func (g *GasTable) LogMsgCost(msgLen int) uint64 {
	return SafeGasAdd(g.HostLogMsg, SafeGasMul(g.HostLogMsgB, uint64(msgLen)))
}

// MemoryGrowCost returns the gas cost for growing memory by the given number of pages.
func (g *GasTable) MemoryGrowCost(pages uint32) uint64 {
	return SafeGasAdd(g.MemoryGrow, SafeGasMul(g.MemoryPageCost, uint64(pages)))
}
