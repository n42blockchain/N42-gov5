// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// minimalHeader and emptyPacket live in test_helpers_test.go.

// ----------------------------------------------------------------------------
// StreamReplayStateReader
// ----------------------------------------------------------------------------

func TestStreamReplay_AccountSequence(t *testing.T) {
	codeHash := types.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")
	log := []ReadLogEntry{
		ReadLogAccount{Nonce: 5, Balance: uint256.NewInt(1000), CodeHash: codeHash},
		ReadLogAccountNotFound{},
	}
	r := NewStreamReplayStateReader(log, NewCodeCache(8))

	a, err := r.ReadAccountData(types.Address{0x1})
	if err != nil {
		t.Fatal(err)
	}
	if a == nil || a.Nonce != 5 || a.Balance.Uint64() != 1000 || a.CodeHash != codeHash {
		t.Fatalf("first account wrong: %+v", a)
	}

	a2, err := r.ReadAccountData(types.Address{0x2})
	if err != nil {
		t.Fatal(err)
	}
	if a2 != nil {
		t.Fatalf("second read should be nil (AccountNotFound), got %+v", a2)
	}

	if r.Remaining() != 0 {
		t.Fatalf("cursor should be exhausted, remaining=%d", r.Remaining())
	}
}

func TestStreamReplay_StorageThenAccount(t *testing.T) {
	log := []ReadLogEntry{
		ReadLogStorage{Value: uint256.NewInt(0x42)},
		ReadLogAccount{Nonce: 1, Balance: uint256.NewInt(0), CodeHash: emptyCodeHash},
	}
	r := NewStreamReplayStateReader(log, NewCodeCache(8))

	key := types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000007")
	v, err := r.ReadAccountStorage(types.Address{0x1}, 0, &key)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) == 0 || v[len(v)-1] != 0x42 {
		t.Fatalf("storage value wrong: %x", v)
	}

	a, err := r.ReadAccountData(types.Address{0x2})
	if err != nil {
		t.Fatal(err)
	}
	if a == nil || a.Nonce != 1 {
		t.Fatalf("account read after storage wrong: %+v", a)
	}
}

func TestStreamReplay_ZeroStorageReturnsNil(t *testing.T) {
	log := []ReadLogEntry{
		ReadLogStorage{Value: uint256.NewInt(0)},
	}
	r := NewStreamReplayStateReader(log, NewCodeCache(8))
	key := types.Hash{}
	v, err := r.ReadAccountStorage(types.Address{0x1}, 0, &key)
	if err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Fatalf("zero storage should be nil, got %x", v)
	}
}

func TestStreamReplay_ExhaustionError(t *testing.T) {
	r := NewStreamReplayStateReader(nil, NewCodeCache(8))
	if _, err := r.ReadAccountData(types.Address{}); err == nil {
		t.Fatal("expected exhaustion error")
	} else if !errors.Is(err, ErrReplayCursorExhausted) {
		t.Fatalf("expected ErrReplayCursorExhausted, got %v", err)
	}
	if r.Err() == nil {
		t.Fatal("Err() should be set after exhaustion")
	}
}

func TestStreamReplay_TypeMismatchError(t *testing.T) {
	log := []ReadLogEntry{
		ReadLogStorage{Value: uint256.NewInt(1)},
	}
	r := NewStreamReplayStateReader(log, NewCodeCache(8))
	// Asking for account when next entry is storage → mismatch
	if _, err := r.ReadAccountData(types.Address{}); err == nil {
		t.Fatal("expected type mismatch error")
	} else if !errors.Is(err, ErrReplayUnexpectedEntry) {
		t.Fatalf("expected ErrReplayUnexpectedEntry, got %v", err)
	}
}

