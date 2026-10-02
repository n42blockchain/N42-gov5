package commitment

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func TestLtHashCommitment_NewDefaultsToEmptyDigest(t *testing.T) {
	c := NewLtHashCommitment(nil)
	if !c.IsZero() {
		t.Fatalf("expected a fresh commitment to be zero")
	}
	zeroRoot := c.Root()
	if c.Digest() == nil {
		t.Fatalf("expected non-nil digest")
	}
	// Root of an empty digest must be deterministic across instances.
	c2 := NewLtHashCommitment(nil)
	if c2.Root() != zeroRoot {
		t.Fatalf("empty digests should produce identical roots")
	}
}

func TestLtHashCommitment_AccountCreateUpdateDelete(t *testing.T) {
	c := NewLtHashCommitment(nil)
	addr := types.Address{0x01}

	rootEmpty := c.Root()

	acct1 := &account.StateAccount{Initialised: true, Nonce: 1, Balance: *uint256.NewInt(100)}
	c.UpdateAccount(addr, nil, acct1)
	if c.IsZero() {
		t.Fatalf("expected non-zero digest after account creation")
	}
	rootAfterCreate := c.Root()
	if rootAfterCreate == rootEmpty {
		t.Fatalf("root should change after adding an account")
	}

	// Update: balance changes.
	acct2 := &account.StateAccount{Initialised: true, Nonce: 1, Balance: *uint256.NewInt(200)}
	c.UpdateAccount(addr, acct1, acct2)
	rootAfterUpdate := c.Root()
	if rootAfterUpdate == rootAfterCreate {
		t.Fatalf("root should change after updating the account")
	}

	// Delete: back to empty.
	c.UpdateAccount(addr, acct2, nil)
	if !c.IsZero() {
		t.Fatalf("expected digest to return to zero after deleting the only account")
	}
	if c.Root() != rootEmpty {
		t.Fatalf("root should return to empty-digest root after full deletion")
	}
}

func TestLtHashCommitment_AccountNilOldAndNewIsNoop(t *testing.T) {
	c := NewLtHashCommitment(nil)
	root0 := c.Root()
	c.UpdateAccount(types.Address{0x02}, nil, nil)
	if c.Root() != root0 {
		t.Fatalf("nil->nil account update must be a no-op")
	}
}

func TestLtHashCommitment_StorageCreateUpdateDelete(t *testing.T) {
	c := NewLtHashCommitment(nil)
	addr := types.Address{0x03}
	slot := types.Hash{0x04}

	rootEmpty := c.Root()

	v1 := uint256.NewInt(42)
	c.UpdateStorage(addr, slot, nil, v1)
	rootAfterCreate := c.Root()
	if rootAfterCreate == rootEmpty {
		t.Fatalf("root should change after storage creation")
	}

	v2 := uint256.NewInt(43)
	c.UpdateStorage(addr, slot, v1, v2)
	rootAfterUpdate := c.Root()
	if rootAfterUpdate == rootAfterCreate {
		t.Fatalf("root should change after storage update")
	}

	c.UpdateStorage(addr, slot, v2, nil)
	if c.Root() != rootEmpty {
		t.Fatalf("root should return to empty after storage deletion")
	}
}

func TestLtHashCommitment_StorageZeroValuesAreNoop(t *testing.T) {
	c := NewLtHashCommitment(nil)
	root0 := c.Root()
	zero := uint256.NewInt(0)
	c.UpdateStorage(types.Address{0x05}, types.Hash{0x06}, zero, zero)
	if c.Root() != root0 {
		t.Fatalf("zero->zero storage update must be a no-op")
	}
	// nil treated as zero too.
	c.UpdateStorage(types.Address{0x05}, types.Hash{0x06}, nil, nil)
	if c.Root() != root0 {
		t.Fatalf("nil->nil storage update must be a no-op")
	}
}

func TestLtHashCommitment_OrderIndependence(t *testing.T) {
	// Lattice hash digests are homomorphic / order-independent: applying
	// two independent account updates in either order yields the same root.
	addrA := types.Address{0xAA}
	addrB := types.Address{0xBB}
	acctA := &account.StateAccount{Initialised: true, Nonce: 1, Balance: *uint256.NewInt(10)}
	acctB := &account.StateAccount{Initialised: true, Nonce: 2, Balance: *uint256.NewInt(20)}

	c1 := NewLtHashCommitment(nil)
	c1.UpdateAccount(addrA, nil, acctA)
	c1.UpdateAccount(addrB, nil, acctB)

	c2 := NewLtHashCommitment(nil)
	c2.UpdateAccount(addrB, nil, acctB)
	c2.UpdateAccount(addrA, nil, acctA)

	if c1.Root() != c2.Root() {
		t.Fatalf("expected order-independent roots, got %x vs %x", c1.Root(), c2.Root())
	}
}
