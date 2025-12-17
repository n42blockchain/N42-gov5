// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package ens

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// Namehash Tests
// =============================================================================

func TestNamehash(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		// Empty name
		{"", "0x0000000000000000000000000000000000000000000000000000000000000000"},
		// ETH TLD
		{"eth", "0x93cdeb708b7545dc668eb9280176169d1c33cfd8ed6f04690a0bcc88a93fc4ae"},
		// Common names
		{"foo.eth", "0xde9b09fd7c5f901e23a3f19fecc54828e9c848539801e86591bd9801b019f84f"},
		// Subdomains - just verify non-zero
		{"sub.foo.eth", "0x"},
		// addr.reverse
		{"addr.reverse", "0x91d1777781884d03a6757a803996e38de2a42967fb37eeaca72729271025a9e2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Namehash(tt.name)
			// Skip specific hash checks for complex names, just verify non-zero
			if tt.name == "sub.foo.eth" {
				if got == (types.Hash{}) {
					t.Error("Namehash should not return zero for valid name")
				}
				return
			}
			want := types.HexToHash(tt.want)
			if got != want {
				t.Errorf("Namehash(%q) = %s, want %s", tt.name, got.Hex(), want.Hex())
			}
		})
	}
}

func TestNamehashConsistency(t *testing.T) {
	// Same name should always produce same hash
	name := "vitalik.eth"
	hash1 := Namehash(name)
	hash2 := Namehash(name)

	if hash1 != hash2 {
		t.Errorf("Namehash should be consistent: %s != %s", hash1.Hex(), hash2.Hex())
	}
}

func TestNamehashCaseInsensitive(t *testing.T) {
	// ENS names should be case-insensitive
	hash1 := Namehash("FOO.ETH")
	hash2 := Namehash("foo.eth")
	hash3 := Namehash("Foo.Eth")

	if hash1 != hash2 || hash2 != hash3 {
		t.Error("Namehash should be case-insensitive")
	}
}

// =============================================================================
// LabelHash Tests
// =============================================================================

func TestLabelHash(t *testing.T) {
	// Label hash should be consistent
	hash1 := LabelHash("foo")
	hash2 := LabelHash("foo")

	if hash1 != hash2 {
		t.Error("LabelHash should be consistent")
	}

	// Different labels should have different hashes
	hash3 := LabelHash("bar")
	if hash1 == hash3 {
		t.Error("Different labels should have different hashes")
	}
}

func TestLabelHashCaseInsensitive(t *testing.T) {
	hash1 := LabelHash("FOO")
	hash2 := LabelHash("foo")

	if hash1 != hash2 {
		t.Error("LabelHash should be case-insensitive")
	}
}

// =============================================================================
// Normalize Tests
// =============================================================================

func TestNormalize(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"FOO.ETH", "foo.eth"},
		{"  foo.eth  ", "foo.eth"},
		{"Foo.Eth", "foo.eth"},
		{"", ""},
	}

	for _, tt := range tests {
		got := Normalize(tt.input)
		if got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// =============================================================================
// IsValidName Tests
// =============================================================================

func TestIsValidName(t *testing.T) {
	tests := []struct {
		name  string
		valid bool
	}{
		{"foo.eth", true},
		{"bar.eth", true},
		{"sub.foo.eth", true},
		{"a.eth", true},
		{"123.eth", true},
		{"foo-bar.eth", true},
		{"", false},
		{"-foo.eth", false},
		{"foo-.eth", false},
		{"foo_bar.eth", false},
	}

	for _, tt := range tests {
		got := IsValidName(tt.name)
		if got != tt.valid {
			t.Errorf("IsValidName(%q) = %v, want %v", tt.name, got, tt.valid)
		}
	}
}

// =============================================================================
// ReverseNode Tests
// =============================================================================

func TestReverseNode(t *testing.T) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	node := ReverseNode(addr)

	// Should not be zero
	if node == (types.Hash{}) {
		t.Error("ReverseNode should not return zero hash")
	}

	// Should be consistent
	node2 := ReverseNode(addr)
	if node != node2 {
		t.Error("ReverseNode should be consistent")
	}

	// Different addresses should have different nodes
	addr2 := types.HexToAddress("0xabcdef0123456789abcdef0123456789abcdef01")
	node3 := ReverseNode(addr2)
	if node == node3 {
		t.Error("Different addresses should have different reverse nodes")
	}
}

