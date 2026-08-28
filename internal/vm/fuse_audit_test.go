package vm

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// opRecorder is an EVMLogger that only remembers the opcodes it was shown.
type opRecorder struct {
	states []OpCode
	faults []OpCode
}

func (r *opRecorder) CaptureTxStart(uint64) {}
func (r *opRecorder) CaptureTxEnd(uint64)   {}
func (r *opRecorder) CaptureStart(VMInterface, types.Address, types.Address, bool, []byte, uint64, *uint256.Int) {
}
func (r *opRecorder) CaptureEnd([]byte, uint64, error) {}
func (r *opRecorder) CaptureEnter(OpCode, types.Address, types.Address, []byte, uint64, *uint256.Int) {
}
func (r *opRecorder) CaptureExit([]byte, uint64, error) {}
func (r *opRecorder) CaptureState(pc uint64, op OpCode, gas, cost uint64, scope *ScopeContext, rData []byte, depth int, err error) {
	r.states = append(r.states, op)
}
func (r *opRecorder) CaptureFault(pc uint64, op OpCode, gas, cost uint64, scope *ScopeContext, depth int, err error) {
	r.faults = append(r.faults, op)
}

// forkConfigs returns chain configs spanning the jump tables whose PUSH/JUMP
// family the fused view must reproduce.
func forkConfigs() map[string]*params.ChainConfig {
	z := big.NewInt(0)
	frontier := &params.ChainConfig{ChainID: big.NewInt(1)}
	homestead := &params.ChainConfig{ChainID: big.NewInt(1), HomesteadBlock: z}
	byzantium := &params.ChainConfig{ChainID: big.NewInt(1), HomesteadBlock: z, TangerineWhistleBlock: z, SpuriousDragonBlock: z, ByzantiumBlock: z}
	berlin := &params.ChainConfig{ChainID: big.NewInt(1), HomesteadBlock: z, TangerineWhistleBlock: z, SpuriousDragonBlock: z, ByzantiumBlock: z,
		ConstantinopleBlock: z, PetersburgBlock: z, IstanbulBlock: z, BerlinBlock: z}
	shanghai := testParisCreateChainConfig()
	shanghai.ShanghaiTime = new(big.Int)
	cancun := testParisCreateChainConfig()
	cancun.ShanghaiTime = new(big.Int)
	cancun.CancunTime = new(big.Int)
	return map[string]*params.ChainConfig{
		"frontier": frontier, "homestead": homestead, "byzantium": byzantium,
		"berlin": berlin, "paris": testParisCreateChainConfig(), "shanghai": shanghai, "cancun": cancun,
	}
}

func newAuditEVM(t *testing.T, chainCfg *params.ChainConfig, cfg Config) (*EVM, *state.IntraBlockState) {
	t.Helper()
	db := memdb.NewTestDB(t)
	tx := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(tx, 1))
	blockCtx := evmtypes.BlockContext{
		CanTransfer: func(evmtypes.IntraBlockState, types.Address, *uint256.Int) bool { return true },
		Transfer:    func(evmtypes.IntraBlockState, types.Address, types.Address, *uint256.Int, bool) {},
		GetHash:     func(uint64) types.Hash { return types.Hash{} },
		GasLimit:    1_000_000,
		BlockNumber: 1,
		Time:        1,
		Difficulty:  big.NewInt(0),
		BaseFee:     uint256.NewInt(0),
	}
	return NewEVM(blockCtx, evmtypes.TxContext{}, ibs, chainCfg, cfg), ibs
}

type runOutcome struct {
	ret []byte
	gas uint64
	err error
}

func (o runOutcome) String() string {
	if o.err != nil {
		return fmt.Sprintf("{ret:%x gas:%d err:%q}", o.ret, o.gas, o.err.Error())
	}
	return fmt.Sprintf("{ret:%x gas:%d err:nil}", o.ret, o.gas)
}

