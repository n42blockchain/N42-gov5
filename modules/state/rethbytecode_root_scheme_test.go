// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

func TestDefaultScheme(t *testing.T) {
	if got := DefaultScheme(""); got != RootSchemeLegacyKeccak {
		t.Fatalf("DefaultScheme(\"\") = %v, want %v", got, RootSchemeLegacyKeccak)
	}
	if got := DefaultScheme("qmdb"); got != RootSchemeQMDB {
		t.Fatalf("DefaultScheme(qmdb) = %v, want %v", got, RootSchemeQMDB)
	}
	if got := DefaultScheme("custom-scheme"); got != RootScheme("custom-scheme") {
		t.Fatalf("DefaultScheme(custom) = %v, want pass-through", got)
	}
}

func TestDecodeRethBytecodeRaw(t *testing.T) {
	raw := []byte{0x60, 0x01, 0x60, 0x02}
	hash := crypto.Keccak256Hash(raw)
	if got := decodeRethBytecode(hash, raw); string(got) != string(raw) {
		t.Fatalf("decodeRethBytecode(raw) = %x, want %x", got, raw)
	}
}

func TestDecodeRethBytecodeLegacyWrapped(t *testing.T) {
	stored := []byte{0x60, 0x01, 0x60, 0x02, 0x00, 0x00, 0x00} // 4 real bytes + 3 padding
	hash := crypto.Keccak256Hash(stored[:4])

	wrapped := make([]byte, 4+len(stored))
	binary.BigEndian.PutUint32(wrapped[0:4], uint32(len(stored)))
	copy(wrapped[4:], stored)

	got := decodeRethBytecode(hash, wrapped)
	if string(got) != string(stored[:4]) {
		t.Fatalf("decodeRethBytecode(legacy) = %x, want %x", got, stored[:4])
	}
}

func TestDecodeRethBytecodeEip7702Delegation(t *testing.T) {
	designator := append([]byte{0xef, 0x01, 0x00}, make([]byte, 20)...)
	designator[5] = 0xAB
	hash := crypto.Keccak256Hash(designator)

	// Wrap with some header bytes before the designator.
	wrapped := append([]byte{0x01, 0x02}, designator...)
	got := decodeRethBytecode(hash, wrapped)
	if string(got) != string(designator) {
		t.Fatalf("decodeRethBytecode(eip7702) = %x, want %x", got, designator)
	}
}

func TestDecodeRethBytecodeShortSubstringAndUndecodable(t *testing.T) {
	inner := []byte{0x01, 0x02, 0x03}
	hash := crypto.Keccak256Hash(inner)
	wrapped := append([]byte{0xAA, 0xBB}, inner...)
	wrapped = append(wrapped, 0xCC)

	got := decodeRethBytecode(hash, wrapped)
	if string(got) != string(inner) {
		t.Fatalf("decodeRethBytecode(short substring) = %x, want %x", got, inner)
	}

	// Nothing matches -> nil.
	if got := decodeRethBytecode(hash, []byte{0x99, 0x98, 0x97}); got != nil {
		t.Fatalf("decodeRethBytecode(no match) = %x, want nil", got)
	}
}

func TestCodeFromTable(t *testing.T) {
	if got := codeFromTable([32]byte{}, nil); got != nil {
		t.Fatalf("codeFromTable(empty) = %v, want nil", got)
	}

	raw := []byte{0x60, 0x0A}
	hash := crypto.Keccak256Hash(raw)
	if got := codeFromTable(hash, raw); string(got) != string(raw) {
		t.Fatalf("codeFromTable(already raw) = %x, want %x", got, raw)
	}

	// Wrapped form: decoded, cached, and the cache is hit on a second call.
	stored := []byte{0x61, 0x0B, 0x00}
	wrappedHash := crypto.Keccak256Hash(stored[:2])
	wrapped := make([]byte, 4+len(stored))
	binary.BigEndian.PutUint32(wrapped[0:4], uint32(len(stored)))
	copy(wrapped[4:], stored)

	got := codeFromTable(wrappedHash, wrapped)
	if string(got) != string(stored[:2]) {
		t.Fatalf("codeFromTable(wrapped) = %x, want %x", got, stored[:2])
	}
	got2 := codeFromTable(wrappedHash, wrapped)
	if string(got2) != string(stored[:2]) {
		t.Fatalf("codeFromTable(cached) = %x, want %x", got2, stored[:2])
	}

	// Undecodable: best-effort returns the raw bytes as-is.
	undecodable := []byte{0x11, 0x22, 0x33}
	if got := codeFromTable([32]byte{0x01}, undecodable); string(got) != string(undecodable) {
		t.Fatalf("codeFromTable(undecodable) = %x, want %x (best effort)", got, undecodable)
	}
}
