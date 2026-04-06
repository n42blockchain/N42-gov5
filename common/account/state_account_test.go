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

package account

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// =============================================================================
// NewAccount Tests
// =============================================================================

func TestNewAccount(t *testing.T) {
	acc := NewAccount()

	if acc.Initialised {
		t.Error("NewAccount should not be initialized")
	}
	if acc.Nonce != 0 {
		t.Error("NewAccount should have zero nonce")
	}
	if !acc.Balance.IsZero() {
		t.Error("NewAccount should have zero balance")
	}
	if acc.IsEmptyRoot() == false {
		t.Error("NewAccount should have empty root")
	}
	if acc.IsEmptyCodeHash() == false {
		t.Error("NewAccount should have empty code hash")
	}

	t.Logf("✓ NewAccount creates account with correct defaults")
}

// =============================================================================
// Copy Tests
// =============================================================================

func TestStateAccountCopy(t *testing.T) {
	original := StateAccount{
		Initialised: true,
		Nonce:       100,
	}
	original.Balance.SetUint64(1000000000000000000)
	original.Root = types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	original.CodeHash = types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

	var copied StateAccount
	copied.Copy(&original)

	if copied.Initialised != original.Initialised {
		t.Error("Copy: Initialised mismatch")
	}
	if copied.Nonce != original.Nonce {
		t.Error("Copy: Nonce mismatch")
	}
	if copied.Balance.Cmp(&original.Balance) != 0 {
		t.Error("Copy: Balance mismatch")
	}
	if copied.Root != original.Root {
		t.Error("Copy: Root mismatch")
	}
	if copied.CodeHash != original.CodeHash {
		t.Error("Copy: CodeHash mismatch")
	}

	// Verify deep copy - modifying copied shouldn't affect original
	copied.Nonce = 999
	if original.Nonce == 999 {
		t.Error("Copy should create deep copy")
	}

	t.Logf("✓ StateAccount.Copy works correctly")
}

func TestSelfCopy(t *testing.T) {
	original := StateAccount{
		Initialised: true,
		Nonce:       50,
	}
	original.Balance.SetUint64(5000000000)

	copied := original.SelfCopy()

	if copied == nil {
		t.Fatal("SelfCopy should not return nil")
	}
	if copied.Nonce != original.Nonce {
		t.Error("SelfCopy: Nonce mismatch")
	}
	if copied.Balance.Cmp(&original.Balance) != 0 {
		t.Error("SelfCopy: Balance mismatch")
	}

	// Verify it's a new pointer
	copied.Nonce = 999
	if original.Nonce == 999 {
		t.Error("SelfCopy should return new pointer")
	}

	t.Logf("✓ StateAccount.SelfCopy works correctly")
}

// =============================================================================
// Reset Tests
// =============================================================================

func TestReset(t *testing.T) {
	acc := StateAccount{
		Initialised: false,
		Nonce:       100,
	}
	acc.Balance.SetUint64(1000000000)
	acc.Root = types.HexToHash("0x1234")
	acc.CodeHash = types.HexToHash("0xabcd")

	acc.Reset()

	if !acc.Initialised {
		t.Error("Reset should set Initialised to true")
	}
	if acc.Nonce != 0 {
		t.Error("Reset should set Nonce to 0")
	}
	if !acc.Balance.IsZero() {
		t.Error("Reset should set Balance to zero")
	}
	if !acc.IsEmptyRoot() {
		t.Error("Reset should set empty root")
	}
	if !acc.IsEmptyCodeHash() {
		t.Error("Reset should set empty code hash")
	}

	t.Logf("✓ StateAccount.Reset works correctly")
}

// =============================================================================
// IsEmpty Tests
// =============================================================================

func TestIsEmptyCodeHash(t *testing.T) {
	acc := NewAccount()

	if !acc.IsEmptyCodeHash() {
		t.Error("NewAccount should have empty code hash")
	}

	acc.CodeHash = types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	if acc.IsEmptyCodeHash() {
		t.Error("Non-empty code hash should not be considered empty")
	}

	acc.CodeHash = types.Hash{}
	if !acc.IsEmptyCodeHash() {
		t.Error("Zero hash should be considered empty")
	}

	t.Logf("✓ IsEmptyCodeHash works correctly")
}