// runCode executes code as a contract at a fixed address under the given
// cache / vm config and returns the outcome.
func runCode(t *testing.T, chainCfg *params.ChainConfig, vmCfg Config, cache *CodeAnalysisCache, code []byte, gas uint64) runOutcome {
	t.Helper()
	saved := GlobalCodeAnalysisCache
	GlobalCodeAnalysisCache = cache
	defer func() { GlobalCodeAnalysisCache = saved }()
	evm, ibs := newAuditEVM(t, chainCfg, vmCfg)
	caller := types.HexToAddress("0x7000000000000000000000000000000000000007")
	target := types.HexToAddress("0x8000000000000000000000000000000000000008")
	ibs.CreateAccount(caller, false)
	ibs.CreateAccount(target, true)
	ibs.SetCode(target, code)
	ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)
	ret, left, err := evm.Call(AccountRef(caller), target, nil, gas, uint256.NewInt(0), false)
	return runOutcome{ret: ret, gas: left, err: err}
}

func outcomesEqual(a, b runOutcome) bool {
	if !bytes.Equal(a.ret, b.ret) || a.gas != b.gas || (a.err == nil) != (b.err == nil) {
		return false
	}
	if a.err == nil {
		return true
	}
	return a.err.Error() == b.err.Error()
}

// TestTracerSeesRealUndefinedOpcode: with a tracer attached the opcode
// reported to CaptureState/CaptureFault for a real 0x0c-0x0f byte must be
// that byte, exactly as before the execution view existed.
func TestTracerSeesRealUndefinedOpcode(t *testing.T) {
	for _, raw := range []byte{0x0c, 0x0d, 0x0e, 0x0f, 0x21} {
		code := []byte{byte(PUSH1), 1, raw}
		rec := &opRecorder{}
		out := runCode(t, testParisCreateChainConfig(), Config{Debug: true, Tracer: rec}, nil, code, 1000)
		var inv *ErrInvalidOpCode
		if !errors.As(out.err, &inv) {
			t.Fatalf("byte %#x: err %v", raw, out.err)
		}
		if out.err.Error() != (&ErrInvalidOpCode{opcode: OpCode(raw)}).Error() {
			t.Fatalf("byte %#x: error %q", raw, out.err)
		}
		if len(rec.states) != 2 || rec.states[0] != PUSH1 || rec.states[1] != OpCode(raw) {
			t.Fatalf("byte %#x: CaptureState ops %v", raw, rec.states)
		}
		if len(rec.faults) != 1 || rec.faults[0] != OpCode(raw) {
			t.Fatalf("byte %#x: CaptureFault ops %v", raw, rec.faults)
		}
	}
}

// TestFusedJumpiUnderflowErrorMatchesPlain: a PUSH+JUMPI pair that
// underflows must report the same error text whether or not it was fused —
// eth_call surfaces the message to clients.
func TestFusedJumpiUnderflowErrorMatchesPlain(t *testing.T) {
	cfg := testParisCreateChainConfig()
	for _, code := range [][]byte{
		{byte(PUSH1), 3, byte(JUMPI), byte(JUMPDEST)},
		{byte(PUSH2), 0, 4, byte(JUMPI), byte(JUMPDEST)},
		{byte(PUSH1), 3, byte(JUMP), byte(JUMPDEST), byte(PUSH1), 3, byte(JUMPI)}, // fused JUMP then fused JUMPI underflow
	} {
		fused := runCode(t, cfg, Config{}, NewCodeAnalysisCache(16), code, 1000)
		plain := runCode(t, cfg, Config{}, nil, code, 1000)
		traced := runCode(t, cfg, Config{Debug: true, Tracer: &opRecorder{}}, nil, code, 1000)
		if !outcomesEqual(fused, plain) || !outcomesEqual(fused, traced) {
			t.Fatalf("code %x: fused %v plain %v traced %v", code, fused, plain, traced)
		}
	}
}

