package vm

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// Static-jump fusion.
//
// A census of mainnet blocks 24.98M–25.0M (24.1G opcodes) found that 4.7% of
// all executed opcodes are the PUSH2 of a PUSH2+JUMPI pair, 2.5% the PUSH2 of
// PUSH2+JUMP, and 7.3% are JUMPDEST — almost all of them the landing of a
// taken jump. The interpreter runs these as separate loop iterations even
// though the destination is a constant known before the first execution.
//
// The execution view of a contract's code replaces each PUSH1/PUSH2 whose
// next opcode is JUMP/JUMPI, and whose constant destination is a valid
// JUMPDEST, by one synthetic opcode that does the push, the jump and the
// JUMPDEST's gas in one iteration. Only the byte at the PUSH's position
// changes; immediates and everything else stay in place, so pc values, PC,
// CODECOPY, jump validation and the tracer (which never sees the view) all
// keep their meaning. The four synthetic values live in the undefined range
// 0x0c–0x0f; a real byte with one of those values at a code position is
// remapped to another undefined opcode so it still fails as before (the
// error reports the original byte, which opUndefined reads from Code).
const (
	fusedPush1Jump  OpCode = 0x0c
	fusedPush1Jumpi OpCode = 0x0d
	fusedPush2Jump  OpCode = 0x0e
	fusedPush2Jumpi OpCode = 0x0f
	fusedCollision  OpCode = 0x21 // undefined in every fork
)

// fuseCode returns the execution view for code given its JUMPDEST bitmap.
// It returns code itself when there is nothing to fuse, so callers can store
// the result without paying for a copy of unfusable code.
func fuseCode(code []byte, analysis []uint64) []byte {
	var fused []byte
	set := func(pc int, op OpCode) {
		if fused == nil {
			fused = make([]byte, len(code))
			copy(fused, code)
		}
		fused[pc] = byte(op)
	}
	for pc := 0; pc < len(code); {
		op := OpCode(code[pc])
		if op >= PUSH1 && op <= PUSH32 {
			n := int(op - PUSH0)
			if n <= 2 && pc+1+n < len(code) {
				next := OpCode(code[pc+1+n])
				if next == JUMP || next == JUMPI {
					dest := int(code[pc+1])
					if n == 2 {
						dest = dest<<8 | int(code[pc+2])
					}
					if dest < len(code) && OpCode(code[dest]) == JUMPDEST && isCodeFromAnalysis(analysis, uint64(dest)) {
						switch {
						case n == 1 && next == JUMP:
							set(pc, fusedPush1Jump)
						case n == 1:
							set(pc, fusedPush1Jumpi)
						case next == JUMP:
							set(pc, fusedPush2Jump)
						default:
							set(pc, fusedPush2Jumpi)
						}
					}
				}
			}
			pc += 1 + n
			continue
		}
		if op >= fusedPush1Jump && op <= fusedPush2Jumpi {
			set(pc, fusedCollision)
		}
		pc++
	}
	if fused == nil {
		return code
	}
	return fused
}

// execView returns the execution view for a contract with a known code hash,
// building and caching it on first use. It also resolves the JUMPDEST bitmap,
// which the first JUMP would otherwise compute.
func execView(c *Contract) []byte {
	if entry := GlobalCodeAnalysisCache.entry(c.CodeHash); entry != nil && entry.fused != nil {
		if c.analysis == nil {
			c.analysis = entry.analysis
		}
		return entry.fused
	}
	analysis := c.analysis
	if analysis == nil {
		var ok bool
		if analysis, ok = c.jumpdests[c.CodeHash]; !ok {
			if analysis, ok = GlobalCodeAnalysisCache.Get(c.CodeHash); !ok {
				analysis = codeBitmap(c.Code)
			}
		}
		c.analysis = analysis
	}
	fused := fuseCode(c.Code, analysis)
	GlobalCodeAnalysisCache.putFused(c.CodeHash, analysis, fused)
	return fused
}