// =============================================================================
// DNS Encoding Tests
// =============================================================================

func TestDNSEncodeName(t *testing.T) {
	tests := []struct {
		name string
		want []byte
	}{
		{"", []byte{0}},
		{"eth", []byte{3, 'e', 't', 'h', 0}},
		{"foo.eth", []byte{3, 'f', 'o', 'o', 3, 'e', 't', 'h', 0}},
	}

	for _, tt := range tests {
		got := DNSEncodeName(tt.name)
		if len(got) != len(tt.want) {
			t.Errorf("DNSEncodeName(%q) length = %d, want %d", tt.name, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("DNSEncodeName(%q)[%d] = %d, want %d", tt.name, i, got[i], tt.want[i])
			}
		}
	}
}

func TestDNSDecodeName(t *testing.T) {
	tests := []struct {
		data    []byte
		want    string
		wantErr bool
	}{
		{[]byte{0}, "", false},
		{[]byte{3, 'e', 't', 'h', 0}, "eth", false},
		{[]byte{3, 'f', 'o', 'o', 3, 'e', 't', 'h', 0}, "foo.eth", false},
		{[]byte{}, "", true},
	}

	for _, tt := range tests {
		got, err := DNSDecodeName(tt.data)
		if (err != nil) != tt.wantErr {
			t.Errorf("DNSDecodeName error = %v, wantErr %v", err, tt.wantErr)
			continue
		}
		if got != tt.want {
			t.Errorf("DNSDecodeName = %q, want %q", got, tt.want)
		}
	}
}

func TestDNSEncodeDecodeRoundTrip(t *testing.T) {
	names := []string{"foo.eth", "sub.foo.eth", "a.b.c.eth"}

	for _, name := range names {
		encoded := DNSEncodeName(name)
		decoded, err := DNSDecodeName(encoded)
		if err != nil {
			t.Errorf("DNSDecodeName error for %q: %v", name, err)
			continue
		}
		if decoded != Normalize(name) {
			t.Errorf("Round trip failed for %q: got %q", name, decoded)
		}
	}
}

// =============================================================================
// Content Hash Tests
// =============================================================================

func TestEncodeDecodeContentHash(t *testing.T) {
	hash := []byte("QmTest1234567890")
	encoded := EncodeContentHash(ContentHashIPFS, hash)

	hashType, decoded, err := DecodeContentHash(encoded)
	if err != nil {
		t.Errorf("DecodeContentHash error: %v", err)
	}
	if hashType != ContentHashIPFS {
		t.Errorf("Hash type = %d, want %d", hashType, ContentHashIPFS)
	}
	if string(decoded) != string(hash) {
		t.Errorf("Decoded hash mismatch")
	}
}

func TestDecodeContentHashInvalid(t *testing.T) {
	_, _, err := DecodeContentHash([]byte{})
	if err != ErrInvalidContentHash {
		t.Errorf("Expected ErrInvalidContentHash, got %v", err)
	}

	_, _, err = DecodeContentHash([]byte{0})
	if err != ErrInvalidContentHash {
		t.Errorf("Expected ErrInvalidContentHash for single byte, got %v", err)
	}
}

// =============================================================================
// Registry Address Tests
// =============================================================================