// TestFusionDisabledUnderSkipAnalysis: with SkipAnalysis a JUMP may land
// inside push data and execution then reads code out of phase. A PUSH whose
// immediate happens to sit on a fused position would read the synthetic byte
// instead of the real one, so the view must not be used in that mode.
func TestFusionDisabledUnderSkipAnalysis(t *testing.T) {
	code := make([]byte, 130)
	copy(code, []byte{
		byte(PUSH1), 5, // 0: dest 5 is inside push data: never fused
		byte(JUMP),              // 2
		byte(JUMPDEST),          // 3: filler
		byte(PUSH2), 0x5b, 0x60, // 4: data bytes: 5 = JUMPDEST, 6 = PUSH1
		byte(PUSH1), 0x80, // 7: fused with the JUMPI at 9 (0x80 is a JUMPDEST)
		byte(JUMPI), // 9
	})
	// out of phase from 5: JUMPDEST, PUSH1 <byte 7>, DUP1 (0x80), JUMPI -> jumps to 0x60 if byte 7 is
	// still PUSH1 (0x60), or to 0x0d if it was replaced by the synthetic opcode.
	code[0x60] = byte(JUMPDEST)
	copy(code[0x61:], []byte{byte(PUSH1), 32, byte(PUSH1), 0, byte(RETURN)})
	code[0x80] = byte(JUMPDEST)
	cfg := testParisCreateChainConfig()
	plain := runCode(t, cfg, Config{SkipAnalysis: true}, nil, code, 100000)
	if plain.err != nil || len(plain.ret) != 32 {
		t.Fatalf("plain: %v", plain)
	}
	fused := runCode(t, cfg, Config{SkipAnalysis: true}, NewCodeAnalysisCache(16), code, 100000)
	if !outcomesEqual(fused, plain) {
		t.Fatalf("skipAnalysis: fused %v plain %v", fused, plain)
	}
}

// opAlphabet is the instruction mix the differential test draws from: the
// fused/inlined family plus everything that observes pc, code bytes and the
// stack limit.
var opAlphabet = []OpCode{
	PUSH1, PUSH1, PUSH1, PUSH2, PUSH2, PUSH3, PUSH8, PUSH9, PUSH32,
	JUMP, JUMP, JUMPI, JUMPI, JUMPDEST, JUMPDEST, JUMPDEST,
	DUP1, DUP2, DUP16, SWAP1, SWAP2, SWAP16, POP,
	ADD, SUB, MUL, AND, OR, XOR, NOT, ISZERO, EQ, LT, GT, SLT, SGT,
	MLOAD, MSTORE, MSTORE8, PC, MSIZE, GAS,
	CODESIZE, CODECOPY, KECCAK256, RETURN, REVERT, STOP, INVALID,
	0x0c, 0x0d, 0x0e, 0x0f, 0x21, PUSH0, SHL, SHR, SAR, RETURNDATASIZE, CALLDATALOAD, ADDRESS, CALLER,
}

// randomCode builds a program biased towards static jumps to valid JUMPDESTs.
func randomCode(rnd *rand.Rand, n int) []byte {
	code := make([]byte, 0, n+40)
	for len(code) < n {
		op := opAlphabet[rnd.Intn(len(opAlphabet))]
		switch {
		case op >= PUSH1 && op <= PUSH32:
			k := int(op - PUSH0)
			code = append(code, byte(op))
			for i := 0; i < k; i++ {
				switch rnd.Intn(4) {
				case 0:
					code = append(code, byte(JUMPDEST)) // JUMPDEST inside push data
				case 1:
					code = append(code, byte(0x0c+rnd.Intn(4))) // synthetic values inside push data
				default:
					code = append(code, byte(rnd.Intn(n+8)))
				}
			}
		case op == JUMP || op == JUMPI:
			// half the jumps are static: PUSH1/PUSH2 of a position that will be a JUMPDEST
			if rnd.Intn(2) == 0 {
				dest := rnd.Intn(n + 8)
				if rnd.Intn(2) == 0 {
					code = append(code, byte(PUSH1), byte(dest))
				} else {
					code = append(code, byte(PUSH2), byte(dest>>8), byte(dest))
				}
			}
			code = append(code, byte(op))
		default:
			code = append(code, byte(op))
		}
	}
	// sprinkle JUMPDESTs at code positions so that static jumps have targets
	for i := 0; i < len(code); i++ {
		op := OpCode(code[i])
		if op >= PUSH1 && op <= PUSH32 {
			i += int(op - PUSH0)
			continue
		}
		if rnd.Intn(3) == 0 {
			code[i] = byte(JUMPDEST)
		}
	}
	if rnd.Intn(4) == 0 { // truncated trailing push
		code = append(code, byte(PUSH2), 0)
	}
	return code
}

