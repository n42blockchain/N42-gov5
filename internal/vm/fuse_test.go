package vm

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func TestFuseCodeRewritesOnlyValidStaticJumps(t *testing.T) {
	code := []byte{
		byte(PUSH1), 1, // 0: cond
		byte(PUSH2), 0, 9, byte(JUMPI), // 2: fusable (dest 9 is a JUMPDEST)
		byte(PUSH1), 0, byte(JUMP), // 6: NOT fusable: dest 0 is PUSH1
		byte(JUMPDEST),              // 9
		byte(PUSH1), 13, byte(JUMP), // 10: NOT fusable: 13 is inside push data
		byte(PUSH2), byte(JUMPDEST), 0x0c, // 13: push data contains JUMPDEST and 0x0c
		0x0c,                        // 16: real undefined opcode at a code position
		byte(PUSH1), 9, byte(JUMPI), // 17: fusable
		byte(PUSH1), 9, byte(JUMP), // 20: fusable
		byte(PUSH3), 0, 0, 9, byte(JUMP), // 23: PUSH3 is never fused
		byte(PUSH2), 0, // 28: truncated push at the end
	}
	fused := fuseCode(code, codeBitmap(code))
	if &fused[0] == &code[0] {
		t.Fatal("expected a rewritten copy")
	}
	want := append([]byte{}, code...)
	want[2] = byte(fusedPush2Jumpi)
	want[16] = byte(fusedCollision)
	want[17] = byte(fusedPush1Jumpi)
	want[20] = byte(fusedPush1Jump)
	for i := range want {
		if fused[i] != want[i] {
			t.Fatalf("byte %d: fused %#x want %#x", i, fused[i], want[i])
		}
	}
	plain := []byte{byte(PUSH1), 0, byte(MSTORE), byte(STOP)}
	if got := fuseCode(plain, codeBitmap(plain)); &got[0] != &plain[0] {
		t.Fatal("unfusable code must be returned as is")
	}
}

// runFusedAndPlain executes code once through the execution view and once
// without it and returns both (ret, gasLeft, err) triples.
func runFusedAndPlain(t *testing.T, code []byte, gas uint64) (retF []byte, gasF uint64, errF error, retP []byte, gasP uint64, errP error) {
	t.Helper()
	run := func(cache *CodeAnalysisCache) ([]byte, uint64, error) {
		saved := GlobalCodeAnalysisCache
		GlobalCodeAnalysisCache = cache
		defer func() { GlobalCodeAnalysisCache = saved }()
		evm, ibs := newCreateTestEVM(t, testParisCreateChainConfig())
		caller := types.HexToAddress("0x7000000000000000000000000000000000000007")
		target := types.HexToAddress("0x8000000000000000000000000000000000000008")
		ibs.CreateAccount(caller, false)
		ibs.CreateAccount(target, true)
		ibs.SetCode(target, code)
		ibs.PrepareAccessList(caller, nil, ActivePrecompiles(evm.ChainRules()), nil)
		return evm.Call(AccountRef(caller), target, nil, gas, uint256.NewInt(0), false)
	}
	retF, gasF, errF = run(NewCodeAnalysisCache(16))
	retP, gasP, errP = run(nil)
	return
}

// sameOutcome compares two executions; stack underflow errors may differ in
// their counts because the fused op validates one combined stack requirement.
func sameOutcome(retF []byte, gasF uint64, errF error, retP []byte, gasP uint64, errP error) bool {
	if string(retF) != string(retP) || gasF != gasP || (errF == nil) != (errP == nil) {
		return false
	}
	if errF == nil {
		return true
	}
	var uf, up *ErrStackUnderflow
	if errors.As(errF, &uf) && errors.As(errP, &up) {
		return true
	}
	var of, op *ErrStackOverflow
	if errors.As(errF, &of) && errors.As(errP, &op) {
		return true
	}
	// The block precheck may report out-of-gas where the per-opcode path
	// reports a stack fault; both fail the frame and consume its gas.
	if errors.Is(errF, ErrOutOfGas) && (up != nil || op != nil) || errors.Is(errP, ErrOutOfGas) && (uf != nil || of != nil) {
		return true
	}
	return errF.Error() == errP.Error()
}

