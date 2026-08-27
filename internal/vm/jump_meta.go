// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// jump_meta.go — a dispatch-shaped view of JumpTable.
//
// JumpTable is [256]*operation, so the interpreter loop pays two dependent
// loads per executed opcode: the pointer out of the 2 KiB table, then the
// operation's own fields from wherever that struct was allocated. Each
// operation is its own heap object, so the second load is effectively a random
// access into the heap and misses cache for anything but the two or three
// hottest opcodes. On the 256-thread dense-block replay profile those two
// source lines alone were 6.1% of all CPU samples.
//
// opMeta copies the fields the loop actually reads into one cache-line-sized
// record and stores them in a flat, contiguous array, so an opcode costs one
// indexed load into a line that then serves every field. Nothing here changes
// what the interpreter does: the values are copied verbatim from the same
// JumpTable the interpreter was built with, and TestOpMetaMatchesJumpTable
// pins that field-for-field over every fork's instruction set.

package vm

import "sync"

// opMeta is the interpreter loop's per-opcode working set. Padded to a 64-byte
// cache line so one opcode never straddles two lines.
type opMeta struct {
	execute     executionFunc
	dynamicGas  gasFunc
	memorySize  memorySizeFunc
	constantGas uint64
	numPop      int
	maxStack    int
	_           [16]byte
}

// opMetaTable is the flat form of one JumpTable.
type opMetaTable [256]opMeta

// opMetaCache memoizes the flat table per JumpTable. The fork instruction sets
// are package-level singletons built once in init, so in practice this holds a
// handful of entries; a caller that builds its own table (ExtraEips, which
// copies before mutating) adds its own and keeps it for that table's lifetime.
var opMetaCache sync.Map // *JumpTable -> *opMetaTable

// opMetaFor returns the flat dispatch table for jt, building it on first use.
func opMetaFor(jt *JumpTable) *opMetaTable {
	if cached, ok := opMetaCache.Load(jt); ok {
		return cached.(*opMetaTable)
	}
	built := newOpMetaTable(jt)
	actual, _ := opMetaCache.LoadOrStore(jt, built)
	return actual.(*opMetaTable)
}

// newOpMetaTable flattens jt. validateAndFillMaxStack guarantees every entry is
// non-nil, so a nil here is a construction bug rather than an undefined opcode
// (those carry opUndefined) — leave the zero value, which faults the same way
// the pointer table would have.
func newOpMetaTable(jt *JumpTable) *opMetaTable {
	tbl := new(opMetaTable)
	for i, op := range jt {
		if op == nil {
			continue
		}
		tbl[i] = opMeta{
			execute:     op.execute,
			dynamicGas:  op.dynamicGas,
			memorySize:  op.memorySize,
			constantGas: op.constantGas,
			numPop:      op.numPop,
			maxStack:    op.maxStack,
		}
	}
	addFusedOps(tbl, jt)
	return tbl
}
