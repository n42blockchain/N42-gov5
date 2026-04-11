// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func TestReadLog_EmptyRoundtrip(t *testing.T) {
	encoded := EncodeReadLog(nil)
	if len(encoded) != 4 {
		t.Fatalf("empty log encodes to %d bytes, want 4", len(encoded))
	}
	decoded, err := DecodeReadLog(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 0 {
		t.Fatalf("decoded %d entries, want 0", len(decoded))
	}
}

func TestReadLog_AccountNotFound(t *testing.T) {
	log := []ReadLogEntry{ReadLogAccountNotFound{}}
	encoded := EncodeReadLog(log)
	if len(encoded) != 5 {
		t.Fatalf("AccountNotFound encodes to %d bytes, want 5", len(encoded))
	}
	if encoded[4] != headerAccountNotFound {
		t.Fatalf("header byte = 0x%02x, want 0x00", encoded[4])
	}
	decoded, err := DecodeReadLog(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 1 {
		t.Fatalf("decoded %d entries, want 1", len(decoded))
	}
	if _, ok := decoded[0].(ReadLogAccountNotFound); !ok {
		t.Fatalf("decoded[0] = %T, want ReadLogAccountNotFound", decoded[0])
	}
}

func TestReadLog_EOAZeroValues(t *testing.T) {
	log := []ReadLogEntry{
		ReadLogAccount{Nonce: 0, Balance: uint256.NewInt(0), CodeHash: emptyCodeHash},
	}
	encoded := EncodeReadLog(log)
	// Header byte only — no nonce, no balance, no code hash bodies.
	if len(encoded) != 5 {
		t.Fatalf("zero EOA encodes to %d bytes, want 5", len(encoded))
	}
	decoded, err := DecodeReadLog(encoded)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded[0].(ReadLogAccount)
	if !ok {
		t.Fatalf("decoded[0] = %T", decoded[0])
	}
	if got.Nonce != 0 || !got.Balance.IsZero() || got.CodeHash != emptyCodeHash {
		t.Fatalf("zero EOA roundtrip: nonce=%d balance=%s code=%x",
			got.Nonce, got.Balance, got.CodeHash)
	}
}

func TestReadLog_AccountWithEverything(t *testing.T) {
	codeHash := types.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")
	balance := new(uint256.Int)
	_ = balance.SetFromHex("0x1234567890abcdef")
	log := []ReadLogEntry{
		ReadLogAccount{
			Nonce:    0xff_aabbccdd,
			Balance:  balance,
			CodeHash: codeHash,
		},
	}
	encoded := EncodeReadLog(log)
	decoded, err := DecodeReadLog(encoded)
	if err != nil {
		t.Fatal(err)
	}
	got := decoded[0].(ReadLogAccount)
	if got.Nonce != 0xff_aabbccdd {
		t.Fatalf("nonce roundtrip: got %x", got.Nonce)
	}
	if got.Balance.Cmp(balance) != 0 {
		t.Fatalf("balance roundtrip: got %s want %s", got.Balance, balance)
	}
	if got.CodeHash != codeHash {
		t.Fatalf("code hash roundtrip: got %x want %x", got.CodeHash, codeHash)
	}
}

func TestReadLog_StorageVariableLength(t *testing.T) {
	cases := []struct {
		name      string
		value     *uint256.Int
		expectLen int // total bytes for one entry
	}{
		{"zero", uint256.NewInt(0), 1},                                                    // header only
		{"one_byte", uint256.NewInt(0xab), 2},                                             // header + 1
		{"max_u64", uint256.NewInt(0xffffffffffffffff), 9},                                // header + 8
		{"big", new(uint256.Int).Lsh(uint256.NewInt(1), 200), 1 + (200/8 + 1)},            // header + 26
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := []ReadLogEntry{ReadLogStorage{Value: tc.value}}
			encoded := EncodeReadLog(log)
			if len(encoded) != 4+tc.expectLen {
				t.Fatalf("encoded len = %d, want %d", len(encoded), 4+tc.expectLen)
			}
			decoded, err := DecodeReadLog(encoded)
			if err != nil {
				t.Fatal(err)
			}
			got := decoded[0].(ReadLogStorage)
			if got.Value.Cmp(tc.value) != 0 {
				t.Fatalf("storage roundtrip: got %s want %s", got.Value, tc.value)
			}
		})
	}
}

