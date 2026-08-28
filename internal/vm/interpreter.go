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
//
// EVMInterpreter main loop and its configuration. Config captures
// debug/tracer/NoRecursion/NoBaseFee/SkipAnalysis and stateless flags
// plus ExtraEips and a SlotAccessRecorder hook called on every SLOAD to
// train the predictive state prefetcher. SlotAccessRecorder is a
// lightweight interface so callers opt in without pulling the prefetch
// package. Memory, stack and scope are owned per call depth by the
// interpreter (runFrame) so the hot path does not allocate.
package vm

import (
	"hash"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/keccak"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/params"
)

// SlotAccessRecorder is an optional callback for recording storage slot accesses.
// Used by the prefetch predictor to learn which slots are frequently read.
type SlotAccessRecorder interface {
	RecordSlotAccess(contract types.Address, slot types.Hash)
}

// Config are the configuration options for the Interpreter
type Config struct {
	Debug         bool      // Enables debugging
	Tracer        EVMLogger // Opcode logger
	NoRecursion   bool      // Disables call, callcode, delegate call and create
	NoBaseFee     bool      // Forces the EIP-1559 baseFee to 0 (needed for 0 price calls)
	SkipAnalysis  bool      // Whether we can skip jumpdest analysis based on the checked history
	TraceJumpDest bool      // Print transaction hashes where jumpdest analysis was useful
	NoReceipts    bool      // Do not calculate receipts
	ReadOnly      bool      // Do no perform any block finalisation
	StatelessExec bool      // true is certain conditions (like state trie root hash matching) need to be relaxed for stateless EVM execution
	RestoreState  bool      // Revert all changes made to the state (useful for constant system calls)

	ExtraEips    []int              // Additional EIPS that are to be enabled
	SlotRecorder SlotAccessRecorder // If non-nil, called on every SLOAD for predictive prefetching
}

func (vmConfig *Config) HasEip3860(rules *params.Rules) bool {
	for _, eip := range vmConfig.ExtraEips {
		if eip == 3860 {
			return true
		}
	}
	return rules.IsShanghai
}

// Interpreter is used to run Ethereum based contracts and will utilise the
// passed environment to query external sources for state information.
// The Interpreter will run the byte code VM based on the passed
// configuration.
type Interpreter interface {
	// Run loops and evaluates the contract's code with the given input data and returns
	// the return byte-slice and an error if one occurred.
	Run(contract *Contract, input []byte, static bool) ([]byte, error)

	// `Depth` returns the current call stack's depth.
	Depth() int
}

// ScopeContext contains the things that are per-call, such as stack and memory,
// but not transients like pc and gas
type ScopeContext struct {
	Memory      *Memory
	Stack       *stack.Stack
	Contract    *Contract
	ReturnStack *stack.ReturnStack // EOF function return stack
}

// keccakState wraps sha3.state. In addition to the usual hash methods, it also supports
// Read to get a variable amount of data from the hash state. Read is faster than Sum
// because it doesn't copy the internal state, but also modifies the internal state.
type keccakState interface {
	hash.Hash
	Read([]byte) (int, error)
}

// EVMInterpreter represents an EVM interpreter
type EVMInterpreter struct {
	*VM
	jt *JumpTable // EVM instruction table
	// meta is jt flattened into one cache-line record per opcode. The run loop
	// reads it instead of chasing jt's pointers; see jump_meta.go.
	meta  *opMetaTable
	depth int
	// frames holds one memory/stack/scope triple per call depth. Execution is
	// strictly depth-first, so the frame at depth d is free again the moment
	// the call at depth d returns, and the next call at that depth can reuse
	// it. Before this, every Run took a Memory and a Stack out of sync.Pools
	// and allocated a ScopeContext — about 1% of all replay CPU across the
	// CALL-heavy dense blocks, plus Pool.getSlow traffic under 100+ workers.
	frames []*runFrame
	// sha3Memo remembers KECCAK256 results for 64-byte inputs (mapping-slot
	// derivations: 88% of all SHA3 opcodes). Within one block 62% of them
	// repeat an input already hashed, and a map probe is an order of
	// magnitude cheaper than the permutation. Bounded by sha3MemoMax.
	sha3Memo map[[64]byte]types.Hash
}

const sha3MemoMax = 1 << 16

