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

package crypto

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

func TestKeccak256(t *testing.T) {
	tests := []struct {
		name   string
		input  [][]byte
		expect string
	}{
		{
			name:   "empty input",
			input:  [][]byte{},
			expect: "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
		},
		{
			name:   "single input",
			input:  [][]byte{[]byte("hello")},
			expect: "1c8aff950685c2ed4bc3174f3472287b56d9517b9c948127319a09a7a36deac8",
		},
		{
			name:   "multiple inputs",
			input:  [][]byte{[]byte("hello"), []byte("world")},
			expect: "fa26db7ca85ead399216e7c6316bc50ed24393c3122b582735e7f3b0f91b93f0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Keccak256(tt.input...)
			got := hex.EncodeToString(result)
			if got != tt.expect {
				t.Errorf("Keccak256() = %s, want %s", got, tt.expect)
			}
		})
	}
}

func TestKeccak256Hash(t *testing.T) {
	data := []byte("test data")
	hash := Keccak256Hash(data)

	// Verify it returns the same result as Keccak256
	expected := Keccak256(data)
	if !bytes.Equal(hash[:], expected) {
		t.Errorf("Keccak256Hash result doesn't match Keccak256")
	}

	// Verify it's 32 bytes
	if len(hash) != 32 {
		t.Errorf("Keccak256Hash should return 32 bytes, got %d", len(hash))
	}
}

func TestKeccak512(t *testing.T) {
	data := []byte("test data")
	hash := Keccak512(data)

	// Keccak512 should return 64 bytes
	if len(hash) != 64 {
		t.Errorf("Keccak512 should return 64 bytes, got %d", len(hash))
	}

	// Verify deterministic
	hash2 := Keccak512(data)
	if !bytes.Equal(hash, hash2) {
		t.Error("Keccak512 should be deterministic")
	}
}

func TestNewKeccakState(t *testing.T) {
	state := NewKeccakState()
	if state == nil {
		t.Fatal("NewKeccakState() returned nil")
	}

	// Test basic operations
	state.Write([]byte("test"))
	sum := state.Sum(nil)
	if len(sum) != 32 {
		t.Errorf("expected 32 bytes, got %d", len(sum))
	}
}

func TestHashData(t *testing.T) {
	state := NewKeccakState()
	data := []byte("test data")

	hash := HashData(state, data)

	// Verify the hash is correct
	expected := Keccak256Hash(data)
	if hash != expected {
		t.Errorf("HashData result doesn't match Keccak256Hash")
	}

	// Verify state can be reused (Reset is called)
	hash2 := HashData(state, data)
	if hash != hash2 {
		t.Error("HashData should be deterministic with the same state")
	}
}

func TestEmptyCodeHash(t *testing.T) {
	// EmptyCodeHash should be Keccak256 of nil/empty
	expected := Keccak256Hash(nil)
	if EmptyCodeHash != expected {
		t.Errorf("EmptyCodeHash = %x, want %x", EmptyCodeHash, expected)
	}
}

func TestCreateAddress(t *testing.T) {
	addr := types.HexToAddress("0x970e8128ab834e8eac17ab8e3812f010678cf791")

	// Test with different nonces
	tests := []struct {
		nonce uint64
	}{
		{0},
		{1},
		{100},
		{^uint64(0)}, // max uint64
	}

	for _, tt := range tests {
		result := CreateAddress(addr, tt.nonce)
		if len(result) != 20 {
			t.Errorf("CreateAddress() should return 20 bytes address")
		}
	}

	// Verify deterministic
	addr1 := CreateAddress(addr, 1)
	addr2 := CreateAddress(addr, 1)
	if addr1 != addr2 {
		t.Error("CreateAddress should be deterministic")
	}

	// Different nonces should produce different addresses
	addr3 := CreateAddress(addr, 2)
	if addr1 == addr3 {
		t.Error("Different nonces should produce different addresses")
	}
}

