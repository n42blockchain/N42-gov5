package vm

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/internal/vm/stack"
	"github.com/n42blockchain/N42/params"
)

// g65FusedScope builds a scope whose contract has enough gas and a code
// buffer long enough for the fused push+jump encodings to read their
// immediate bytes, so the synthetic opcode handlers in fuse.go can be
// invoked directly without going through the full interpreter loop.
func g65FusedScope(code []byte) *ScopeContext {
	return &ScopeContext{
		Stack:  stack.New(),
		Memory: NewMemory(),
		Contract: &Contract{
			Code: code,
			Gas:  1_000_000,
		},
	}
}

func TestOpFusedPush1JumpLandsOnDest(t *testing.T) {
	t.Parallel()
	code := []byte{byte(fusedPush1Jump), 3, byte(JUMPDEST)}
	scope := g65FusedScope(code)
	pc := uint64(0)

	if _, err := opFusedPush1Jump(&pc, nil, scope); err != nil {
		t.Fatalf("opFusedPush1Jump() error = %v", err)
	}
	if pc != 3 {
		t.Fatalf("pc = %d, want 3", pc)
	}
	if scope.Contract.Gas != 1_000_000-params.JumpdestGas {
		t.Fatalf("gas = %d, want %d charged for the landed JUMPDEST", scope.Contract.Gas, params.JumpdestGas)
	}
}

func TestOpFusedPush2JumpLandsOnDest(t *testing.T) {
	t.Parallel()
	code := make([]byte, 0x105)
	code[0] = byte(fusedPush2Jump)
	code[1] = 0x01
	code[2] = 0x00
	code[0x100] = byte(JUMPDEST)
	scope := g65FusedScope(code)
	pc := uint64(0)

	if _, err := opFusedPush2Jump(&pc, nil, scope); err != nil {
		t.Fatalf("opFusedPush2Jump() error = %v", err)
	}
	if pc != 0x100 {
		t.Fatalf("pc = %#x, want %#x", pc, 0x100)
	}
}

func TestOpFusedPush1JumpiBranches(t *testing.T) {
	t.Parallel()

	code := []byte{byte(fusedPush1Jumpi), 3, byte(JUMPDEST)}

	// Non-zero condition: takes the jump.
	taken := g65FusedScope(code)
	taken.Stack.Push(uint256.NewInt(1))
	pc := uint64(0)
	if _, err := opFusedPush1Jumpi(&pc, nil, taken); err != nil {
		t.Fatalf("opFusedPush1Jumpi() error = %v", err)
	}
	if pc != 3 {
		t.Fatalf("taken branch: pc = %d, want 3", pc)
	}

	// Zero condition: falls through past the two-byte encoding.
	notTaken := g65FusedScope(code)
	notTaken.Stack.Push(uint256.NewInt(0))
	pc = 0
	if _, err := opFusedPush1Jumpi(&pc, nil, notTaken); err != nil {
		t.Fatalf("opFusedPush1Jumpi() error = %v", err)
	}
	if pc != 2 {
		t.Fatalf("fallthrough branch: pc = %d, want 2", pc)
	}
}

func TestOpFusedPush2JumpiBranches(t *testing.T) {
	t.Parallel()

	code := []byte{byte(fusedPush2Jumpi), 0, 4, byte(JUMPDEST)}

	taken := g65FusedScope(code)
	taken.Stack.Push(uint256.NewInt(1))
	pc := uint64(0)
	if _, err := opFusedPush2Jumpi(&pc, nil, taken); err != nil {
		t.Fatalf("opFusedPush2Jumpi() error = %v", err)
	}
	if pc != 4 {
		t.Fatalf("taken branch: pc = %d, want 4", pc)
	}

	notTaken := g65FusedScope(code)
	notTaken.Stack.Push(uint256.NewInt(0))
	pc = 0
	if _, err := opFusedPush2Jumpi(&pc, nil, notTaken); err != nil {
		t.Fatalf("opFusedPush2Jumpi() error = %v", err)
	}
	if pc != 3 {
		t.Fatalf("fallthrough branch: pc = %d, want 3", pc)
	}
}

func TestFusedDestHelper(t *testing.T) {
	t.Parallel()
	code := []byte{byte(fusedPush2Jump), 0x01, 0x02}
	if got := fusedDest(code, 0, 2); got != 0x0102 {
		t.Fatalf("fusedDest(n=2) = %#x, want %#x", got, 0x0102)
	}
	code1 := []byte{byte(fusedPush1Jump), 0x07}
	if got := fusedDest(code1, 0, 1); got != 0x07 {
		t.Fatalf("fusedDest(n=1) = %#x, want %#x", got, 0x07)
	}
}

func TestFusedJumpOutOfGas(t *testing.T) {
	t.Parallel()
	c := &Contract{Gas: 0}
	pc := uint64(5)
	if err := fusedJump(&pc, c, 9); err != ErrOutOfGas {
		t.Fatalf("fusedJump() error = %v, want ErrOutOfGas", err)
	}
	if pc != 5 {
		t.Fatalf("pc must be left untouched on out-of-gas, got %d", pc)
	}
}
