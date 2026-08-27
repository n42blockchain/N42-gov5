package vm

import (
	"math/bits"

	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/params"
)

// Basic-block gas and stack precheck (the evmone "advanced" idea).
//
// The loop used to validate every opcode on its own: two stack-depth
// compares, the constant-gas charge, and the metadata record those values
// live in. Within a basic block execution is straight-line, so the whole
// block's constant gas can be charged when it is entered and its stack needs
// checked once. Failures keep their outcome: any error inside a frame fails
// the frame and consumes its gas, and an early precharge fails exactly when
// the sequential charges would have failed somewhere in the block. Dynamic
// gas is still charged at each opcode.
//
// What changes is what an opcode sees in contract.Gas: the constant gas of
// the rest of the block is already gone. Opcodes whose semantics read the
// remaining gas — GAS, the CALL family (63/64 rule), CREATE/CREATE2, SSTORE
// (EIP-2200 sentry) — get it added back for their duration (blockRest).
//
// Blocks start at pc 0, at every JUMPDEST, and after every JUMPI (fused or
// not); they end at a terminating opcode or just before the next start.
// Instructions between a terminator and the next start are unreachable.
type blockInfo struct {
	gas       uint32 // sum of constant gas, terminator included
	stackReq  int16  // minimum stack height on entry
	maxGrowth int16  // largest height increase over entry within the block
}

type blockTable struct {
	starts []uint64    // bit per pc: block start
	rank   []uint32    // starts before word i
	blocks []blockInfo // in order of start pc
}

func (t *blockTable) isStart(pc uint64) bool {
	return t.starts[pc>>6]&(1<<(pc&63)) != 0
}

func (t *blockTable) index(pc uint64) int {
	w := pc >> 6
	return int(t.rank[w]) + bits.OnesCount64(t.starts[w]&(1<<(pc&63)-1))
}

// tryEnter charges the block starting at pc and reports true when its gas
// and stack needs are met. When they are not, nothing is charged and the
// caller runs the block with per-opcode checks instead: the failure then
// happens at the same opcode as before, after the same state reads — which
// matters, because the witness is a sequential stream of the accesses the
// original execution made, doomed frames included.
func (t *blockTable) tryEnter(pc uint64, contract *Contract, st *stack.Stack) bool {
	b := &t.blocks[t.index(pc)]
	h := st.Len()
	if contract.Gas < uint64(b.gas) || h < int(b.stackReq) || h+int(b.maxGrowth) > int(params.StackLimit) {
		return false
	}
	contract.Gas -= uint64(b.gas)
	return true
}

// rest returns the constant gas of the instructions after pc in pc's block;
// zero when the opcode at pc ends the block itself.
func (t *blockTable) rest(code []byte, pc uint64, meta *opMetaTable) uint64 {
	if meta[code[pc]].terminates {
		return 0
	}
	var sum uint64
	for i := pc + 1; i < uint64(len(code)); {
		if t.isStart(i) {
			break
		}
		op := OpCode(code[i])
		m := &meta[op]
		sum += m.constantGas
		if m.terminates {
			break
		}
		i++
		if op >= PUSH1 && op <= PUSH32 {
			i += uint64(op - PUSH0)
		}
	}
	return sum
}

// fusedSize is the byte length of a fused static jump: PUSH, immediates,
// and the JUMP/JUMPI byte that follows them in the view.
func fusedSize(op OpCode) int {
	if op == fusedPush1Jump || op == fusedPush1Jumpi {
		return 3
	}
	return 4
}

// analyzeBlocks builds the block table for an execution view under one
// fork's jump table.
func analyzeBlocks(code []byte, jt *JumpTable, meta *opMetaTable) *blockTable {
	words := (len(code) + 63) / 64
	t := &blockTable{starts: make([]uint64, words), rank: make([]uint32, words)}
	set := func(pc int) {
		if pc < len(code) {
			t.starts[pc>>6] |= 1 << (uint(pc) & 63)
		}
	}
	set(0)
	for pc := 0; pc < len(code); {
		op := OpCode(code[pc])
		size := 1
		switch {
		case op >= PUSH1 && op <= PUSH32:
			size += int(op - PUSH0)
		case op >= fusedPush1Jump && op <= fusedPush2Jumpi:
			// PUSH byte, its immediates, then the JUMP/JUMPI byte it absorbed.
			size = fusedSize(op)
		}
		switch op {
		case JUMPDEST:
			set(pc)
		case JUMPI, fusedPush1Jumpi, fusedPush2Jumpi:
			set(pc + size)
		}
		pc += size
	}
	var n uint32
	for w := range t.starts {
		t.rank[w] = n
		n += uint32(bits.OnesCount64(t.starts[w]))
	}
	t.blocks = make([]blockInfo, 0, n)
	// Walk each block. pop/push come from the jump table; synthetic opcodes
	// are their PUSH followed by the JUMP/JUMPI.
	for pc := 0; pc < len(code); {
		if !t.isStart(uint64(pc)) {
			pc++ // unreachable byte (after a terminator); skip to the next start
			continue
		}
		var b blockInfo
		var gas uint64
		height, minHeight, maxHeight := 0, 0, 0
		apply := func(pop, push int) {
			height -= pop
			if height < minHeight {
				minHeight = height
			}
			height += push
			if height > maxHeight {
				maxHeight = height
			}
		}
		i := pc
		for i < len(code) {
			if i != pc && t.isStart(uint64(i)) {
				break
			}
			op := OpCode(code[i])
			m := &meta[op]
			gas += m.constantGas
			switch {
			case op >= fusedPush1Jump && op <= fusedPush2Jumpi:
				apply(0, 1) // the PUSH
				if op == fusedPush1Jumpi || op == fusedPush2Jumpi {
					apply(2, 0)
				} else {
					apply(1, 0)
				}
			default:
				apply(jt[op].numPop, jt[op].numPush)
			}
			i++
			if op >= PUSH1 && op <= PUSH32 {
				i += int(op - PUSH0)
			}
			if m.terminates {
				break
			}
		}
		b.gas = uint32(gas)
		b.stackReq = int16(-minHeight)
		b.maxGrowth = int16(maxHeight)
		t.blocks = append(t.blocks, b)
		pc = i
	}
	return t
}