func TestReadLog_BlockHash(t *testing.T) {
	hash := types.HexToHash("0xfeedface00000000000000000000000000000000000000000000000000000000")
	log := []ReadLogEntry{ReadLogBlockHash{Hash: hash}}
	encoded := EncodeReadLog(log)
	if len(encoded) != 4+1+32 {
		t.Fatalf("BlockHash encodes to %d bytes, want %d", len(encoded), 4+1+32)
	}
	decoded, err := DecodeReadLog(encoded)
	if err != nil {
		t.Fatal(err)
	}
	got := decoded[0].(ReadLogBlockHash)
	if got.Hash != hash {
		t.Fatalf("blockhash roundtrip: got %x want %x", got.Hash, hash)
	}
}

func TestReadLog_MixedSequence(t *testing.T) {
	codeHash := types.HexToHash("0xdeadbeef00000000000000000000000000000000000000000000000000000000")
	log := []ReadLogEntry{
		ReadLogAccount{Nonce: 5, Balance: uint256.NewInt(1000), CodeHash: codeHash},
		ReadLogStorage{Value: uint256.NewInt(42)},
		ReadLogAccountNotFound{},
		ReadLogBlockHash{Hash: types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")},
		ReadLogStorage{Value: uint256.NewInt(0)},
		ReadLogAccount{Nonce: 0, Balance: uint256.NewInt(0), CodeHash: emptyCodeHash},
	}
	encoded := EncodeReadLog(log)
	decoded, err := DecodeReadLog(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != len(log) {
		t.Fatalf("decoded %d entries, want %d", len(decoded), len(log))
	}
	for i := range log {
		if !reflect.DeepEqual(log[i], decoded[i]) {
			t.Errorf("entry %d mismatch: got %#v want %#v", i, decoded[i], log[i])
		}
	}
}

func TestReadLog_TruncatedInputs(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0x01, 0x00, 0x00},                   // count truncated
		{0x01, 0x00, 0x00, 0x00},             // count says 1 but body missing
		{0x01, 0x00, 0x00, 0x00, headerBlockHash}, // BlockHash header but no 32-byte body
	}
	for i, c := range cases {
		if _, err := DecodeReadLog(c); err == nil {
			t.Errorf("case %d: expected error, got nil", i)
		}
	}
}

func TestReadLog_EntryCountOverflow(t *testing.T) {
	bigCount := []byte{0xff, 0xff, 0xff, 0xff} // 4G entries
	if _, err := DecodeReadLog(bigCount); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestReadLog_HeaderByteUnchangedFromRust(t *testing.T) {
	// Sentinel checks: these specific byte values are the wire contract
	// with D:\N42\n42-26\crates\n42-execution\src\read_log.rs. If they
	// drift, mobile↔IDC packets will silently corrupt.
	if headerAccountNotFound != 0x00 {
		t.Fatal("AccountNotFound header changed")
	}
	if headerStorageBase != 0x80 {
		t.Fatal("Storage base header changed")
	}
	if headerBlockHash != 0xC0 {
		t.Fatal("BlockHash header changed")
	}
	if acctExistsBit != 0x01 {
		t.Fatal("acct exists bit changed")
	}
	if acctBalanceBit != 0x10 {
		t.Fatal("acct balance bit changed")
	}
	if acctCodeHashBit != 0x20 {
		t.Fatal("acct code hash bit changed")
	}
	if acctNonce8BBit != 0x40 {
		t.Fatal("acct nonce 8B bit changed")
	}
}

// Mini benchmark to make sure encode is allocation-friendly.
func TestReadLog_LargeBatch(t *testing.T) {
	const n = 1000
	log := make([]ReadLogEntry, 0, n)
	for i := 0; i < n; i++ {
		log = append(log, ReadLogStorage{Value: uint256.NewInt(uint64(i))})
	}
	encoded := EncodeReadLog(log)
	decoded, err := DecodeReadLog(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != n {
		t.Fatalf("batch size: got %d want %d", len(decoded), n)
	}
	// Spot check value 500.
	if decoded[500].(ReadLogStorage).Value.Uint64() != 500 {
		t.Fatal("batch entry 500 wrong")
	}
	if !bytes.Equal(encoded, EncodeReadLog(decoded)) {
		t.Fatal("re-encode mismatch")
	}
}
