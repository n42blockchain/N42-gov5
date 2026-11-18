package vm

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestAddFusedOpsSkipsWhenTableLacksJumpOps(t *testing.T) {
	t.Parallel()

	var tbl opMetaTable
	var jt JumpTable // JUMP/JUMPI/PUSH1 all nil

	addFusedOps(&tbl, &jt)

	if tbl[fusedPush1Jump].execute != nil {
		t.Fatalf("expected addFusedOps to leave the table untouched when the base jump table is incomplete")
	}
}

func TestExecViewReusesCachedFusedCodeAcrossContracts(t *testing.T) {
	saved := GlobalCodeAnalysisCache
	GlobalCodeAnalysisCache = NewCodeAnalysisCache(16)
	defer func() { GlobalCodeAnalysisCache = saved }()

	code := []byte{
		byte(PUSH1), 1,
		byte(PUSH1), 5, byte(JUMP),
		byte(JUMPDEST),
		byte(STOP),
	}
	codeHash := types.BytesToHash([]byte("g65-exec-view-reuse"))

	first := &Contract{Code: code, CodeHash: codeHash}
	view1 := execView(first)
	if view1[2] != byte(fusedPush1Jump) {
		t.Fatalf("expected the first call to produce a fused view")
	}

	// A fresh contract with no local analysis but the same code hash must
	// hit the cached-entry fast path and inherit the cached analysis.
	second := &Contract{Code: code, CodeHash: codeHash}
	view2 := execView(second)
	if &view1[0] != &view2[0] {
		t.Fatalf("expected the cached fused view to be reused, not rebuilt")
	}
	if second.analysis == nil {
		t.Fatalf("expected execView to populate the contract's local analysis from the cache entry")
	}
}

// TestExecViewDoesNotCacheTheCallersCode: code with nothing to fuse comes
// back from fuseCode as the caller's own slice, and the caller's slice is a
// view of the database's memory map that is only valid for its read
// transaction. The cached view must be a copy: a later execution of the same
// code hash otherwise runs whatever those pages hold by then (zeroes execute
// as STOP, so a call that stores returns at once with no gas used).
func TestExecViewDoesNotCacheTheCallersCode(t *testing.T) {
	saved := GlobalCodeAnalysisCache
	GlobalCodeAnalysisCache = NewCodeAnalysisCache(16)
	defer func() { GlobalCodeAnalysisCache = saved }()

	// PUSH1 1 PUSH1 0 SSTORE PUSH1 0 PUSH1 0 SSTORE STOP: no static jump.
	want := []byte{byte(PUSH1), 1, byte(PUSH1), 0, byte(SSTORE), byte(PUSH1), 0, byte(PUSH1), 0, byte(SSTORE), byte(STOP)}
	mapped := append([]byte(nil), want...) // stands for the transaction's memory
	codeHash := types.BytesToHash([]byte("exec-view-owns-its-bytes"))

	execView(&Contract{Code: mapped, CodeHash: codeHash})
	clear(mapped) // the read transaction ended and its pages were reused

	fresh := append([]byte(nil), want...)
	view := execView(&Contract{Code: fresh, CodeHash: codeHash})
	if string(view) != string(want) {
		t.Fatalf("cached view follows the first caller's memory: got %x, want %x", view, want)
	}
}