func TestIsEmptyCodeHashFunction(t *testing.T) {
	// Test the package-level function
	if !IsEmptyCodeHash(types.Hash{}) {
		t.Error("Zero hash should be empty")
	}

	nonEmpty := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	if IsEmptyCodeHash(nonEmpty) {
		t.Error("Non-zero hash should not be empty")
	}

	t.Logf("✓ IsEmptyCodeHash function works correctly")
}

func TestIsEmptyRoot(t *testing.T) {
	acc := NewAccount()

	if !acc.IsEmptyRoot() {
		t.Error("NewAccount should have empty root")
	}

	acc.Root = types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	if acc.IsEmptyRoot() {
		t.Error("Non-empty root should not be considered empty")
	}

	acc.Root = types.Hash{}
	if !acc.IsEmptyRoot() {
		t.Error("Zero hash should be considered empty root")
	}

	t.Logf("✓ IsEmptyRoot works correctly")
}

// =============================================================================
// Equals Tests
// =============================================================================

func TestEquals(t *testing.T) {
	codeHash1 := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	codeHash2 := types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

	acc1 := StateAccount{
		Nonce: 100,
	}
	acc1.Balance.SetUint64(1000)
	acc1.CodeHash = codeHash1

	acc2 := StateAccount{
		Nonce: 100,
	}
	acc2.Balance.SetUint64(1000)
	acc2.CodeHash = codeHash1

	if !acc1.Equals(&acc2) {
		t.Error("Identical accounts should be equal")
	}

	// Test different nonce
	acc2.Nonce = 101
	if acc1.Equals(&acc2) {
		t.Error("Accounts with different nonce should not be equal")
	}
	acc2.Nonce = 100

	// Test different balance
	acc2.Balance.SetUint64(2000)
	if acc1.Equals(&acc2) {
		t.Error("Accounts with different balance should not be equal")
	}
	acc2.Balance.SetUint64(1000)

	// Test different code hash
	acc2.CodeHash = codeHash2
	if acc1.Equals(&acc2) {
		t.Error("Accounts with different code hash should not be equal")
	}
	acc2.CodeHash = codeHash1

	t.Logf("✓ Equals works correctly")
}

// =============================================================================
// Marshal/Unmarshal Tests
// =============================================================================

func TestMarshalUnmarshal(t *testing.T) {
	original := StateAccount{
		Initialised: true,
		Nonce: 100,
	}
	original.Balance.SetUint64(1000000000000000000)
	original.Root = types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	original.CodeHash = types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Marshal should return non-empty data")
	}

	var decoded StateAccount
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if decoded.Initialised != original.Initialised {
		t.Error("Marshal/Unmarshal: Initialised mismatch")
	}
	if decoded.Nonce != original.Nonce {
		t.Error("Marshal/Unmarshal: Nonce mismatch")
	}
	if decoded.Balance.Cmp(&original.Balance) != 0 {
		t.Error("Marshal/Unmarshal: Balance mismatch")
	}
	if decoded.Root != original.Root {
		t.Error("Marshal/Unmarshal: Root mismatch")
	}
	if decoded.CodeHash != original.CodeHash {
		t.Error("Marshal/Unmarshal: CodeHash mismatch")
	}

	t.Logf("✓ Marshal/Unmarshal roundtrip works correctly")
}

func TestUnmarshalInvalidData(t *testing.T) {
	var acc StateAccount
	err := acc.Unmarshal([]byte{0x00, 0x01, 0x02}) // Invalid protobuf

	if err == nil {
		t.Error("Unmarshal should fail with invalid data")
	}

	t.Logf("✓ Unmarshal correctly rejects invalid data")
}

// =============================================================================
// EncodeForStorage/DecodeForStorage Tests
// =============================================================================

