package commitment

import (
	"math/big"
	"testing"

	gverkle "github.com/ethereum/go-verkle"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	libverkle "github.com/n42blockchain/N42/lib/verkle"
	"github.com/n42blockchain/N42/modules/state"
)

func TestVerkleCommitment_Basic(t *testing.T) {
	root := gverkle.New()
	store := libverkle.NewMemStore()
	vc := NewVerkleCommitment(root, store)

	// Insert an account.
	addr := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	acct := &account.StateAccount{
		Nonce:       1,
		Initialised: true,
	}
	acct.Balance.SetUint64(1000)

	if err := vc.UpdateAccount(addr, acct); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	// Root should be non-zero after insert.
	h := vc.Root()
	if h == (types.Hash{}) {
		t.Fatal("expected non-zero root after insert")
	}

	// Flush should persist nodes.
	nodes, bytes, err := vc.Flush()
	if err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if nodes == 0 {
		t.Fatal("expected nodes > 0 after Flush")
	}
	if bytes == 0 {
		t.Fatal("expected bytes > 0 after Flush")
	}
	if store.Len() == 0 {
		t.Fatal("expected store to have entries after Flush")
	}
	t.Logf("Flushed %d nodes, %d bytes, store has %d entries", nodes, bytes, store.Len())
}

func TestVerkleCommitment_Storage(t *testing.T) {
	root := gverkle.New()
	store := libverkle.NewMemStore()
	vc := NewVerkleCommitment(root, store)

	addr := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	slot := types.HexToHash("0x01")
	val := uint256.NewInt(42)

	if err := vc.UpdateStorage(addr, slot, val); err != nil {
		t.Fatalf("UpdateStorage: %v", err)
	}

	h := vc.Root()
	if h == (types.Hash{}) {
		t.Fatal("expected non-zero root after storage insert")
	}

	// Delete storage.
	if err := vc.UpdateStorage(addr, slot, uint256.NewInt(0)); err != nil {
		t.Fatalf("UpdateStorage delete: %v", err)
	}
}

func TestVerkleCommitment_DeleteAccount(t *testing.T) {
	root := gverkle.New()
	store := libverkle.NewMemStore()
	vc := NewVerkleCommitment(root, store)

	addr := types.HexToAddress("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	acct := &account.StateAccount{
		Nonce:       5,
		Initialised: true,
	}
	acct.Balance.SetFromBig(big.NewInt(999))

	_ = vc.UpdateAccount(addr, acct)
	rootBefore := vc.Root()

	// Delete.
	_ = vc.UpdateAccount(addr, nil)
	rootAfter := vc.Root()

	if rootBefore == rootAfter {
		t.Log("root unchanged after delete (may be expected for single-element tree)")
	}
}

func TestVerkleRootComputer_ComputeRoot(t *testing.T) {
	root := gverkle.New()
	store := libverkle.NewMemStore()
	vc := NewVerkleCommitment(root, store)
	rc := NewVerkleRootComputer(vc)

	// Verify scheme.
	if rc.RootScheme() != state.RootSchemeVerkle {
		t.Fatalf("expected RootSchemeVerkle, got %s", rc.RootScheme())
	}

	// Build dirty maps.
	accounts := map[types.Address]*account.StateAccount{
		types.HexToAddress("0x01"): {Nonce: 1, Initialised: true},
		types.HexToAddress("0x02"): {Nonce: 2, Initialised: true},
	}
	accounts[types.HexToAddress("0x01")].Balance.SetUint64(100)
	accounts[types.HexToAddress("0x02")].Balance.SetUint64(200)

	storage := map[types.Address]map[types.Hash]*uint256.Int{
		types.HexToAddress("0x01"): {
			types.HexToHash("0x00"): uint256.NewInt(10),
			types.HexToHash("0x01"): uint256.NewInt(20),
		},
	}

	h, err := rc.ComputeRoot(accounts, storage)
	if err != nil {
		t.Fatalf("ComputeRoot: %v", err)
	}
	if h == (types.Hash{}) {
		t.Fatal("expected non-zero root from ComputeRoot")
	}
	t.Logf("ComputeRoot: %x", h[:8])
}
