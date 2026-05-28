package commitment

import (
	"bytes"
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// TestHashedCanonicalUnwind exercises the full record→forward→unwind round trip:
// a block-1 delta mutates an account, creates a new account (with code),
// modifies + adds storage, and changes a storage-only account; the writer
// records changesets; ComputeRoot writes the forward state; then
// UnwindHashedCanonical reverts block 1 and must reproduce block 0's root +
// state exactly, and clear the consumed changesets.
func TestHashedCanonicalUnwind(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, _ := db.BeginRw(context.Background())
	defer tx.Rollback()

	hash := func(b []byte) types.Hash { return crypto.Keccak256Hash(b) }
	slot := func(n byte) types.Hash { var s types.Hash; s[31] = n; return s }
	emptyCH := crypto.Keccak256Hash(nil)

	var A, B, C types.Address
	A[19], B[19], C[19] = 0xAA, 0xBB, 0xCC

	// --- block 0 (bootstrap) ---
	a0 := &account.StateAccount{Initialised: true, Nonce: 5, CodeHash: emptyCH}
	a0.Balance.SetUint64(1000)
	b0 := &account.StateAccount{Initialised: true, Nonce: 2, CodeHash: emptyCH}
	b0.Balance.SetUint64(2000)
	initAccts := map[types.Address]*account.StateAccount{A: a0, B: b0}
	initStor := map[types.Address]map[types.Hash]*uint256.Int{
		A: {slot(1): uint256.NewInt(10)},
		B: {slot(3): uint256.NewInt(20)},
	}

	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	r0, err := trc.ComputeRoot(initAccts, initStor)
	if err != nil {
		t.Fatal(err)
	}

	// --- block 1 forward: record changesets (writer) + write forward (ComputeRoot) ---
	w := state.NewHashedCanonicalWriter(tx, 1)

	aNew := &account.StateAccount{Initialised: true, Nonce: 6, CodeHash: emptyCH}
	aNew.Balance.SetUint64(1500) // A balance change
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xf3}
	cCode := crypto.Keccak256Hash(code)
	cNew := &account.StateAccount{Initialised: true, Nonce: 1, CodeHash: cCode} // C new contract

	// Simulate CommitBlock's per-object writer calls (original=block0, new=block1).
	if err := w.UpdateAccountData(A, a0, aNew); err != nil {
		t.Fatal(err)
	}
	if err := w.UpdateAccountData(C, &account.StateAccount{}, cNew); err != nil { // new: original empty
		t.Fatal(err)
	}
	if err := w.UpdateAccountCode(C, cCode, code); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAccountStorage(A, slot(1), *uint256.NewInt(10), *uint256.NewInt(11)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAccountStorage(A, slot(2), *uint256.NewInt(0), *uint256.NewInt(99)); err != nil { // new slot
		t.Fatal(err)
	}
	if err := w.WriteAccountStorage(B, slot(3), *uint256.NewInt(20), *uint256.NewInt(21)); err != nil { // storage-only acct
		t.Fatal(err)
	}
	if err := w.WriteChangeSets(); err != nil {
		t.Fatal(err)
	}

	// Forward state write (what IntermediateRoot does).
	trc.SetIncremental(true)
	fwdAccts := map[types.Address]*account.StateAccount{A: aNew, B: b0, C: cNew}
	fwdStor := map[types.Address]map[types.Hash]*uint256.Int{
		A: {slot(1): uint256.NewInt(11), slot(2): uint256.NewInt(99)},
		B: {slot(3): uint256.NewInt(21)},
	}
	r1, err := trc.ComputeRoot(fwdAccts, fwdStor)
	if err != nil {
		t.Fatal(err)
	}
	if r1 == r0 {
		t.Fatal("forward root equals bootstrap root — delta had no effect")
	}
	// Code persisted + C present.
	if got, _ := tx.GetOne(modules.Code, cCode[:]); !bytes.Equal(got, code) {
		t.Fatalf("code not persisted: got %x", got)
	}
	if v, _ := tx.GetOne(modules.HashedAccounts, hashBytes(hash, C)); len(v) == 0 {
		t.Fatal("C not in HashedAccounts after forward")
	}

	// --- unwind block 1 → must reproduce block 0 ---
	r0b, err := UnwindHashedCanonical(tx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r0b != r0 {
		t.Fatalf("unwind root mismatch: got %x want %x", r0b[:8], r0[:8])
	}

	// State restored:
	if v, _ := tx.GetOne(modules.HashedAccounts, hashBytes(hash, C)); len(v) != 0 {
		t.Error("C should be deleted after unwind (was created in block 1)")
	}
	if v, _ := tx.GetOne(modules.HashedAccounts, hashBytes(hash, A)); !bytes.Equal(v, a0.MarshalV2()) {
		t.Errorf("A not restored: got %x want %x", v, a0.MarshalV2())
	}
	if v := readStorage(tx, hash, A, slot(1)); v != 10 {
		t.Errorf("A.slot1 not restored: got %d want 10", v)
	}
	if v := readStorage(tx, hash, A, slot(2)); v != 0 {
		t.Errorf("A.slot2 should be deleted (new in block 1): got %d", v)
	}
	if v := readStorage(tx, hash, B, slot(3)); v != 20 {
		t.Errorf("B.slot3 not restored: got %d want 20", v)
	}

	// Changesets consumed.
	if v, _ := tx.GetOne(modules.AccountChangeSet, modules.EncodeBlockNumber(1)); len(v) != 0 {
		t.Error("AccountChangeSet[1] not deleted after unwind")
	}
}

// TestHashedCanonicalUnwindMultiBlock unwinds two blocks at once (2→0) and
// verifies the descending per-block replay restores block 0 exactly.
func TestHashedCanonicalUnwindMultiBlock(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, _ := db.BeginRw(context.Background())
	defer tx.Rollback()
	emptyCH := crypto.Keccak256Hash(nil)
	hash := func(b []byte) types.Hash { return crypto.Keccak256Hash(b) }
	var A types.Address
	A[19] = 0xAA

	mk := func(nonce, bal uint64) *account.StateAccount {
		a := &account.StateAccount{Initialised: true, Nonce: nonce, CodeHash: emptyCH}
		a.Balance.SetUint64(bal)
		return a
	}

	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	a0 := mk(1, 100)
	r0, err := trc.ComputeRoot(map[types.Address]*account.StateAccount{A: a0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	trc.SetIncremental(true)

	prev := a0
	apply := func(blk, nonce, bal uint64) {
		w := state.NewHashedCanonicalWriter(tx, blk)
		na := mk(nonce, bal)
		if err := w.UpdateAccountData(A, prev, na); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteChangeSets(); err != nil {
			t.Fatal(err)
		}
		if _, err := trc.ComputeRoot(map[types.Address]*account.StateAccount{A: na}, nil); err != nil {
			t.Fatal(err)
		}
		prev = na
	}
	apply(1, 2, 200)
	apply(2, 3, 300)

	r0b, err := UnwindHashedCanonical(tx, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r0b != r0 {
		t.Fatalf("multi-block unwind root mismatch: got %x want %x", r0b[:8], r0[:8])
	}
	if v, _ := tx.GetOne(modules.HashedAccounts, hashBytes(hash, A)); !bytes.Equal(v, a0.MarshalV2()) {
		t.Errorf("A not restored to block 0: got %x want %x", v, a0.MarshalV2())
	}
}

func hashBytes(h func([]byte) types.Hash, addr types.Address) []byte {
	hh := h(addr[:])
	return hh[:]
}

// readStorage reads HashedStorage[keccak(addr)+keccak(slot)] as a uint64 (0 if absent).
func readStorage(tx interface {
	GetOne(string, []byte) ([]byte, error)
}, h func([]byte) types.Hash, addr types.Address, s types.Hash) uint64 {
	ah := h(addr[:])
	sh := h(s[:])
	key := append(append([]byte{}, ah[:]...), sh[:]...)
	v, _ := tx.GetOne(modules.HashedStorage, key)
	if len(v) == 0 {
		return 0
	}
	return new(uint256.Int).SetBytes(v).Uint64()
}