func TestEncodeDecodeForStorage(t *testing.T) {
	original := StateAccount{
		Initialised: true,
		Nonce: 100,
	}
	original.Balance.SetUint64(1000000000000000000)
	original.Root = types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	original.CodeHash = types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890")

	length := original.EncodingLengthForStorage()
	if length == 0 {
		t.Error("EncodingLengthForStorage should return non-zero length")
	}

	buffer := make([]byte, length)
	original.EncodeForStorage(buffer)

	var decoded StateAccount
	err := decoded.DecodeForStorage(buffer)
	if err != nil {
		t.Fatalf("DecodeForStorage error: %v", err)
	}

	if decoded.Nonce != original.Nonce {
		t.Error("EncodeForStorage/DecodeForStorage: Nonce mismatch")
	}
	if decoded.Balance.Cmp(&original.Balance) != 0 {
		t.Error("EncodeForStorage/DecodeForStorage: Balance mismatch")
	}

	t.Logf("✓ EncodeForStorage/DecodeForStorage roundtrip works correctly")
}

func TestDecodeForStorageEmpty(t *testing.T) {
	var acc StateAccount
	err := acc.DecodeForStorage([]byte{})

	if err != nil {
		t.Errorf("DecodeForStorage with empty data should not error: %v", err)
	}

	// Should be reset to defaults
	if !acc.Initialised {
		t.Error("DecodeForStorage should initialize account")
	}
	if acc.Nonce != 0 {
		t.Error("DecodeForStorage with empty data should have zero nonce")
	}

	t.Logf("✓ DecodeForStorage handles empty data correctly")
}

// =============================================================================
// ToProtoMessage/FromProtoMessage Tests
// =============================================================================

func TestToProtoMessage(t *testing.T) {
	acc := StateAccount{
		Initialised: true,
		Nonce:       100,
	}
	acc.Balance.SetUint64(1000)

	proto := acc.ToProtoMessage()
	if proto == nil {
		t.Error("ToProtoMessage should not return nil")
	}

	t.Logf("✓ ToProtoMessage works correctly")
}

func TestFromProtoMessage(t *testing.T) {
	original := StateAccount{
		Initialised: true,
		Nonce:       100,
	}
	original.Balance.SetUint64(1000)

	proto := original.ToProtoMessage()

	var decoded StateAccount
	err := decoded.FromProtoMessage(proto)
	if err != nil {
		t.Fatalf("FromProtoMessage error: %v", err)
	}

	if decoded.Nonce != original.Nonce {
		t.Error("FromProtoMessage: Nonce mismatch")
	}
	if decoded.Balance.Cmp(&original.Balance) != 0 {
		t.Error("FromProtoMessage: Balance mismatch")
	}

	t.Logf("✓ FromProtoMessage works correctly")
}

func TestFromProtoMessageTypeError(t *testing.T) {
	var acc StateAccount
	err := acc.FromProtoMessage(nil)

	if err == nil {
		t.Error("FromProtoMessage should fail with nil message")
	}

	t.Logf("✓ FromProtoMessage correctly rejects nil message")
}

// =============================================================================
// Constants Tests
// =============================================================================

func TestMimetypeConstants(t *testing.T) {
	constants := []string{
		MimetypeDataWithValidator,
		MimetypeTypedData,
		MimetypeClique,
		MimetypeParlia,
		MimetypeBor,
		MimetypeTextPlain,
	}

	for _, c := range constants {
		if c == "" {
			t.Error("Mimetype constant should not be empty")
		}
	}

	t.Logf("✓ Mimetype constants are defined correctly")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkNewAccount(b *testing.B) {
	for i := 0; i < b.N; i++ {
		NewAccount()
	}
}

func BenchmarkStateAccountCopy(b *testing.B) {
	original := StateAccount{
		Initialised: true,
		Nonce:       100,
	}
	original.Balance.SetUint64(1000000000000000000)

	var copied StateAccount
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		copied.Copy(&original)
	}
}

func BenchmarkMarshal(b *testing.B) {
	acc := StateAccount{
		Initialised: true,
		Nonce:       100,
	}
	acc.Balance.SetUint64(1000000000000000000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		acc.Marshal()
	}
}

func BenchmarkEquals(b *testing.B) {
	acc1 := StateAccount{Nonce: 100}
	acc1.Balance.SetUint64(1000)
	acc2 := StateAccount{Nonce: 100}
	acc2.Balance.SetUint64(1000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		acc1.Equals(&acc2)
	}
}
