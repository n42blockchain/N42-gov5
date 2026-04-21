// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func TestEncodeKeys_AreDistinct(t *testing.T) {
	var addr1 types.Address
	var addr2 types.Address
	copy(addr1[:], bytes.Repeat([]byte{0x01}, 20))
	copy(addr2[:], bytes.Repeat([]byte{0x02}, 20))
	var slot types.Hash
	copy(slot[:], bytes.Repeat([]byte{0x03}, 32))
	var codeHash types.Hash
	copy(codeHash[:], bytes.Repeat([]byte{0x04}, 32))

	keys := [][]byte{
		EncodeAccountKey(addr1),
		EncodeAccountKey(addr2),
		EncodeStorageKey(addr1, slot),
		EncodeStorageKey(addr2, slot),
		EncodeCodeKey(codeHash),
	}
	seen := make(map[string]bool, len(keys))
	for i, k := range keys {
		if seen[string(k)] {
			t.Errorf("key[%d] collides with an earlier key", i)
		}
		seen[string(k)] = true
	}
}

func TestEVMStateView_AccountRoundtrip(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)
	view := NewEVMStateView(NewMVStateView(mv, base, 3, 0))

	var addr types.Address
	copy(addr[:], bytes.Repeat([]byte{0x42}, 20))

	// Read before write: nil.
	acct, err := view.ReadAccount(addr)
	if err != nil || acct != nil {
		t.Fatalf("read before write: acct=%v err=%v", acct, err)
	}

	// Write account.
	want := &account.StateAccount{
		Nonce:       7,
		Balance:     *uint256.NewInt(1000),
		Initialised: true,
	}
	copy(want.CodeHash[:], bytes.Repeat([]byte{0xab}, 32))
	view.WriteAccount(addr, want)

	// Self-read from writeSet.
	got, err := view.ReadAccount(addr)
	if err != nil {
		t.Fatal(err)
	}
	if got.Nonce != want.Nonce || got.Balance.Cmp(&want.Balance) != 0 {
		t.Errorf("roundtrip: got=%+v want=%+v", got, want)
	}
}

func TestEVMStateView_StorageRoundtrip(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)
	view := NewEVMStateView(NewMVStateView(mv, base, 5, 0))

	var addr types.Address
	var slot types.Hash
	copy(addr[:], bytes.Repeat([]byte{0x7}, 20))
	copy(slot[:], bytes.Repeat([]byte{0x8}, 32))

	// Zero initially.
	got, err := view.ReadStorage(addr, slot)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsZero() {
		t.Errorf("initial: got %s want 0", got.String())
	}

	// Write.
	val := uint256.NewInt(0xdeadbeef)
	view.WriteStorage(addr, slot, val)

	got, err = view.ReadStorage(addr, slot)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cmp(val) != 0 {
		t.Errorf("roundtrip: got %s want %s", got.String(), val.String())
	}

	// Write zero deletes.
	view.WriteStorage(addr, slot, uint256.NewInt(0))
	got, _ = view.ReadStorage(addr, slot)
	if !got.IsZero() {
		t.Errorf("after zero-write: got %s want 0", got.String())
	}
}

func TestEVMStateView_CodeRoundtrip(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)
	view := NewEVMStateView(NewMVStateView(mv, base, 2, 0))

	var codeHash types.Hash
	copy(codeHash[:], bytes.Repeat([]byte{0xaa}, 32))
	code := []byte{0x60, 0x80, 0x60, 0x40, 0x52} // random bytecode prefix

	view.WriteCode(codeHash, code)

	got, err := view.ReadCode(codeHash)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, code) {
		t.Errorf("code roundtrip: got %x want %x", got, code)
	}
}

// mvMockReader implements StateReader for testing
// MVBaseFromStateReader.
type mvMockReader struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash][]byte
	code     map[types.Hash][]byte
}

func newMVMockReader() *mvMockReader {
	return &mvMockReader{
		accounts: make(map[types.Address]*account.StateAccount),
		storage:  make(map[types.Address]map[types.Hash][]byte),
		code:     make(map[types.Hash][]byte),
	}
}

func (m *mvMockReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	if a, ok := m.accounts[addr]; ok {
		return a, nil
	}
	return nil, nil
}

func (m *mvMockReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	if slots, ok := m.storage[addr]; ok {
		if v, ok2 := slots[*key]; ok2 {
			return v, nil
		}
	}
	return nil, nil
}

func (m *mvMockReader) ReadAccountCode(addr types.Address, codeHash types.Hash) ([]byte, error) {
	if c, ok := m.code[codeHash]; ok {
		return c, nil
	}
	return nil, nil
}

