//go:build vmstephook

package vm

import (
	"errors"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// sameFrameOutcome: return data and gas left must match; an error may differ
// in kind (the block precheck can report out-of-gas where the per-opcode path
// reports a stack fault, or vice versa) but never in whether the frame failed
// or whether it was a REVERT (which keeps the remaining gas).
func sameFrameOutcome(retA []byte, gasA uint64, errA error, retB []byte, gasB uint64, errB error) bool {
	if string(retA) != string(retB) || gasA != gasB || (errA == nil) != (errB == nil) {
		return false
	}
	return errors.Is(errA, ErrExecutionReverted) == errors.Is(errB, ErrExecutionReverted)
}

// sweepRunner executes one contract repeatedly on one EVM/state (state
// carries over between runs identically in both modes).
type sweepRunner struct {
	cache  *CodeAnalysisCache
	evm    *EVM
	caller types.Address
	target types.Address
}

func newSweepRunner(t *testing.T, precheck bool, code []byte) *sweepRunner {
	t.Helper()
	r := &sweepRunner{
		caller: types.HexToAddress("0x7000000000000000000000000000000000000007"),
		target: types.HexToAddress("0x8000000000000000000000000000000000000008"),
	}
	if precheck {
		r.cache = NewCodeAnalysisCache(16)
	}
	var ibs *state.IntraBlockState
	r.evm, ibs = newCreateTestEVM(t, testParisCreateChainConfig())
	ibs.CreateAccount(r.caller, false)
	ibs.CreateAccount(r.target, true)
	ibs.SetCode(r.target, code)
	return r
}

type stepRecord struct {
	depth int
	pc    uint64
	op    OpCode
}

// run executes once and returns the outcome plus the opcode stream with the
// control-flow opcodes removed (the fused view executes those differently by
// design; everything else — every state access in particular — must match).
func (r *sweepRunner) run(gas uint64) ([]byte, uint64, error, []stepRecord) {
	saved := GlobalCodeAnalysisCache
	GlobalCodeAnalysisCache = r.cache
	var steps []stepRecord
	testStepHook = func(depth int, pc uint64, op OpCode) {
		switch op {
		case JUMPDEST, JUMP, JUMPI, PUSH1, PUSH2, fusedPush1Jump, fusedPush1Jumpi, fusedPush2Jump, fusedPush2Jumpi:
			return
		}
		steps = append(steps, stepRecord{depth, pc, op})
	}
	defer func() { GlobalCodeAnalysisCache = saved; testStepHook = nil }()
	ibs := r.evm.IntraBlockState().(*state.IntraBlockState)
	ibs.PrepareAccessList(r.caller, nil, ActivePrecompiles(r.evm.ChainRules()), nil)
	ret, left, err := r.evm.Call(AccountRef(r.caller), r.target, nil, gas, uint256.NewInt(0), false)
	return ret, left, err, steps
}

func sweep(t *testing.T, name string, code []byte, gases []uint64) {
	t.Helper()
	pre, seq := newSweepRunner(t, true, code), newSweepRunner(t, false, code)
	for _, gas := range gases {
		retF, gasF, errF, stepsF := pre.run(gas)
		retP, gasP, errP, stepsP := seq.run(gas)
		if !sameFrameOutcome(retF, gasF, errF, retP, gasP, errP) {
			t.Fatalf("%s gas=%d: precheck (%x, %d, %v) sequential (%x, %d, %v)", name, gas, retF, gasF, errF, retP, gasP, errP)
		}
		if len(stepsF) != len(stepsP) {
			t.Fatalf("%s gas=%d: precheck executed %d opcodes, sequential %d (%v / %v)", name, gas, len(stepsF), len(stepsP), errF, errP)
		}
		for i := range stepsF {
			if stepsF[i] != stepsP[i] {
				t.Fatalf("%s gas=%d: step %d differs: precheck %+v sequential %+v", name, gas, i, stepsF[i], stepsP[i])
			}
		}
	}
}

func rangeGas(lo, hi, step uint64) []uint64 {
	var out []uint64
	for g := lo; g <= hi; g += step {
		out = append(out, g)
	}
	return out
}

func TestBlockPrecheckGasObservers(t *testing.T) {
	ret32 := []byte{byte(PUSH1), 0, byte(MSTORE), byte(PUSH1), 32, byte(PUSH1), 0, byte(RETURN)}
	// GAS in the middle of a block: the value must be the sequential one.
	sweep(t, "gas", append([]byte{byte(PUSH1), 7, byte(POP), byte(GAS)}, ret32...), rangeGas(0, 60, 1))
	// CALL to the identity precompile with all remaining gas (63/64 rule).
	callAll := []byte{byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 4, byte(GAS), byte(CALL)}
	sweep(t, "call-all", append(append(callAll, byte(POP), byte(GAS)), ret32...), append(rangeGas(0, 1200, 1), 5000, 100000))
	// CALL with a fixed gas argument larger than what is available.
	callFixed := []byte{byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 4, byte(PUSH2), 0x01, 0x00, byte(CALL)}
	sweep(t, "call-fixed", append(append(callFixed, byte(POP), byte(GAS)), ret32...), append(rangeGas(0, 1200, 1), 100000))
	// SSTORE: the EIP-2200 sentry reads the remaining gas.
	sstore := []byte{byte(PUSH1), 1, byte(PUSH1), 0, byte(SSTORE), byte(PUSH1), 2, byte(PUSH1), 1, byte(SSTORE), byte(GAS)}
	sweep(t, "sstore", append(sstore, ret32...), append(append(rangeGas(2280, 2330, 1), rangeGas(22090, 22130, 1)...), rangeGas(24380, 24440, 1)...))
	// CREATE of an empty contract: 63/64 of the corrected remaining gas.
	create := []byte{byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(CREATE), byte(POP), byte(GAS)}
	sweep(t, "create", append(create, ret32...), append(rangeGas(31990, 32100, 1), rangeGas(40000, 40200, 7)...))
}

func TestBlockPrecheckStackAndFlow(t *testing.T) {
	// Overflow deep inside a block.
	over := make([]byte, 0, 2100)
	for i := 0; i < 1030; i++ {
		over = append(over, byte(PUSH1), 1)
	}
	over = append(over, byte(STOP))
	sweep(t, "overflow", over, []uint64{0, 3000, 3080, 3090, 100000})
	// Underflow after some work; fall-through into a JUMPDEST; dead code
	// after a terminator; JUMPI not taken into a new block; truncated PUSH.
	flow := []byte{
		byte(PUSH1), 0, byte(PUSH2), 0, 9, byte(JUMPI), // not taken → fall block at 6
		byte(PUSH1), 5, byte(ADD), // 6: underflows (only one item)
		byte(JUMPDEST), byte(STOP), // 9
	}
	sweep(t, "underflow", flow, rangeGas(0, 40, 1))
	flow2 := []byte{
		byte(PUSH1), 1, byte(PUSH1), 2, byte(ADD), byte(JUMPDEST), byte(POP), // fall-through into JUMPDEST
		byte(PUSH1), 1, byte(PUSH2), 0, 15, byte(JUMPI), byte(INVALID), byte(INVALID), // taken
		byte(JUMPDEST), byte(PUSH1), 0, byte(DUP1), byte(RETURN), // 15
		byte(ADD), byte(ADD), byte(ADD), // dead
		byte(PUSH2), 1, // truncated
	}
	sweep(t, "flow", flow2, rangeGas(0, 60, 1))
	// REVERT keeps gas: the outcome must be an exact REVERT in both modes.
	rev := []byte{byte(PUSH1), 1, byte(PUSH1), 0, byte(MSTORE8), byte(PUSH1), 1, byte(PUSH1), 0, byte(REVERT)}
	sweep(t, "revert", rev, rangeGas(0, 40, 1))
}

// TestBlockPrecheckRandomPrograms: seeded random programs from a small
// opcode set (with loops), every gas value in a range, both paths agree.
func TestBlockPrecheckRandomPrograms(t *testing.T) {
	rng := rand.New(rand.NewSource(20260827))
	for prog := 0; prog < 300; prog++ {
		n := 4 + rng.Intn(24)
		var code []byte
		var jumpdests []int
		for i := 0; i < n; i++ {
			switch rng.Intn(20) {
			case 0, 1, 2:
				code = append(code, byte(PUSH1), byte(rng.Intn(64)))
			case 3:
				code = append(code, byte(DUP1))
			case 4:
				code = append(code, byte(SWAP1))
			case 5:
				code = append(code, byte(ADD))
			case 6:
				code = append(code, byte(POP))
			case 7:
				code = append(code, byte(PUSH1), byte(rng.Intn(96)), byte(MLOAD))
			case 8:
				code = append(code, byte(PUSH1), byte(rng.Intn(96)), byte(MSTORE))
			case 9:
				jumpdests = append(jumpdests, len(code))
				code = append(code, byte(JUMPDEST))
			case 10:
				code = append(code, byte(GAS))
			case 11:
				code = append(code, byte(ISZERO))
			case 12:
				code = append(code, byte(PUSH1), 0, byte(SLOAD))
			case 13:
				code = append(code, byte(PUSH1), byte(rng.Intn(3)), byte(PUSH1), 0, byte(SSTORE))
			case 14:
				code = append(code, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 0, byte(PUSH1), 4, byte(GAS), byte(CALL))
			case 15, 16:
				// static or dynamic jump to a JUMPDEST placed earlier (loop) or a
				// placeholder patched below
				code = append(code, byte(PUSH2), 0xff, 0xff)
				if rng.Intn(2) == 0 {
					code = append(code, byte(JUMPI))
				} else {
					code = append(code, byte(JUMP))
				}
			case 17:
				code = append(code, byte(PUSH1), 0, byte(DUP1), byte(RETURN))
			case 18:
				code = append(code, byte(PUSH1), 0, byte(DUP1), byte(REVERT))
			case 19:
				code = append(code, byte(STOP))
			}
		}
		code = append(code, byte(JUMPDEST), byte(STOP))
		jumpdests = append(jumpdests, len(code)-2)
		// patch jump targets: half to a random JUMPDEST, others left invalid
		for i := 0; i+3 < len(code); i++ {
			if code[i] == byte(PUSH2) && code[i+1] == 0xff && code[i+2] == 0xff && (code[i+3] == byte(JUMP) || code[i+3] == byte(JUMPI)) {
				if rng.Intn(4) != 0 {
					d := jumpdests[rng.Intn(len(jumpdests))]
					code[i+1], code[i+2] = byte(d>>8), byte(d)
				}
				i += 3
			}
		}
		gases := rangeGas(0, 120, 1)
		gases = append(gases, 500, 3000, 25000, 60000)
		sweep(t, "random", code, gases)
	}
}
