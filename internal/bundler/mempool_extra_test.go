package bundler

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

// TestUserOpPool_RemoveBySender covers removing every pending operation for
// a sender, including the no-op path when the sender has nothing pending.
func TestUserOpPool_RemoveBySender(t *testing.T) {
	pool := NewUserOpPool(100, vm.EntryPointV07, 1)
	sender := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	other := types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd")

	if _, err := pool.Add(makeTestOp(sender, 0)); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if _, err := pool.Add(makeTestOp(sender, 1)); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if _, err := pool.Add(makeTestOp(other, 0)); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if pool.Count() != 3 {
		t.Fatalf("Count() = %d, want 3", pool.Count())
	}

	// No-op: sender with nothing pending.
	unrelated := types.HexToAddress("0x0000000000000000000000000000000000dead")
	pool.RemoveBySender(unrelated)
	if pool.Count() != 3 {
		t.Fatalf("Count() after no-op RemoveBySender = %d, want 3", pool.Count())
	}

	pool.RemoveBySender(sender)
	if pool.Count() != 1 {
		t.Fatalf("Count() after RemoveBySender = %d, want 1", pool.Count())
	}

	pending := pool.Pending(0)
	if len(pending) != 1 || pending[0].Sender != other {
		t.Fatalf("Pending() = %+v, want only %x's op", pending, other)
	}
}

// TestUserOpHashForReceipt verifies the receipt hash is deterministic for
// identical inputs, changes when the entry point or chain ID changes, and
// tolerates a nil Nonce.
func TestUserOpHashForReceipt(t *testing.T) {
	sender := types.HexToAddress("0x01")
	op := makeTestOp(sender, 0)
	entryPoint := vm.EntryPointV07

	h1 := UserOpHashForReceipt(op, entryPoint, 1)
	h2 := UserOpHashForReceipt(op, entryPoint, 1)
	if h1 != h2 {
		t.Fatalf("UserOpHashForReceipt() not deterministic: %x != %x", h1, h2)
	}

	h3 := UserOpHashForReceipt(op, entryPoint, 2)
	if h1 == h3 {
		t.Fatal("UserOpHashForReceipt() should differ across chain IDs")
	}

	otherEntryPoint := types.HexToAddress("0x02")
	h4 := UserOpHashForReceipt(op, otherEntryPoint, 1)
	if h1 == h4 {
		t.Fatal("UserOpHashForReceipt() should differ across entry points")
	}

	op.Nonce = nil
	if h := UserOpHashForReceipt(op, entryPoint, 1); h == (types.Hash{}) {
		t.Fatal("UserOpHashForReceipt() with nil Nonce produced zero hash unexpectedly")
	}
}