// runFrame is the per-depth scratch for one interpreter invocation.
type runFrame struct {
	mem   Memory
	stack stack.Stack
	scope ScopeContext
	// pc lives here rather than as a Run local: its address is handed to every
	// opcode through a function value, which makes a local escape to the heap
	// — one allocation per interpreter invocation.
	pc uint64
}

// runFrameMemoryKeep bounds the memory buffer a frame keeps between calls. A
// single call that expanded memory to tens of megabytes must not pin that
// buffer on the interpreter for every later call at the same depth.
const runFrameMemoryKeep = 1 << 20

// frame returns the scratch frame for the current depth, reset for use.
func (in *EVMInterpreter) frame(contract *Contract) (*Memory, *stack.Stack, *ScopeContext, *uint64) {
	d := in.depth
	for len(in.frames) <= d {
		in.frames = append(in.frames, &runFrame{
			mem:   Memory{store: make([]byte, 0, 4*1024)},
			stack: stack.Stack{Data: make([]uint256.Int, 0, 16)},
		})
	}
	f := in.frames[d]
	if cap(f.mem.store) > runFrameMemoryKeep {
		f.mem.store = make([]byte, 0, 4*1024)
	}
	f.mem.Reset()
	f.stack.Reset()
	f.scope = ScopeContext{Memory: &f.mem, Stack: &f.stack, Contract: contract}
	f.pc = 0
	return &f.mem, &f.stack, &f.scope, &f.pc
}

type VM struct {
	evm VMInterpreter
	cfg Config

	hasher    keccak.State // Keccak256 sponge shared across opcodes (concrete: no interface calls, nothing escapes)
	hasherBuf types.Hash   // Keccak256 hasher result array shared across opcodes

	readOnly   bool   // Whether to throw on stateful modifications
	returnData []byte // Last CALL's return data for subsequent reuse
}

func copyJumpTable(jt *JumpTable) *JumpTable {
	var copy JumpTable
	for i, op := range jt {
		if op != nil {
			opCopy := *op
			copy[i] = &opCopy
		}
	}
	return &copy
}

// NewEVMInterpreter returns a new instance of the Interpreter.
func NewEVMInterpreter(evm VMInterpreter, cfg Config) *EVMInterpreter {
	var jt *JumpTable
	switch {
	case evm.ChainRules().IsGlamsterdam:
		if evm.ChainRules().IsEOF {
			jt = &glamsterdamEOFInstructionSet
		} else {
			jt = &glamsterdamInstructionSet
		}
	case evm.ChainRules().IsFusaka:
		if evm.ChainRules().IsEOF {
			jt = &fusakaEOFInstructionSet
		} else {
			jt = &fusakaInstructionSet
		}
	case evm.ChainRules().IsOsaka:
		if evm.ChainRules().IsEOF {
			jt = &osakaEOFInstructionSet
		} else {
			jt = &osakaInstructionSet
		}
	case evm.ChainRules().IsPectra:
		jt = &pectraInstructionSet
	case evm.ChainRules().IsPrague:
		jt = &pragueInstructionSet
	case evm.ChainRules().IsCancun:
		jt = &cancunInstructionSet
	case evm.ChainRules().IsShanghai:
		jt = &shanghaiInstructionSet
	case evm.ChainRules().IsLondon:
		jt = &londonInstructionSet
	case evm.ChainRules().IsBerlin:
		jt = &berlinInstructionSet
	case evm.ChainRules().IsIstanbul:
		jt = &istanbulInstructionSet
	case evm.ChainRules().IsConstantinople:
		jt = &constantinopleInstructionSet
	case evm.ChainRules().IsByzantium:
		jt = &byzantiumInstructionSet
	case evm.ChainRules().IsSpuriousDragon:
		jt = &spuriousDragonInstructionSet
	case evm.ChainRules().IsTangerineWhistle:
		jt = &tangerineWhistleInstructionSet
	case evm.ChainRules().IsHomestead:
		jt = &homesteadInstructionSet
	default:
		jt = &frontierInstructionSet
	}
	if len(cfg.ExtraEips) > 0 {
		jt = copyJumpTable(jt)
		// Use reverse iteration to safely remove elements during iteration
		for i := len(cfg.ExtraEips) - 1; i >= 0; i-- {
			eip := cfg.ExtraEips[i]
			if err := EnableEIP(eip, jt); err != nil {
				// Disable it, so caller can check if it's activated or not
				cfg.ExtraEips = append(cfg.ExtraEips[:i], cfg.ExtraEips[i+1:]...)
				log.Error("EIP activation failed", "eip", eip, "err", err)
			}
		}
	}

	return &EVMInterpreter{
		VM: &VM{
			evm: evm,
			cfg: cfg,
		},
		jt:   jt,
		meta: opMetaFor(jt),
	}
}

