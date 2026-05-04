// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestWitnessV2_RoundTrip records a few reads via WitnessStateReader,
// encodes to the v2 sorted-map blob, decodes via WitnessReplayReader,
// and confirms the read values match — order of recording is shuffled
// vs order of replay reads to prove key-based lookup is order-
// independent.
func TestWitnessV2_RoundTrip(t *testing.T) {
	addrA := types.HexToAddress("0x000000000000000000000000000000000000a000")
	addrB := types.HexToAddress("0x000000000000000000000000000000000000b000")
	slot1 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001")
	slot2 := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002")

	src := &fakeReader{
		accounts: map[types.Address]*account.StateAccount{
			addrA: {Initialised: true, Nonce: 7},
		},
		storage: map[types.Address]map[types.Hash][]byte{
			addrA: {slot1: {0x42}},
			addrB: {slot2: {0xab, 0xcd}},
		},
	}
	src.accounts[addrA].Balance.SetUint64(123)

	w := NewWitnessStateReader(src)
	// Recording order: B's slot first, then A's account, then A's slot,
	// then absent account, then absent slot. Order is intentionally not
	// the natural sort order.
	w.ReadAccountStorage(addrB, &slot2)
	w.ReadAccountData(addrA)
	w.ReadAccountStorage(addrA, &slot1)
	w.ReadAccountData(addrB)                          // absent
	w.ReadAccountStorage(addrA, &types.Hash{0xff})    // absent

	blob := w.Encode()
	r, err := NewWitnessReplayReaderStrict(blob, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Replay reads in *different* order than recording.
	gotA, _ := r.ReadAccountData(addrA)
	if gotA == nil || gotA.Nonce != 7 || gotA.Balance.Uint64() != 123 {
		t.Fatalf("addrA roundtrip lost data: %+v", gotA)
	}
	gotB, _ := r.ReadAccountData(addrB)
	if gotB != nil {
		t.Fatalf("addrB should be absent, got %+v", gotB)
	}
	v1, _ := r.ReadAccountStorage(addrA, &slot1)
	if len(v1) != 1 || v1[0] != 0x42 {
		t.Fatalf("addrA/slot1 roundtrip: got %x", v1)
	}
	v2, _ := r.ReadAccountStorage(addrB, &slot2)
	if len(v2) != 2 || v2[0] != 0xab || v2[1] != 0xcd {
		t.Fatalf("addrB/slot2 roundtrip: got %x", v2)
	}
	vAbsent, _ := r.ReadAccountStorage(addrA, &types.Hash{0xff})
	if vAbsent != nil {
		t.Fatalf("absent slot should yield nil, got %x", vAbsent)
	}
}

// TestWitnessV2_EmptyEncodesToNil confirms zero-recording yields a
// zero-length blob (so the freezer entry stays tiny for empty blocks).
func TestWitnessV2_EmptyEncodesToNil(t *testing.T) {
	w := NewWitnessStateReader(&fakeReader{})
	if got := w.Encode(); len(got) != 0 {
		t.Fatalf("empty encode should be 0 bytes, got %d", len(got))
	}
	r, err := NewWitnessReplayReaderStrict(nil, nil)
	if err != nil {
		t.Fatalf("nil-blob decode: %v", err)
	}
	got, _ := r.ReadAccountData(types.Address{0xfe})
	if got != nil {
		t.Fatalf("absent in empty witness, got %+v", got)
	}
}

// TestWitnessV2_RejectsUnknownVersion pins the upgrade contract: any
// unknown version byte fails decoding fast, so a stale binary doesn't
// silently misinterpret a newer-format blob.
func TestWitnessV2_RejectsUnknownVersion(t *testing.T) {
	bogus := []byte{99, 0, 0}
	if _, err := NewWitnessReplayReaderStrict(bogus, nil); err == nil {
		t.Fatal("decode of unknown-version blob should fail")
	}
}

// TestWitnessReplayReader_CodeFromMDBX confirms the asymmetric source
// for code: witness omits it, the reader pulls from the supplied
// kv.Tx's Code table.
func TestWitnessReplayReader_CodeFromMDBX(t *testing.T) {
	codeHash := types.HexToHash("0x000000000000000000000000000000000000000000000000000000000000c0de")
	bytecode := []byte{0x60, 0x80, 0x60, 0x40, 0x52}

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	{
		rwTx, err := db.BeginRw(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Put("Code", codeHash[:], bytecode); err != nil {
			t.Fatal(err)
		}
		if err := rwTx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	roTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()

	r := NewWitnessReplayReader(nil, roTx)
	got, err := r.ReadAccountCode(types.Address{}, codeHash)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(bytecode) {
		t.Fatalf("code roundtrip got %x want %x", got, bytecode)
	}
}

// TestWitnessCapturingWriter_RecordsBothOldAndNew pins the contract
// the parallel worker depends on: every UpdateAccountData / Storage
// write records BOTH (old, new) values.
func TestWitnessCapturingWriter_RecordsBothOldAndNew(t *testing.T) {
	addr := types.HexToAddress("0x2222222222222222222222222222222222222222")
	slot := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000005")

	old := &account.StateAccount{Initialised: true, Nonce: 1}
	old.Balance.SetUint64(50)
	new1 := &account.StateAccount{Initialised: true, Nonce: 2}
	new1.Balance.SetUint64(75)

	w := NewWitnessCapturingWriter()
	if err := w.UpdateAccountData(addr, old, new1); err != nil {
		t.Fatal(err)
	}
	cs, err := w.ChangeSetWriter().GetAccountChanges()
	if err != nil {
		t.Fatal(err)
	}
	if cs.Len() != 1 {
		t.Fatalf("expected 1 account change, got %d", cs.Len())
	}
	if v := w.AccountNewValue(addr); len(v) == 0 {
		t.Fatal("AccountNewValue empty for updated account")
	}

	oldSto := uint256.NewInt(0)
	newSto := uint256.NewInt(0xDEADBEEF)
	if err := w.WriteAccountStorage(addr, &slot, oldSto, newSto); err != nil {
		t.Fatal(err)
	}
	stoCS, err := w.ChangeSetWriter().GetStorageChanges()
	if err != nil {
		t.Fatal(err)
	}
	if stoCS.Len() != 1 {
		t.Fatalf("expected 1 storage change, got %d", stoCS.Len())
	}
	if v := w.StorageNewValue(addr, slot); len(v) == 0 {
		t.Fatal("StorageNewValue empty for updated slot")
	}
}

// fakeReader is a minimal in-memory state.StateReader for tests.
type fakeReader struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash][]byte
}

func (f *fakeReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	return f.accounts[addr], nil
}
func (f *fakeReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	if slots, ok := f.storage[addr]; ok {
		return slots[*key], nil
	}
	return nil, nil
}
func (f *fakeReader) ReadAccountCode(types.Address, types.Hash) ([]byte, error) { return nil, nil }
func (f *fakeReader) ReadAccountCodeSize(types.Address, types.Hash) (int, error) { return 0, nil }