func (m *mvMockReader) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	if c, ok := m.code[codeHash]; ok {
		return len(c), nil
	}
	return 0, nil
}

func (m *mvMockReader) ReadAccountIncarnation(addr types.Address) (uint64, error) {
	return 0, nil
}

func TestMVBaseFromStateReader_AccountPassthrough(t *testing.T) {
	r := newMVMockReader()
	var addr types.Address
	copy(addr[:], bytes.Repeat([]byte{0x1}, 20))
	r.accounts[addr] = &account.StateAccount{
		Nonce:       5,
		Balance:     *uint256.NewInt(42),
		Initialised: true,
	}
	base := NewMVBaseFromStateReader(r)

	enc, err := base.Get(EncodeAccountKey(addr))
	if err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 {
		t.Fatal("expected non-empty encoded account")
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		t.Fatal(err)
	}
	if a.Nonce != 5 || a.Balance.Uint64() != 42 {
		t.Errorf("decoded: %+v", a)
	}

	// Missing address returns nil, no error.
	var missing types.Address
	copy(missing[:], bytes.Repeat([]byte{0x99}, 20))
	enc, err = base.Get(EncodeAccountKey(missing))
	if err != nil {
		t.Fatal(err)
	}
	if enc != nil {
		t.Errorf("missing addr: got %x want nil", enc)
	}
}

func TestMVBaseFromStateReader_StoragePassthrough(t *testing.T) {
	r := newMVMockReader()
	var addr types.Address
	var slot types.Hash
	copy(addr[:], bytes.Repeat([]byte{0x2}, 20))
	copy(slot[:], bytes.Repeat([]byte{0x3}, 32))
	r.storage[addr] = map[types.Hash][]byte{slot: {0xcc, 0xdd}}
	base := NewMVBaseFromStateReader(r)

	v, err := base.Get(EncodeStorageKey(addr, slot))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v, []byte{0xcc, 0xdd}) {
		t.Errorf("got %x want ccdd", v)
	}
}

func TestMVBaseFromStateReader_CodePassthrough(t *testing.T) {
	r := newMVMockReader()
	var codeHash types.Hash
	copy(codeHash[:], bytes.Repeat([]byte{0x4}, 32))
	code := []byte{0x60, 0x80}
	r.code[codeHash] = code
	base := NewMVBaseFromStateReader(r)

	v, err := base.Get(EncodeCodeKey(codeHash))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(v, code) {
		t.Errorf("got %x want %x", v, code)
	}
}

func TestMVBaseFromStateReader_BadKeyTag(t *testing.T) {
	base := NewMVBaseFromStateReader(newMVMockReader())
	_, err := base.Get([]byte{99, 0, 0, 0})
	if err == nil {
		t.Error("expected error for unknown tag")
	}
	_, err = base.Get(nil)
	if err == nil {
		t.Error("expected error for empty key")
	}
}

// TestEVMStateView_MVLayeredOverBase verifies that the typed view
// falls through to the base reader when MVHashMap has no entry.
func TestEVMStateView_MVLayeredOverBase(t *testing.T) {
	mv := NewMVHashMap(16)
	r := newMVMockReader()

	var addr types.Address
	copy(addr[:], bytes.Repeat([]byte{0x1}, 20))
	baseAcct := &account.StateAccount{
		Nonce:       99,
		Balance:     *uint256.NewInt(1000),
		Initialised: true,
	}
	r.accounts[addr] = baseAcct
	base := NewMVBaseFromStateReader(r)

	// Tx 5 reads. MV empty, should fall through.
	view := NewEVMStateView(NewMVStateView(mv, base, 5, 0))
	got, err := view.ReadAccount(addr)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Nonce != 99 {
		t.Errorf("got %+v want nonce=99", got)
	}

	// Now tx 2 writes (earlier txIdx).
	inner := NewMVStateView(mv, base, 2, 0)
	newAcct := &account.StateAccount{
		Nonce:       100,
		Balance:     *uint256.NewInt(2000),
		Initialised: true,
	}
	NewEVMStateView(inner).WriteAccount(addr, newAcct)
	inner.FlushWrites()

	// Fresh view for tx 5 — should now see tx 2's write, not base.
	view2 := NewEVMStateView(NewMVStateView(mv, base, 5, 0))
	got, err = view2.ReadAccount(addr)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Nonce != 100 {
		t.Errorf("got %+v want nonce=100 (tx 2's write)", got)
	}
}