func (in *EVMInterpreter) decrementDepth() { in.depth-- }

// Run loops and evaluates the contract's code with the given input data and returns
// the return byte-slice and an error if one occurred.
//
// It's important to note that any errors returned by the interpreter should be
// considered a revert-and-consume-all-gas operation except for
// ErrExecutionReverted which means revert-and-keep-gas-left.
func (in *EVMInterpreter) Run(contract *Contract, input []byte, readOnly bool) (ret []byte, err error) {
	// Don't bother with the execution if there's no code.
	if len(contract.Code) == 0 {
		return nil, nil
	}

	// Increment the call depth which is restricted to 1024
	in.depth++
	defer in.decrementDepth()

	// Make sure the readOnly is only set if we aren't in readOnly yet.
	// This makes also sure that the readOnly flag isn't removed for child calls.
	if readOnly && !in.readOnly {
		in.readOnly = true
		defer func() { in.readOnly = false }()
	}

	// Reset the previous call's return data. It's unimportant to preserve the old buffer
	// as every returning call will return new data anyway.
	in.returnData = nil

	var op OpCode // current opcode
	mem, locStack, callContext, pc := in.frame(contract)

	// EOF: Initialize return stack and set code to section 0 for EOF contracts.
	// Gated on the EOFTime rule: without it a legacy-chain contract whose code
	// happens to parse as a container must still execute as legacy bytes
	// (first byte 0xEF -> invalid opcode, matching geth).
	if contract.EOFContainer != nil && !in.evm.ChainRules().IsEOF {
		contract.EOFContainer = nil
	}
	if contract.EOFContainer != nil {
		callContext.ReturnStack = stack.NewReturnStack()
		defer stack.ReturnRStack(callContext.ReturnStack)
		// Set code to the first code section
		if section := contract.EOFContainer.GetCodeSection(0); section != nil {
			contract.Code = section
			contract.CodeSection = 0
		}
	}

	var (
		// For optimisation reason we're using uint64 as the program counter.
		// It's theoretically possible to go above 2^64. The YP defines the PC
		// to be uint256. Practically much less so feasible.
		cost uint64
		// copies used by tracer
		pcCopy  uint64 // needed for the deferred Tracer
		gasCopy uint64 // for Tracer to log gas remaining before execution
		logged  bool   // deferred Tracer should ignore already logged steps
		res     []byte // result of the opcode execution function
	)
	// Don't move this deferrred function, it's placed before the capturestate-deferred method,
	// so that it get's executed _after_: the capturestate needs the stacks before
	// they are returned to the pools
	contract.Input = input

	if in.cfg.Debug {
		defer func() {
			if err != nil {
				if !logged {
					in.cfg.Tracer.CaptureState(pcCopy, op, gasCopy, cost, callContext, in.returnData, in.depth, err) //nolint:errcheck
				} else {
					in.cfg.Tracer.CaptureFault(pcCopy, op, gasCopy, cost, callContext, in.depth, err)
				}
			}
		}()
	}
	// The Interpreter main run loop (contextual). This loop runs until either an
	// explicit STOP, RETURN or SELFDESTRUCT is executed, an error occurred during
	// the execution of one of the operations or until the done flag is set by the
	// parent context.
	// Hoist the two loop invariants the dispatch reads on every single opcode.
	// in.cfg.Debug lives behind the embedded *VM, so re-reading it three times
	// per iteration is three dependent loads that a local turns into a
	// register; meta is the flat dispatch table (jump_meta.go).
	debug := in.cfg.Debug
	meta := in.meta
	if meta == nil {
		// An interpreter assembled literally rather than through
		// NewEVMInterpreter (tests, embedders) still has a jump table; derive
		// the flat view once rather than making meta a constructor obligation.
		meta = opMetaFor(in.jt)
		in.meta = meta
	}
	// code is the byte stream the loop fetches opcodes from: the fused
	// execution view when allowed, otherwise the plain code with the synthetic
	// opcode values neutralised (see fuse.go). EOF code switches sections
	// mid-run (CALLF/JUMPF), so it is re-read from the contract every step;
	// EOF validation already rejects undefined opcodes, so no view is needed.
	code := contract.Code
	eof := contract.EOFContainer != nil
	if !eof {
		if canFuse(contract, debug) {
			code = execView(contract)
		} else {
			code = plainView(code)
		}
	}
	cancelCheck := 1000
	for {
		cancelCheck--
		if cancelCheck == 0 {
			cancelCheck = 1000
			if in.evm.Cancelled() {
				break
			}
		}
		if debug {
			// Capture pre-execution values for tracing.
			logged, pcCopy, gasCopy = false, *pc, contract.Gas
		}
		// Get the operation from the jump table and validate the stack to ensure there are
		// enough stack items available to perform the operation.
		if eof {
			code = contract.Code
		}
		if *pc < uint64(len(code)) {
			op = OpCode(code[*pc])
		} else {
			op = STOP
		}
		if op == JUMPDEST && !debug {
			// 7% of all executed opcodes are JUMPDEST (every taken jump lands
			// on one). It pops and pushes nothing and only charges its gas, so
			// it never needs the metadata record or the dispatch call.
			if !contract.UseGas(params.JumpdestGas) {
				return nil, ErrOutOfGas
			}
			*pc++
			continue
		}
		operation := &meta[op]
		cost = operation.constantGas // For tracing
		// Validate stack
		if sLen := locStack.Len(); sLen < operation.numPop {
			return nil, &ErrStackUnderflow{stackLen: sLen, required: operation.numPop}
		} else if sLen > operation.maxStack {
			return nil, &ErrStackOverflow{stackLen: sLen, limit: operation.maxStack}
		}
		if !contract.UseGas(cost) {
			return nil, ErrOutOfGas
		}
		if operation.dynamicGas != nil {
			// All ops with a dynamic memory usage also has a dynamic gas cost.
			var memorySize uint64
			// calculate the new memory size and expand the memory to fit
			// the operation
			// Memory check needs to be done prior to evaluating the dynamic gas portion,
			// to detect calculation overflows
			if operation.memorySize != nil {
				memSize, overflow := operation.memorySize(locStack)
				if overflow {
					return nil, ErrGasUintOverflow
				}
				// memory is expanded in words of 32 bytes. Gas
				// is also calculated in words.
				if memorySize, overflow = math.SafeMul(ToWordSize(memSize), 32); overflow {
					return nil, ErrGasUintOverflow
				}
			}
			// Consume the gas and return an error if not enough gas is available.
			// cost is explicitly set so that the capture state defer method can get the proper cost
			var dynamicCost uint64
			dynamicCost, err = operation.dynamicGas(in.evm, contract, locStack, mem, memorySize)
			cost += dynamicCost // for tracing
			if err != nil || !contract.UseGas(dynamicCost) {
				return nil, ErrOutOfGas
			}
			if memorySize > 0 {
				mem.Resize(memorySize)
			}
		}
		if debug {
			in.cfg.Tracer.CaptureState(*pc, op, gasCopy, cost, callContext, in.returnData, in.depth, err) //nolint:errcheck
			logged = true
		}
		// execute the operation. The hottest opcodes — together about 85% of
		// everything executed — are handled inline: the compiler turns this
		// dense switch into a jump table, so they skip the indirect call, the
		// closure context load and the callee prologue. Their bodies mirror
		// the table functions exactly; stack depth was validated above.
		// res must not survive from an earlier table op: an inline op that
		// fails ends the frame, and the loop returns res as its return data.
		res = nil
		switch op {
		case PUSH1:
			v := uint64(0)
			if idx := *pc + 1; idx < uint64(len(code)) {
				v = uint64(code[idx])
			}
			locStack.PushSlot().SetUint64(v)
			*pc++
		case PUSH2, PUSH3, PUSH4, PUSH5, PUSH6, PUSH7, PUSH8:
			n := int(op - PUSH0)
			start := int(*pc + 1)
			var v uint64
			if start+n <= len(code) {
				for _, b := range code[start : start+n] {
					v = v<<8 | uint64(b)
				}
			} else {
				for i := 0; i < n; i++ {
					v <<= 8
					if j := start + i; j < len(code) {
						v |= uint64(code[j])
					}
				}
			}
			locStack.PushSlot().SetUint64(v)
			*pc += uint64(n)
		case DUP1, DUP2, DUP3, DUP4, DUP5, DUP6, DUP7, DUP8, DUP9, DUP10, DUP11, DUP12, DUP13, DUP14, DUP15, DUP16:
			locStack.DupUnchecked(int(op-DUP1) + 1)
		case SWAP1, SWAP2, SWAP3, SWAP4, SWAP5, SWAP6, SWAP7, SWAP8, SWAP9, SWAP10, SWAP11, SWAP12, SWAP13, SWAP14, SWAP15, SWAP16:
			locStack.SwapUnchecked(int(op-SWAP1) + 2)
		case POP:
			locStack.PopDiscard()
		case ADD:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			y.Add(x, y)
		case SUB:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			y.Sub(x, y)
		case MUL:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			y.Mul(x, y)
		case AND:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			y.And(x, y)
		case OR:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			y.Or(x, y)
		case XOR:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			y.Xor(x, y)
		case NOT:
			x := locStack.PeekUnchecked()
			x.Not(x)
		case ISZERO:
			x := locStack.PeekUnchecked()
			if x.IsZero() {
				x.SetOne()
			} else {
				x.Clear()
			}
		case EQ:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			if x.Eq(y) {
				y.SetOne()
			} else {
				y.Clear()
			}
		case LT:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			if x.Lt(y) {
				y.SetOne()
			} else {
				y.Clear()
			}
		case GT:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			if x.Gt(y) {
				y.SetOne()
			} else {
				y.Clear()
			}
		case SLT:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			if x.Slt(y) {
				y.SetOne()
			} else {
				y.Clear()
			}
		case SGT:
			x, y := locStack.PopPtrUnchecked(), locStack.PeekUnchecked()
			if x.Sgt(y) {
				y.SetOne()
			} else {
				y.Clear()
			}
		case MLOAD:
			v := locStack.PeekUnchecked()
			v.SetBytes(mem.GetPtr(int64(v.Uint64()), 32))
		case MSTORE:
			mStart, val := locStack.PopPtrUnchecked(), locStack.PopPtrUnchecked()
			err = mem.Set32(mStart.Uint64(), val)
		case JUMP:
			pos := locStack.PopPtrUnchecked()
			if valid, usedBitmap := contract.validJumpdest(pos); !valid {
				if usedBitmap {
					logInvalidJumpBitmap(in)
				}
				err = ErrInvalidJump
				break
			}
			*pc = pos.Uint64() - 1 // pc is incremented below
		case JUMPI:
			pos, cond := locStack.PopPtrUnchecked(), locStack.PopPtrUnchecked()
			if !cond.IsZero() {
				if valid, usedBitmap := contract.validJumpdest(pos); !valid {
					if usedBitmap {
						logInvalidJumpBitmap(in)
					}
					err = ErrInvalidJump
					break
				}
				*pc = pos.Uint64() - 1
			}
		case fusedPush1Jump:
			err = fusedJump(pc, contract, uint64(code[*pc+1]))
		case fusedPush2Jump:
			err = fusedJump(pc, contract, uint64(code[*pc+1])<<8|uint64(code[*pc+2]))
		case fusedPush1Jumpi:
			if cond := locStack.PopPtrUnchecked(); !cond.IsZero() {
				err = fusedJump(pc, contract, uint64(code[*pc+1]))
			} else {
				*pc += 2
			}
		case fusedPush2Jumpi:
			if cond := locStack.PopPtrUnchecked(); !cond.IsZero() {
				err = fusedJump(pc, contract, uint64(code[*pc+1])<<8|uint64(code[*pc+2]))
			} else {
				*pc += 3
			}
		default:
			res, err = operation.execute(pc, in, callContext)
		}

		if err != nil {
			break
		}
		*pc++
	}

	if err == errStopToken {
		err = nil // clear stop token error
	}

	ret = append(ret, res...)
	return
}

// Depth returns the current call stack depth.
func (in *EVMInterpreter) Depth() int {
	return in.depth
}

func (vm *VM) disableReadonly() { vm.readOnly = false }
func (vm *VM) noop()            {}

func (vm *VM) setReadonly(outerReadonly bool) func() {
	if outerReadonly && !vm.readOnly {
		vm.readOnly = true
		return func() {
			vm.readOnly = false
		}
	}
	return func() {}
}

func (vm *VM) getReadonly() bool {
	return vm.readOnly
}
