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

package block

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

func TestBlockNonceSize(t *testing.T) {
	var nonce BlockNonce
	if len(nonce) != 8 {
		t.Errorf("BlockNonce size = %d, want 8", len(nonce))
	}
}

func TestEncodeNonce(t *testing.T) {
	tests := []struct {
		name  string
		input uint64
	}{
		{"zero", 0},
		{"one", 1},
		{"max_uint64", ^uint64(0)},
		{"typical", 12345678901234567890},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nonce := EncodeNonce(tt.input)
			result := nonce.Uint64()
			if result != tt.input {
				t.Errorf("EncodeNonce(%d).Uint64() = %d, want %d", tt.input, result, tt.input)
			}
		})
	}
}

func TestBlockNonceMarshalText(t *testing.T) {
	nonce := EncodeNonce(0x123456789ABCDEF0)

	text, err := nonce.MarshalText()
	if err != nil {
		t.Fatalf("BlockNonce.MarshalText error: %v", err)
	}
	if len(text) == 0 {
		t.Error("BlockNonce.MarshalText returned empty string")
	}
	if !bytes.HasPrefix(text, []byte("0x")) {
		t.Error("BlockNonce.MarshalText should start with 0x")
	}
}

func TestBlockNonceUnmarshalText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected uint64
	}{
		{"zeros", "0x0000000000000000", 0},
		{"ones", "0x0000000000000001", 1},
		{"full", "0xffffffffffffffff", ^uint64(0)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var nonce BlockNonce
			err := nonce.UnmarshalText([]byte(tt.input))
			if err != nil {
				t.Fatalf("UnmarshalText error: %v", err)
			}
			if nonce.Uint64() != tt.expected {
				t.Errorf("UnmarshalText(%s) = %d, want %d", tt.input, nonce.Uint64(), tt.expected)
			}
		})
	}
}

func TestBlockNonceMarshalUnmarshalRoundtrip(t *testing.T) {
	original := EncodeNonce(0xDEADBEEFCAFEBABE)

	text, err := original.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error: %v", err)
	}

	var decoded BlockNonce
	err = decoded.UnmarshalText(text)
	if err != nil {
		t.Fatalf("UnmarshalText error: %v", err)
	}

	if original != decoded {
		t.Errorf("Roundtrip failed: original=%x, decoded=%x", original, decoded)
	}
}

func TestHeaderNumber64(t *testing.T) {
	h := &Header{Number: uint256.NewInt(12345)}
	if h.Number64().Uint64() != 12345 {
		t.Errorf("Header.Number64() = %d, want 12345", h.Number64().Uint64())
	}
}

func TestHeaderStateRoot(t *testing.T) {
	expectedRoot := types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef")
	h := &Header{Root: expectedRoot, Number: uint256.NewInt(0)}
	if h.StateRoot() != expectedRoot {
		t.Errorf("Header.StateRoot() = %v, want %v", h.StateRoot(), expectedRoot)
	}
}

func TestHeaderBaseFee64(t *testing.T) {
	h := &Header{BaseFee: uint256.NewInt(1000000000), Number: uint256.NewInt(0)}
	if h.BaseFee64().Uint64() != 1000000000 {
		t.Errorf("Header.BaseFee64() = %d, want 1000000000", h.BaseFee64().Uint64())
	}
}

func TestHeaderBaseFee64Nil(t *testing.T) {
	h := &Header{Number: uint256.NewInt(0)}
	if h.BaseFee64() != nil {
		t.Errorf("Header.BaseFee64() should be nil for header without BaseFee")
	}
}

func TestHeaderHash(t *testing.T) {
	h := Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
		GasUsed:    21000,
		Time:       1234567890,
	}

	hash1 := h.Hash()
	if hash1 == (types.Hash{}) {
		t.Error("Header.Hash() should not return empty hash")
	}

	hash2 := h.Hash()
	if hash1 != hash2 {
		t.Error("Header.Hash() should return cached value")
	}
}

func TestHeaderHashDifferentHeaders(t *testing.T) {
	h1 := Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
	}

	h2 := Header{
		ParentHash: types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
	}

	if h1.Hash() == h2.Hash() {
		t.Error("Different headers should have different hashes")
	}
}

func TestHeaderHashNilFields(t *testing.T) {
	h := Header{Number: uint256.NewInt(0)}
	hash := h.Hash()
	if hash == (types.Hash{}) {
		t.Error("Header.Hash() should not return empty hash even with nil fields")
	}
}

func TestCopyHeader(t *testing.T) {
	original := &Header{
		ParentHash:  types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Coinbase:    types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Root:        types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		TxHash:      types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		ReceiptHash: types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Difficulty:  uint256.NewInt(1000000),
		Number:      uint256.NewInt(12345),
		GasLimit:    8000000,
		GasUsed:     5000000,
		Time:        1234567890,
		BaseFee:     uint256.NewInt(1000000000),
		Extra:       []byte("test extra data"),
		Nonce:       EncodeNonce(12345),
	}

	copied := CopyHeader(original)

	if copied.ParentHash != original.ParentHash {
		t.Error("CopyHeader: ParentHash mismatch")
	}
	if copied.Coinbase != original.Coinbase {
		t.Error("CopyHeader: Coinbase mismatch")
	}
	if copied.Number.Cmp(original.Number) != 0 {
		t.Error("CopyHeader: Number mismatch")
	}
	if copied.Difficulty.Cmp(original.Difficulty) != 0 {
		t.Error("CopyHeader: Difficulty mismatch")
	}
	if copied.BaseFee.Cmp(original.BaseFee) != 0 {
		t.Error("CopyHeader: BaseFee mismatch")
	}
	if !bytes.Equal(copied.Extra, original.Extra) {
		t.Error("CopyHeader: Extra mismatch")
	}

	copied.Number = uint256.NewInt(99999)
	if original.Number.Uint64() == 99999 {
		t.Error("CopyHeader should create a deep copy")
	}

	copied.Extra[0] = 0xFF
	if original.Extra[0] == 0xFF {
		t.Error("CopyHeader should deep copy Extra data")
	}
}

