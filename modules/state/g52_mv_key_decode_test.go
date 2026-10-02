// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestG52DecodeKeyTag covers the tag-extraction helper, including its
// empty-key error path -- exercised nowhere else in the suite.
func TestG52DecodeKeyTag(t *testing.T) {
	if _, err := DecodeKeyTag(nil); err == nil {
		t.Fatal("expected error decoding an empty key")
	}
	addr := types.HexToAddress("0x00000000000000000000000000000000000000f1")
	tag, err := DecodeKeyTag(EncodeAccountKey(addr))
	if err != nil || tag != mvKeyTagAccount {
		t.Fatalf("DecodeKeyTag(account) = %d, %v; want %d, nil", tag, err, mvKeyTagAccount)
	}
}

// TestG52DecodeAccountKey round-trips EncodeAccountKey and rejects
// non-account keys (wrong length, wrong tag).
func TestG52DecodeAccountKey(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000000000f2")
	got, ok := DecodeAccountKey(EncodeAccountKey(addr))
	if !ok || got != addr {
		t.Fatalf("DecodeAccountKey round-trip mismatch: got=%v ok=%v want=%v", got, ok, addr)
	}

	if _, ok := DecodeAccountKey(EncodeStorageKey(addr, types.HexToHash("0x01"))); ok {
		t.Fatal("DecodeAccountKey must reject a storage key")
	}
	if _, ok := DecodeAccountKey([]byte{mvKeyTagAccount}); ok {
		t.Fatal("DecodeAccountKey must reject a too-short key")
	}
}

// TestG52DecodeStorageKey round-trips EncodeStorageKey and rejects
// non-storage keys.
func TestG52DecodeStorageKey(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000000000f3")
	slot := types.HexToHash("0x02")
	gotAddr, gotSlot, ok := DecodeStorageKey(EncodeStorageKey(addr, slot))
	if !ok || gotAddr != addr || gotSlot != slot {
		t.Fatalf("DecodeStorageKey round-trip mismatch: addr=%v slot=%v ok=%v", gotAddr, gotSlot, ok)
	}

	if _, _, ok := DecodeStorageKey(EncodeAccountKey(addr)); ok {
		t.Fatal("DecodeStorageKey must reject an account key")
	}
	if _, _, ok := DecodeStorageKey([]byte{mvKeyTagStorage}); ok {
		t.Fatal("DecodeStorageKey must reject a too-short key")
	}
}

// TestG52ValidateStorageKey covers the pass branch (a well-formed storage
// key) and the wrong-tag/wrong-length fail branch via an account key of a
// *different* length than 53, which is the only fail input that doesn't
// trip the key[0] index.
//
// NOTE (found defect, not fixed per task scope): ValidateStorageKey(nil) or
// any key with len==0 panics with an out-of-range index. The guard clause
// `len(key) != 53 || key[0] != mvKeyTagStorage` short-circuits the boolean
// test correctly, but the function body still unconditionally evaluates
// key[0] as an fmt.Errorf format argument on the failing branch, so a
// zero-length key crashes instead of returning an error. See
// mv_evm_adapter.go:376-379.
func TestG52ValidateStorageKey(t *testing.T) {
	addr := types.HexToAddress("0x00000000000000000000000000000000000000f4")
	slot := types.HexToHash("0x03")

	if err := ValidateStorageKey(EncodeStorageKey(addr, slot)); err != nil {
		t.Fatalf("expected a valid storage key to pass: %v", err)
	}
	if err := ValidateStorageKey(EncodeAccountKey(addr)); err == nil {
		t.Fatal("expected ValidateStorageKey to reject an account-length key with the wrong tag")
	}
}

// TestG52AsUint64Hex pins the manual hex formatter against a few known
// values, including zero and the all-F max.
func TestG52AsUint64Hex(t *testing.T) {
	cases := []struct {
		v    uint64
		want string
	}{
		{0, "0000000000000000"},
		{1, "0000000000000001"},
		{0xdeadbeef, "00000000deadbeef"},
		{^uint64(0), "ffffffffffffffff"},
	}
	for _, c := range cases {
		got := asUint64Hex(c.v)
		if string(got[:]) != c.want {
			t.Fatalf("asUint64Hex(%d) = %q, want %q", c.v, got, c.want)
		}
	}
}