func TestCreateAddress2(t *testing.T) {
	addr := types.HexToAddress("0x970e8128ab834e8eac17ab8e3812f010678cf791")
	salt := [32]byte{1, 2, 3, 4}
	initHash := Keccak256([]byte("contract code"))

	result := CreateAddress2(addr, salt, initHash)
	if len(result) != 20 {
		t.Errorf("CreateAddress2() should return 20 bytes address")
	}

	// Verify deterministic
	result2 := CreateAddress2(addr, salt, initHash)
	if result != result2 {
		t.Error("CreateAddress2 should be deterministic")
	}

	// Different salt should produce different address
	salt2 := [32]byte{5, 6, 7, 8}
	result3 := CreateAddress2(addr, salt2, initHash)
	if result == result3 {
		t.Error("Different salt should produce different address")
	}
}

func TestGenerateKey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	if key == nil {
		t.Fatal("GenerateKey() returned nil key")
	}

	// Verify the key is valid
	if key.D == nil || key.D.Sign() <= 0 {
		t.Error("Generated key has invalid D value")
	}
	if key.PublicKey.X == nil || key.PublicKey.Y == nil {
		t.Error("Generated key has invalid public key")
	}
}

func TestToECDSA(t *testing.T) {
	// Generate a valid key first
	genKey, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	// Export and re-import
	keyBytes := FromECDSA(genKey)
	importedKey, err := ToECDSA(keyBytes)
	if err != nil {
		t.Fatalf("ToECDSA() error = %v", err)
	}

	// Verify keys match
	if importedKey.D.Cmp(genKey.D) != 0 {
		t.Error("Imported key D value doesn't match original")
	}

	// Test invalid inputs
	tests := []struct {
		name    string
		input   []byte
		wantErr bool
	}{
		{
			name:    "too short",
			input:   make([]byte, 31),
			wantErr: true,
		},
		{
			name:    "too long",
			input:   make([]byte, 33),
			wantErr: true,
		},
		{
			name:    "all zeros",
			input:   make([]byte, 32),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ToECDSA(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ToECDSA() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestToECDSAUnsafe(t *testing.T) {
	// Generate a valid key
	genKey, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	keyBytes := FromECDSA(genKey)
	importedKey := ToECDSAUnsafe(keyBytes)

	if importedKey == nil {
		t.Fatal("ToECDSAUnsafe returned nil for valid input")
	}

	if importedKey.D.Cmp(genKey.D) != 0 {
		t.Error("Imported key D value doesn't match original")
	}

	// Test with invalid input - should not panic, just return nil or invalid key
	invalid := ToECDSAUnsafe(make([]byte, 32))
	// For all zeros, it should return nil because D would be 0
	if invalid != nil {
		t.Log("ToECDSAUnsafe returned non-nil for zero input")
	}
}

func TestFromECDSA(t *testing.T) {
	// Test with nil
	result := FromECDSA(nil)
	if result != nil {
		t.Error("FromECDSA(nil) should return nil")
	}

	// Test with valid key
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	keyBytes := FromECDSA(key)
	if len(keyBytes) != 32 {
		t.Errorf("FromECDSA should return 32 bytes, got %d", len(keyBytes))
	}
}

func TestMarshalUnmarshalPubkey(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	// Test MarshalPubkey (64 bytes format)
	pubBytes := MarshalPubkey(&key.PublicKey)
	if len(pubBytes) != 64 {
		t.Errorf("MarshalPubkey should return 64 bytes, got %d", len(pubBytes))
	}

	// Test MarshalPubkeyStd (65 bytes format)
	pubBytesStd := MarshalPubkeyStd(&key.PublicKey)
	if len(pubBytesStd) != 65 {
		t.Errorf("MarshalPubkeyStd should return 65 bytes, got %d", len(pubBytesStd))
	}

	// First byte should be 0x04
	if pubBytesStd[0] != 0x04 {
		t.Errorf("MarshalPubkeyStd first byte should be 0x04, got 0x%x", pubBytesStd[0])
	}

	// Test UnmarshalPubkey (from 65 bytes format)
	recoveredPub, err := UnmarshalPubkey(pubBytesStd)
	if err != nil {
		t.Fatalf("UnmarshalPubkey() error = %v", err)
	}

	if recoveredPub.X.Cmp(key.PublicKey.X) != 0 || recoveredPub.Y.Cmp(key.PublicKey.Y) != 0 {
		t.Error("UnmarshalPubkey didn't recover the correct public key")
	}

	// Test UnmarshalPubkeyStd
	recoveredPub2, err := UnmarshalPubkeyStd(pubBytesStd)
	if err != nil {
		t.Fatalf("UnmarshalPubkeyStd() error = %v", err)
	}

	if recoveredPub2.X.Cmp(key.PublicKey.X) != 0 || recoveredPub2.Y.Cmp(key.PublicKey.Y) != 0 {
		t.Error("UnmarshalPubkeyStd didn't recover the correct public key")
	}
}

func TestMarshalPubkeyNil(t *testing.T) {
	// Test with nil
	result := MarshalPubkey(nil)
	if result != nil {
		t.Error("MarshalPubkey(nil) should return nil")
	}

	resultStd := MarshalPubkeyStd(nil)
	if resultStd != nil {
		t.Error("MarshalPubkeyStd(nil) should return nil")
	}

	// Test with partial nil
	partialPub := &ecdsa.PublicKey{X: nil, Y: nil}
	result = MarshalPubkey(partialPub)
	if result != nil {
		t.Error("MarshalPubkey with nil X/Y should return nil")
	}
}

func TestUnmarshalPubkeyInvalid(t *testing.T) {
	// Test with invalid input
	_, err := UnmarshalPubkey([]byte{1, 2, 3})
	if err == nil {
		t.Error("UnmarshalPubkey should fail with invalid input")
	}

	_, err = UnmarshalPubkeyStd([]byte{1, 2, 3})
	if err == nil {
		t.Error("UnmarshalPubkeyStd should fail with invalid input")
	}
}

func TestFromECDSAPub(t *testing.T) {
	// Test with nil
	result := FromECDSAPub(nil)
	if result != nil {
		t.Error("FromECDSAPub(nil) should return nil")
	}

	// Test with valid key
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	pubBytes := FromECDSAPub(&key.PublicKey)
	if len(pubBytes) != 65 {
		t.Errorf("FromECDSAPub should return 65 bytes, got %d", len(pubBytes))
	}
}

func TestHexToECDSA(t *testing.T) {
	// Generate a key and get its hex
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	keyHex := hex.EncodeToString(FromECDSA(key))

	// Parse it back
	parsedKey, err := HexToECDSA(keyHex)
	if err != nil {
		t.Fatalf("HexToECDSA() error = %v", err)
	}

	if parsedKey.D.Cmp(key.D) != 0 {
		t.Error("HexToECDSA didn't recover the correct key")
	}

	// Test invalid hex
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "invalid hex char",
			input:   "gg",
			wantErr: true,
		},
		{
			name:    "odd length hex",
			input:   "abc",
			wantErr: true,
		},
		{
			name:    "all zeros (invalid key)",
			input:   "0000000000000000000000000000000000000000000000000000000000000000",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := HexToECDSA(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("HexToECDSA() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSaveLoadECDSA(t *testing.T) {
	// Create temp file
	tmpDir := t.TempDir()
	keyFile := filepath.Join(tmpDir, "test.key")

	// Generate a key
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	// Save the key
	err = SaveECDSA(keyFile, key)
	if err != nil {
		t.Fatalf("SaveECDSA() error = %v", err)
	}

	// Verify file permissions (Unix only)
	info, err := os.Stat(keyFile)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("Key file permissions = %o, want 0600", info.Mode().Perm())
	}

	// Load the key
	loadedKey, err := LoadECDSA(keyFile)
	if err != nil {
		t.Fatalf("LoadECDSA() error = %v", err)
	}

	if loadedKey.D.Cmp(key.D) != 0 {
		t.Error("LoadECDSA didn't recover the correct key")
	}
}

func TestLoadECDSAErrors(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with non-existent file
	_, err := LoadECDSA(filepath.Join(tmpDir, "nonexistent.key"))
	if err == nil {
		t.Error("LoadECDSA should fail with non-existent file")
	}

	// Test with too short file
	shortFile := filepath.Join(tmpDir, "short.key")
	os.WriteFile(shortFile, []byte("abc"), 0600)
	_, err = LoadECDSA(shortFile)
	if err == nil {
		t.Error("LoadECDSA should fail with too short file")
	}

	// Test with file having extra content
	longFile := filepath.Join(tmpDir, "long.key")
	key, _ := GenerateKey()
	keyHex := hex.EncodeToString(FromECDSA(key))
	os.WriteFile(longFile, []byte(keyHex+"extra content"), 0600)
	_, err = LoadECDSA(longFile)
	if err == nil {
		t.Error("LoadECDSA should fail with extra content")
	}

	// Test with valid file + allowed newlines
	validFile := filepath.Join(tmpDir, "valid.key")
	os.WriteFile(validFile, []byte(keyHex+"\n\n"), 0600)
	_, err = LoadECDSA(validFile)
	if err != nil {
		t.Errorf("LoadECDSA should allow trailing newlines: %v", err)
	}
}

func TestValidateSignatureValues(t *testing.T) {
	tests := []struct {
		name      string
		v         byte
		r         *uint256.Int
		s         *uint256.Int
		homestead bool
		want      bool
	}{
		{
			name:      "valid signature",
			v:         0,
			r:         uint256.NewInt(1),
			s:         uint256.NewInt(1),
			homestead: false,
			want:      true,
		},
		{
			name:      "valid signature v=1",
			v:         1,
			r:         uint256.NewInt(1),
			s:         uint256.NewInt(1),
			homestead: false,
			want:      true,
		},
		{
			name:      "invalid v=2",
			v:         2,
			r:         uint256.NewInt(1),
			s:         uint256.NewInt(1),
			homestead: false,
			want:      false,
		},
		{
			name:      "r is zero",
			v:         0,
			r:         uint256.NewInt(0),
			s:         uint256.NewInt(1),
			homestead: false,
			want:      false,
		},
		{
			name:      "s is zero",
			v:         0,
			r:         uint256.NewInt(1),
			s:         uint256.NewInt(0),
			homestead: false,
			want:      false,
		},
		{
			name:      "homestead with valid low s",
			v:         0,
			r:         uint256.NewInt(1),
			s:         uint256.NewInt(1),
			homestead: true,
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ValidateSignatureValues(tt.v, tt.r, tt.s, tt.homestead)
			if got != tt.want {
				t.Errorf("ValidateSignatureValues() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPubkeyToAddress(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	addr := PubkeyToAddress(key.PublicKey)

	// Address should be 20 bytes
	if len(addr) != 20 {
		t.Errorf("Address should be 20 bytes, got %d", len(addr))
	}

	// Should be deterministic
	addr2 := PubkeyToAddress(key.PublicKey)
	if addr != addr2 {
		t.Error("PubkeyToAddress should be deterministic")
	}
}

func TestZeroBytes(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5}
	zeroBytes(data)

	for i, b := range data {
		if b != 0 {
			t.Errorf("zeroBytes didn't zero byte %d, got %d", i, b)
		}
	}
}

func TestConstants(t *testing.T) {
	if SignatureLength != 65 {
		t.Errorf("SignatureLength = %d, want 65", SignatureLength)
	}

	if RecoveryIDOffset != 64 {
		t.Errorf("RecoveryIDOffset = %d, want 64", RecoveryIDOffset)
	}

	if DigestLength != 32 {
		t.Errorf("DigestLength = %d, want 32", DigestLength)
	}
}