func TestCopyHeaderNilFields(t *testing.T) {
	original := &Header{Number: uint256.NewInt(100)}
	copied := CopyHeader(original)
	if copied.Number.Uint64() != 100 {
		t.Error("CopyHeader: Number mismatch")
	}
}

func TestCopyReward(t *testing.T) {
	rewards := []*Reward{
		{
			Address: types.HexToAddress("0x1234567890123456789012345678901234567890"),
			Amount:  uint256.NewInt(1000000000000000000),
		},
		{
			Address: types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"),
			Amount:  uint256.NewInt(2000000000000000000),
		},
	}

	copied := CopyReward(rewards)

	if len(copied) != len(rewards) {
		t.Errorf("CopyReward length mismatch: got %d, want %d", len(copied), len(rewards))
	}

	for i := range rewards {
		if copied[i].Address != rewards[i].Address {
			t.Errorf("CopyReward[%d].Address mismatch", i)
		}
		if copied[i].Amount.Cmp(rewards[i].Amount) != 0 {
			t.Errorf("CopyReward[%d].Amount mismatch", i)
		}
	}

	copied[0].Amount = uint256.NewInt(999)
	if rewards[0].Amount.Cmp(uint256.NewInt(999)) == 0 {
		t.Error("CopyReward should create a deep copy")
	}
}

func TestCopyRewardEmpty(t *testing.T) {
	rewards := []*Reward{}
	copied := CopyReward(rewards)
	if len(copied) != 0 {
		t.Errorf("CopyReward of empty slice should return empty, got length %d", len(copied))
	}
}

func TestHeaderMarshalUnmarshal(t *testing.T) {
	original := &Header{
		ParentHash:  types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Coinbase:    types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Root:        types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		TxHash:      types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		ReceiptHash: types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222"),
		Difficulty:  uint256.NewInt(1000000),
		Number:      uint256.NewInt(12345),
		GasLimit:    8000000,
		GasUsed:     5000000,
		Time:        1234567890,
		BaseFee:     uint256.NewInt(1000000000),
		Extra:       []byte("test extra data"),
		Nonce:       EncodeNonce(12345),
		MixDigest:   types.HexToHash("0x3333333333333333333333333333333333333333333333333333333333333333"),
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Header.Marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Header.Marshal returned empty data")
	}

	decoded := &Header{}
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Header.Unmarshal error: %v", err)
	}

	if decoded.ParentHash != original.ParentHash {
		t.Error("Marshal/Unmarshal: ParentHash mismatch")
	}
	if decoded.Coinbase != original.Coinbase {
		t.Error("Marshal/Unmarshal: Coinbase mismatch")
	}
	if decoded.Number.Cmp(original.Number) != 0 {
		t.Error("Marshal/Unmarshal: Number mismatch")
	}
	if decoded.GasLimit != original.GasLimit {
		t.Error("Marshal/Unmarshal: GasLimit mismatch")
	}
	if decoded.GasUsed != original.GasUsed {
		t.Error("Marshal/Unmarshal: GasUsed mismatch")
	}
	if decoded.Time != original.Time {
		t.Error("Marshal/Unmarshal: Time mismatch")
	}
	if decoded.Nonce.Uint64() != original.Nonce.Uint64() {
		t.Error("Marshal/Unmarshal: Nonce mismatch")
	}
	if !bytes.Equal(decoded.Extra, original.Extra) {
		t.Error("Marshal/Unmarshal: Extra mismatch")
	}
}

func TestHeaderUnmarshalInvalidData(t *testing.T) {
	h := &Header{}
	err := h.Unmarshal([]byte{0x00, 0x01, 0x02})
	if err == nil {
		t.Error("Header.Unmarshal should fail with invalid data")
	}
}

func TestHeaderToProtoMessage(t *testing.T) {
	h := &Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
		Time:       1234567890,
		Difficulty: uint256.NewInt(1000),
		BaseFee:    uint256.NewInt(1000000000),
	}

	proto := h.ToProtoMessage()
	if proto == nil {
		t.Error("Header.ToProtoMessage should not return nil")
	}
}

func TestHeaderFromProtoMessageTypeError(t *testing.T) {
	h := &Header{}
	err := h.FromProtoMessage(nil)
	if err == nil {
		t.Error("Header.FromProtoMessage should fail with nil message")
	}
}

func BenchmarkHeaderHash(b *testing.B) {
	h := Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
		GasUsed:    21000,
		Time:       1234567890,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.hash.Store(nil)
		h.Hash()
	}
}

func BenchmarkCopyHeader(b *testing.B) {
	h := &Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		Difficulty: uint256.NewInt(1000000),
		BaseFee:    uint256.NewInt(1000000000),
		Extra:      make([]byte, 32),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CopyHeader(h)
	}
}

func BenchmarkHeaderMarshal(b *testing.B) {
	h := &Header{
		ParentHash: types.HexToHash("0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"),
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
		Difficulty: uint256.NewInt(1000000),
		BaseFee:    uint256.NewInt(1000000000),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Marshal()
	}
}

func BenchmarkEncodeNonce(b *testing.B) {
	for i := 0; i < b.N; i++ {
		EncodeNonce(uint64(i))
	}
}