func TestStreamReplay_CodeFromCache(t *testing.T) {
	codes := NewCodeCache(8)
	codeHash := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	codes.Insert(codeHash, []byte{0x60, 0x00, 0xf3})

	r := NewStreamReplayStateReader(nil, codes)
	code, err := r.ReadAccountCode(types.Address{0x1}, 0, codeHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 3 {
		t.Fatalf("code length = %d, want 3", len(code))
	}

	// EmptyCodeHash short-circuits without touching cache.
	code, err = r.ReadAccountCode(types.Address{0x1}, 0, emptyCodeHash)
	if err != nil {
		t.Fatal(err)
	}
	if code != nil {
		t.Fatalf("emptyCodeHash should return nil, got %x", code)
	}

	// Missing code is an error.
	missingHash := types.HexToHash("0xabcdef0000000000000000000000000000000000000000000000000000000000")
	if _, err := r.ReadAccountCode(types.Address{0x1}, 0, missingHash); err == nil {
		t.Fatal("expected missing-code error")
	}
}

func TestStreamReplay_ReadAccountIncarnationAlwaysZero(t *testing.T) {
	r := NewStreamReplayStateReader(nil, NewCodeCache(8))
	inc, err := r.ReadAccountIncarnation(types.Address{0x1})
	if err != nil {
		t.Fatal(err)
	}
	if inc != 0 {
		t.Fatalf("incarnation should be 0, got %d", inc)
	}
}

// Compile-time check that account.NewAccount() can be initialized via
// the cursor — used by ReadAccountData.
func TestStreamReplay_AccountInitialised(t *testing.T) {
	log := []ReadLogEntry{
		ReadLogAccount{Nonce: 7, Balance: uint256.NewInt(42), CodeHash: emptyCodeHash},
	}
	r := NewStreamReplayStateReader(log, NewCodeCache(8))
	a, err := r.ReadAccountData(types.Address{})
	if err != nil {
		t.Fatal(err)
	}
	var ref account.StateAccount
	if a == nil {
		t.Fatal("nil account")
	}
	if a.Nonce != 7 {
		t.Fatalf("nonce = %d, want 7", a.Nonce)
	}
	_ = ref // type assertion only
}

// ----------------------------------------------------------------------------
// VerificationReceipt
// ----------------------------------------------------------------------------

func TestVerificationReceipt_SigningMessageLayout(t *testing.T) {
	hash := types.HexToHash("0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	root := types.HexToHash("0x21222324252627282930313233343536373839404142434445464748495051525354555657585960616263")
	msg := BuildSigningMessage(hash, 0xdeadbeef, root)
	if len(msg) != 72 {
		t.Fatalf("signing msg length = %d, want 72", len(msg))
	}
	// First 32 bytes = block hash
	for i := 0; i < 32; i++ {
		if msg[i] != hash[i] {
			t.Fatalf("byte %d: got %02x want %02x", i, msg[i], hash[i])
		}
	}
	// Bytes 32..40 = block number, little endian
	if msg[32] != 0xef || msg[33] != 0xbe || msg[34] != 0xad || msg[35] != 0xde {
		t.Fatalf("block number bytes wrong: %02x%02x%02x%02x", msg[32], msg[33], msg[34], msg[35])
	}
	// Bytes 40..72 = receipts root
	for i := 0; i < 32; i++ {
		if msg[40+i] != root[i] {
			t.Fatalf("root byte %d: got %02x want %02x", i, msg[40+i], root[i])
		}
	}
}

// ----------------------------------------------------------------------------
// ExecuteAndVerifyV2
// ----------------------------------------------------------------------------

func TestExecuteAndVerifyV2_EmptyBlock(t *testing.T) {
	hdr := minimalHeader(t, 100, emptyEthTrieRoot)
	pkt := emptyPacket(t, hdr)

	codes := NewCodeCache(16)
	res, err := ExecuteAndVerifyV2(pkt, codes, nil)
	if err != nil {
		t.Fatalf("ExecuteAndVerifyV2: %v", err)
	}
	if res.BlockNumber != 100 {
		t.Errorf("blockNumber = %d, want 100", res.BlockNumber)
	}
	if res.TxCount != 0 {
		t.Errorf("txCount = %d, want 0", res.TxCount)
	}
	if res.WitnessReadLogLen != 0 {
		t.Errorf("readLogLen = %d, want 0", res.WitnessReadLogLen)
	}
	if res.ComputedReceiptsRoot != emptyEthTrieRoot {
		t.Errorf("computed root = %x, want %x", res.ComputedReceiptsRoot, emptyEthTrieRoot)
	}
	if res.BlockHash != hdr.Hash() {
		t.Errorf("block hash mismatch")
	}
}

func TestExecuteAndVerifyV2_HeaderHashMismatch(t *testing.T) {
	hdr := minimalHeader(t, 100, emptyEthTrieRoot)
	pkt := emptyPacket(t, hdr)
	// Corrupt the packet's BlockHash so it no longer matches header.Hash().
	pkt.BlockHash = types.HexToHash("0xfacefacefacefacefacefacefacefacefacefacefacefacefacefacefaceface")

	if _, err := ExecuteAndVerifyV2(pkt, NewCodeCache(8), nil); err == nil {
		t.Fatal("expected header hash mismatch error")
	}
}

func TestExecuteAndVerifyV2_ReceiptRootMismatch(t *testing.T) {
	// Set ReceiptHash in the header to a value that re-execution won't produce.
	wrongRoot := types.HexToHash("0xbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbadbad0")
	hdr := minimalHeader(t, 100, wrongRoot)
	pkt := emptyPacket(t, hdr)

	_, err := ExecuteAndVerifyV2(pkt, NewCodeCache(8), nil)
	if err == nil {
		t.Fatal("expected receipt root mismatch error")
	}
	if !errors.Is(err, ErrReceiptRootMismatch) {
		t.Fatalf("expected ErrReceiptRootMismatch, got %v", err)
	}
}

func TestExecuteAndVerifyV2_NilPacket(t *testing.T) {
	if _, err := ExecuteAndVerifyV2(nil, NewCodeCache(8), nil); err == nil {
		t.Fatal("expected nil packet error")
	}
}

func TestExecuteAndVerifyV2_NilCodeCache(t *testing.T) {
	hdr := minimalHeader(t, 1, emptyEthTrieRoot)
	pkt := emptyPacket(t, hdr)
	if _, err := ExecuteAndVerifyV2(pkt, nil, nil); err == nil {
		t.Fatal("expected nil code cache error")
	}
}

func TestExecuteAndVerifyV2_TamperedReadLog(t *testing.T) {
	hdr := minimalHeader(t, 100, emptyEthTrieRoot)
	pkt := emptyPacket(t, hdr)
	// Inject a stray read log entry. With no txs the EVM never calls the
	// reader, so the cursor will not advance — the verify still passes.
	// This documents the current property: untouched cursor isn't an error.
	pkt.ReadLogData = EncodeReadLog([]ReadLogEntry{
		ReadLogStorage{Value: uint256.NewInt(99)},
	})

	if _, err := ExecuteAndVerifyV2(pkt, NewCodeCache(8), nil); err != nil {
		t.Fatalf("empty block with extra read log entry should still verify: %v", err)
	}
}