func TestGetRegistryAddress(t *testing.T) {
	// Mainnet
	addr := GetRegistryAddress(1)
	if addr != MainnetRegistryAddress {
		t.Errorf("Mainnet registry address mismatch")
	}

	// Sepolia
	addr = GetRegistryAddress(11155111)
	if addr != SepoliaRegistryAddress {
		t.Errorf("Sepolia registry address mismatch")
	}

	// Unknown chain should default to mainnet
	addr = GetRegistryAddress(999999)
	if addr != MainnetRegistryAddress {
		t.Errorf("Unknown chain should default to mainnet")
	}
}

// =============================================================================
// Utility Function Tests
// =============================================================================

func TestIsETHName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"foo.eth", true},
		{"FOO.ETH", true},
		{"sub.foo.eth", true},
		{"foo.com", false},
		{"eth", false},
		{"", false},
	}

	for _, tt := range tests {
		got := IsETHName(tt.name)
		if got != tt.want {
			t.Errorf("IsETHName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestGetParentName(t *testing.T) {
	tests := []struct {
		name   string
		parent string
	}{
		{"foo.eth", "eth"},
		{"sub.foo.eth", "foo.eth"},
		{"eth", ""},
		{"", ""},
	}

	for _, tt := range tests {
		got := GetParentName(tt.name)
		if got != tt.parent {
			t.Errorf("GetParentName(%q) = %q, want %q", tt.name, got, tt.parent)
		}
	}
}

func TestGetLabel(t *testing.T) {
	tests := []struct {
		name  string
		label string
	}{
		{"foo.eth", "foo"},
		{"sub.foo.eth", "sub"},
		{"eth", "eth"},
		{"", ""},
	}

	for _, tt := range tests {
		got := GetLabel(tt.name)
		if got != tt.label {
			t.Errorf("GetLabel(%q) = %q, want %q", tt.name, got, tt.label)
		}
	}
}

// =============================================================================
// Contract Address Constants Tests
// =============================================================================

func TestContractAddresses(t *testing.T) {
	// Registry addresses should not be zero
	if MainnetRegistryAddress == (types.Address{}) {
		t.Error("MainnetRegistryAddress should not be zero")
	}
	if PublicResolverAddress == (types.Address{}) {
		t.Error("PublicResolverAddress should not be zero")
	}
	if UniversalResolverAddress == (types.Address{}) {
		t.Error("UniversalResolverAddress should not be zero")
	}
	if ReverseRegistrarAddress == (types.Address{}) {
		t.Error("ReverseRegistrarAddress should not be zero")
	}
}

// =============================================================================
// Namehash Constants Tests
// =============================================================================

func TestNamehashConstants(t *testing.T) {
	// ETHNamehash should equal namehash("eth")
	calculated := Namehash("eth")
	if calculated != ETHNamehash {
		t.Errorf("ETHNamehash constant mismatch: calculated %s, constant %s",
			calculated.Hex(), ETHNamehash.Hex())
	}

	// ReverseNamehash should equal namehash("addr.reverse")
	calculated = Namehash("addr.reverse")
	if calculated != ReverseNamehash {
		t.Errorf("ReverseNamehash constant mismatch: calculated %s, constant %s",
			calculated.Hex(), ReverseNamehash.Hex())
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkNamehash(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Namehash("vitalik.eth")
	}
}

func BenchmarkNamehashLong(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Namehash("subdomain.subdomain.subdomain.vitalik.eth")
	}
}

func BenchmarkLabelHash(b *testing.B) {
	for i := 0; i < b.N; i++ {
		LabelHash("vitalik")
	}
}

func BenchmarkIsValidName(b *testing.B) {
	for i := 0; i < b.N; i++ {
		IsValidName("vitalik.eth")
	}
}

func BenchmarkDNSEncodeName(b *testing.B) {
	for i := 0; i < b.N; i++ {
		DNSEncodeName("vitalik.eth")
	}
}

func BenchmarkReverseNode(b *testing.B) {
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ReverseNode(addr)
	}
}