// TestFusedExecutionDifferential runs random programs through the fused
// view, the plain interpreter and the traced interpreter under several forks
// and requires byte-identical results, gas and error text.
func TestFusedExecutionDifferential(t *testing.T) {
	iters := 300
	if testing.Short() {
		iters = 60
	}
	for name, cfg := range forkConfigs() {
		t.Run(name, func(t *testing.T) {
			rnd := rand.New(rand.NewSource(int64(len(name)) * 7919))
			for i := 0; i < iters; i++ {
				code := randomCode(rnd, 16+rnd.Intn(120))
				gas := uint64(rnd.Intn(4000))
				if rnd.Intn(3) == 0 {
					gas = 200000
				}
				fused := runCode(t, cfg, Config{}, NewCodeAnalysisCache(16), code, gas)
				plain := runCode(t, cfg, Config{}, nil, code, gas)
				traced := runCode(t, cfg, Config{Debug: true, Tracer: &opRecorder{}}, nil, code, gas)
				if !outcomesEqual(fused, plain) {
					t.Fatalf("iter %d code %x gas %d:\n fused %v\n plain %v", i, code, gas, fused, plain)
				}
				if !outcomesEqual(fused, traced) {
					t.Fatalf("iter %d code %x gas %d:\n fused  %v\n traced %v", i, code, gas, fused, traced)
				}
			}
		})
	}
}

// TestFusedStackLimitEdge: PUSH+JUMP at exactly 1023 and 1024 stack items.
func TestFusedStackLimitEdge(t *testing.T) {
	cfg := testParisCreateChainConfig()
	for _, depth := range []int{1022, 1023, 1024} {
		// 0: PUSH1 5 JUMP (fused) | 3: JUMPDEST STOP | 5: JUMPDEST PUSH1 1 DUP1... PUSH1 3 JUMP (fused)
		code := []byte{byte(PUSH1), 5, byte(JUMP), byte(JUMPDEST), byte(STOP), byte(JUMPDEST), byte(PUSH1), 1}
		for i := 1; i < depth; i++ {
			code = append(code, byte(DUP1))
		}
		code = append(code, byte(PUSH1), 3, byte(JUMP))
		fused := runCode(t, cfg, Config{}, NewCodeAnalysisCache(16), code, 100000)
		plain := runCode(t, cfg, Config{}, nil, code, 100000)
		if !outcomesEqual(fused, plain) {
			t.Fatalf("depth %d: fused %v plain %v", depth, fused, plain)
		}
	}
	// PUSH2 variant with a big stack: build stack with DUP1 to keep code short
	for _, depth := range []int{1023, 1024} {
		code := []byte{byte(PUSH1), 1}
		for i := 1; i < depth; i++ {
			code = append(code, byte(DUP1))
		}
		dest := len(code) + 4
		code = append(code, byte(PUSH2), byte(dest>>8), byte(dest), byte(JUMPI), byte(JUMPDEST), byte(STOP))
		fused := runCode(t, cfg, Config{}, NewCodeAnalysisCache(16), code, 100000)
		plain := runCode(t, cfg, Config{}, nil, code, 100000)
		if !outcomesEqual(fused, plain) {
			t.Fatalf("depth %d jumpi: fused %v plain %v", depth, fused, plain)
		}
	}
}