// addFusedOps adds the synthetic opcodes to a flat table built from a jump
// table that defines JUMP and JUMPI. The execute functions are the fallback
// for callers that dispatch through the table; the interpreter loop handles
// these opcodes inline.
func addFusedOps(tbl *opMetaTable, jt *JumpTable) {
	if jt[JUMP] == nil || jt[JUMPI] == nil || jt[PUSH1] == nil {
		return
	}
	pushGas, pushMax := jt[PUSH1].constantGas, jt[PUSH1].maxStack
	tbl[fusedPush1Jump] = opMeta{execute: opFusedPush1Jump, constantGas: pushGas + jt[JUMP].constantGas, numPop: 0, maxStack: pushMax}
	tbl[fusedPush2Jump] = opMeta{execute: opFusedPush2Jump, constantGas: pushGas + jt[JUMP].constantGas, numPop: 0, maxStack: pushMax}
	tbl[fusedPush1Jumpi] = opMeta{execute: opFusedPush1Jumpi, constantGas: pushGas + jt[JUMPI].constantGas, numPop: 1, maxStack: pushMax}
	tbl[fusedPush2Jumpi] = opMeta{execute: opFusedPush2Jumpi, constantGas: pushGas + jt[JUMPI].constantGas, numPop: 1, maxStack: pushMax}
}

func fusedDest(code []byte, pc uint64, n uint64) uint64 {
	dest := uint64(code[pc+1])
	if n == 2 {
		dest = dest<<8 | uint64(code[pc+2])
	}
	return dest
}

// fusedJump lands on the pre-validated JUMPDEST at dest: charges its gas and
// leaves pc on it so the loop's increment steps over it.
func fusedJump(pc *uint64, contract *Contract, dest uint64) error {
	if !contract.UseGas(params.JumpdestGas) {
		return ErrOutOfGas
	}
	*pc = dest
	return nil
}

func opFusedPush1Jump(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	return nil, fusedJump(pc, scope.Contract, fusedDest(scope.Contract.Code, *pc, 1))
}

func opFusedPush2Jump(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	return nil, fusedJump(pc, scope.Contract, fusedDest(scope.Contract.Code, *pc, 2))
}

func opFusedPush1Jumpi(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	if cond := scope.Stack.PopPtrUnchecked(); !cond.IsZero() {
		return nil, fusedJump(pc, scope.Contract, fusedDest(scope.Contract.Code, *pc, 1))
	}
	*pc += 2
	return nil, nil
}

func opFusedPush2Jumpi(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	if cond := scope.Stack.PopPtrUnchecked(); !cond.IsZero() {
		return nil, fusedJump(pc, scope.Contract, fusedDest(scope.Contract.Code, *pc, 2))
	}
	*pc += 3
	return nil, nil
}

// plainView returns code with any real 0x0c–0x0f byte at a code position
// remapped to fusedCollision, so that code run without the fused view can
// never dispatch a synthetic opcode. Code without such bytes — all of it in
// practice — is returned as is without copying.
func plainView(code []byte) []byte {
	var out []byte
	for pc := 0; pc < len(code); pc++ {
		op := OpCode(code[pc])
		if op >= PUSH1 && op <= PUSH32 {
			pc += int(op - PUSH0)
			continue
		}
		if op >= fusedPush1Jump && op <= fusedPush2Jumpi {
			if out == nil {
				out = make([]byte, len(code))
				copy(out, code)
			}
			out[pc] = byte(fusedCollision)
		}
	}
	if out == nil {
		return code
	}
	return out
}

// canFuse reports whether the interpreter may run contract through its
// execution view: legacy code with a known hash, no tracer attached.
func canFuse(c *Contract, debug bool) bool {
	return !debug && c.EOFContainer == nil && GlobalCodeAnalysisCache != nil && c.CodeHash != (types.Hash{})
}