func TestFusedExecutionMatchesPlain(t *testing.T) {
	// Returns 0x2a if CALLDATA-free branch taken, loops twice via a static
	// JUMP, then a dynamic JUMP, then an undefined 0x0c opcode when gas allows.
	code := []byte{
		byte(PUSH1), 1, byte(PUSH2), 0, 8, byte(JUMPI), // 0: taken
		byte(INVALID), byte(INVALID), // 6: skipped
		byte(JUMPDEST),                                 // 8
		byte(PUSH1), 0, byte(PUSH2), 0, 8, byte(JUMPI), // 9: not taken
		byte(PUSH1), 0x2a, byte(PUSH1), 0, byte(MSTORE), // 15
		byte(PUSH1), 24, byte(JUMP), // 20: static jump forward
		byte(INVALID),                           // 23
		byte(JUMPDEST),                          // 24
		byte(PUSH1), 29, byte(DUP1), byte(JUMP), // 25: dynamic jump (DUP breaks fusion)
		byte(JUMPDEST),                                // 29
		byte(PUSH1), 32, byte(PUSH1), 0, byte(RETURN), // 30
	}
	for _, gas := range []uint64{100000, 60, 50, 40, 30, 20, 14, 13, 12, 11, 4, 3, 2, 1, 0} {
		retF, gasF, errF, retP, gasP, errP := runFusedAndPlain(t, code, gas)
		if !sameOutcome(retF, gasF, errF, retP, gasP, errP) {
			t.Fatalf("gas=%d: fused (%x, %d, %v) plain (%x, %d, %v)", gas, retF, gasF, errF, retP, gasP, errP)
		}
	}
	if ret, _, err := func() ([]byte, uint64, error) { r, g, e, _, _, _ := runFusedAndPlain(t, code, 100000); return r, g, e }(); err != nil || len(ret) != 32 || ret[31] != 0x2a {
		t.Fatalf("unexpected result %x %v", ret, err)
	}
}

func TestFusedUndefinedAndInvalidJumpMatchPlain(t *testing.T) {
	cases := [][]byte{
		{byte(PUSH1), 1, byte(PUSH2), 0, 8, byte(JUMPI), byte(STOP), byte(STOP), byte(JUMPDEST), 0x0c}, // real 0x0c after a fused jump
		{0x0d}, // undefined at pc 0
		{byte(PUSH1), 5, byte(JUMP), byte(JUMPDEST), byte(STOP)}, // static jump to a non-JUMPDEST: stays dynamic, invalid
		{byte(PUSH1), 3, byte(JUMPI)},                            // stack underflow on the fused op
		{byte(PUSH2), 0, 4, byte(JUMP), byte(JUMPDEST), byte(PUSH1), 0, byte(DUP1), byte(REVERT)},
	}
	for i, code := range cases {
		retF, gasF, errF, retP, gasP, errP := runFusedAndPlain(t, code, 1000)
		if !sameOutcome(retF, gasF, errF, retP, gasP, errP) {
			t.Fatalf("case %d: fused (%x, %d, %v) plain (%x, %d, %v)", i, retF, gasF, errF, retP, gasP, errP)
		}
	}
}

func TestPlainViewNeutralisesSyntheticValues(t *testing.T) {
	code := []byte{byte(PUSH1), 0x0c, 0x0d, byte(STOP)}
	got := plainView(code)
	if got[1] != 0x0c || got[2] != byte(fusedCollision) || code[2] != 0x0d {
		t.Fatalf("plainView = %x", got)
	}
	clean := []byte{byte(PUSH1), 0x0c, byte(STOP)}
	if v := plainView(clean); &v[0] != &clean[0] {
		t.Fatal("clean code must not be copied")
	}
}

func TestFusedViewIsCachedPerCodeHash(t *testing.T) {
	saved := GlobalCodeAnalysisCache
	GlobalCodeAnalysisCache = NewCodeAnalysisCache(16)
	defer func() { GlobalCodeAnalysisCache = saved }()
	code := []byte{byte(PUSH1), 4, byte(JUMP), byte(STOP), byte(JUMPDEST), byte(STOP)}
	c := &Contract{Code: code, CodeHash: crypto.Keccak256Hash(code), jumpdests: map[types.Hash][]uint64{}}
	jt := &cancunInstructionSet
	v1, b1 := execView(c, jt, opMetaFor(jt))
	v2, b2 := execView(c, jt, opMetaFor(jt))
	if b1 == nil || b1 != b2 {
		t.Fatal("block table not cached")
	}
	if &v1[0] != &v2[0] || v1[0] != byte(fusedPush1Jump) {
		t.Fatal("execution view not cached or not fused")
	}
	if c.analysis == nil {
		t.Fatal("execView should resolve the jumpdest bitmap")
	}
}
