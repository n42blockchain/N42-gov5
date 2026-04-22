// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func TestPlainStateBufferApplyTarget_AccountRoundtrip(t *testing.T) {
	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)

	addr := mkAddr(0xab)
	var a account.StateAccount
	a.Initialised = true
	a.Nonce = 7
	a.Balance.SetUint64(1234)

	enc := a.MarshalV2()
	if err := target.PutAccount(addr, enc); err != nil {
		t.Fatal(err)
	}

	got, ok := buf.accounts[addr]
	if !ok {
		t.Fatal("account not in buffer")
	}
	var decoded account.StateAccount
	if err := decoded.DecodeForStorage(got); err != nil {
		t.Fatal(err)
	}
	if decoded.Nonce != 7 || decoded.Balance.Uint64() != 1234 {
		t.Errorf("decoded: nonce=%d balance=%s", decoded.Nonce, decoded.Balance.String())
	}
}

func TestPlainStateBufferApplyTarget_StorageRoundtrip(t *testing.T) {
	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)

	addr := mkAddr(0x01)
	slot := mkHash(0x02)
	val := uint256.NewInt(0x4242).Bytes()

	if err := target.PutStorage(addr, slot, val); err != nil {
		t.Fatal(err)
	}

	slots, ok := buf.storage[addr]
	if !ok {
		t.Fatal("addr missing from buffer.storage")
	}
	entry, ok := slots[slot]
	if !ok {
		t.Fatal("slot missing")
	}
	got := new(uint256.Int).SetBytes(entry.value)
	if got.Uint64() != 0x4242 {
		t.Errorf("slot value: got %s want 0x4242", got.String())
	}
}

func TestPlainStateBufferApplyTarget_StorageEmptyDeletes(t *testing.T) {
	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)

	addr := mkAddr(0x01)
	slot := mkHash(0x02)

	// Write a non-zero first.
	if err := target.PutStorage(addr, slot, uint256.NewInt(5).Bytes()); err != nil {
		t.Fatal(err)
	}
	// Then "delete" (empty value).
	if err := target.PutStorage(addr, slot, nil); err != nil {
		t.Fatal(err)
	}

	slots, ok := buf.storage[addr]
	if !ok {
		t.Fatal("addr missing")
	}
	entry, ok := slots[slot]
	if !ok {
		t.Fatal("slot missing")
	}
	// Deletion is encoded as deletedSentinel (zero-length value) in the buffer.
	if len(entry.value) != 0 {
		t.Errorf("expected deletedSentinel (empty value); got %x", entry.value)
	}
}

func TestPlainStateBufferApplyTarget_CodeIdempotent(t *testing.T) {
	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)

	codeHash := mkHash(0xcc)
	code := []byte{0x60, 0x01, 0x60, 0x02} // PUSH1 01 PUSH1 02

	if err := target.PutCode(codeHash, code); err != nil {
		t.Fatal(err)
	}
	// Second call is idempotent (BufferedPlainStateWriter.UpdateAccountCode
	// just overwrites, which is fine).
	if err := target.PutCode(codeHash, code); err != nil {
		t.Fatal(err)
	}
	got, ok := buf.code[codeHash]
	if !ok {
		t.Fatal("code missing")
	}
	if len(got) != len(code) {
		t.Errorf("code len: got %d want %d", len(got), len(code))
	}
}

func TestPlainStateBufferApplyTarget_AddBalanceErrors(t *testing.T) {
	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)

	addr := mkAddr(0xff)
	err := target.AddBalance(addr, uint256.NewInt(100))
	if err == nil {
		t.Fatal("AddBalance should error in Phase 5 (CoinbaseDelta must be zero)")
	}
}

func TestPlainStateBufferApplyTarget_WipeStorage(t *testing.T) {
	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)

	addr := mkAddr(0x01)

	// Pre-populate some slots in the buffer to see the wipe.
	if err := target.PutStorage(addr, mkHash(0x01), uint256.NewInt(11).Bytes()); err != nil {
		t.Fatal(err)
	}

	if err := target.WipeStorage(addr); err != nil {
		t.Fatal(err)
	}

	// After CreateContract, the buffer tracks the wipe via wipedStorage map
	// and contractWipes slice; the MDBX storage-scan happens on flush.
	if _, ok := buf.wipedStorage[addr]; !ok {
		t.Errorf("addr not marked wiped in wipedStorage")
	}
}

func TestPlainStateBufferApplyTarget_EndToEndBlockCommit(t *testing.T) {
	// End-to-end: parallel pipeline populates MV, FinalizeBlock produces
	// a BlockCommit, Apply flows into PlainStateBuffer.
	addr := mkAddr(0xaa)
	slot := mkHash(0x1)

	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	// Single tx writes slot=99.
	v0 := NewEVMStateView(NewMVStateView(mv, base, 0, 0))
	v0.WriteStorage(addr, slot, uint256.NewInt(99))
	v0.Inner().FlushWrites()

	bc, err := FinalizeBlock(mv, []TxOutput{{TxIdx: 0, GasUsed: 21000, Status: 1}}, types.Address{})
	if err != nil {
		t.Fatal(err)
	}

	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	target := NewPlainStateBufferApplyTarget(writer)

	if err := bc.Apply(target); err != nil {
		t.Fatal(err)
	}

	slots := buf.storage[addr]
	entry, ok := slots[slot]
	if !ok {
		t.Fatal("slot missing after apply")
	}
	got := new(uint256.Int).SetBytes(entry.value)
	if got.Uint64() != 99 {
		t.Errorf("slot: got %s want 99", got.String())
	}
}
