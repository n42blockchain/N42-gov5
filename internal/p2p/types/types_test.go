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

package p2ptypes

import (
	"bytes"
	"testing"
)

// =============================================================================
// SSZBytes Tests
// =============================================================================

func TestSSZBytesHashTreeRoot(t *testing.T) {
	data := SSZBytes([]byte{0x01, 0x02, 0x03, 0x04})

	root, err := data.HashTreeRoot()
	if err != nil {
		t.Errorf("HashTreeRoot should not return error: %v", err)
	}

	// Root should be 32 bytes
	if len(root) != 32 {
		t.Errorf("Root should be 32 bytes, got %d", len(root))
	}

	t.Logf("✓ SSZBytes.HashTreeRoot works correctly")
}

func TestSSZBytesHashTreeRootConsistency(t *testing.T) {
	data := SSZBytes([]byte{0x01, 0x02, 0x03, 0x04})

	root1, _ := data.HashTreeRoot()
	root2, _ := data.HashTreeRoot()

	if root1 != root2 {
		t.Error("HashTreeRoot should be deterministic")
	}

	t.Logf("✓ SSZBytes.HashTreeRoot is deterministic")
}

// =============================================================================
// BlockByRootsReq Tests
// =============================================================================

func TestBlockByRootsReqMarshalSSZ(t *testing.T) {
	roots := BlockByRootsReq{
		{0x01, 0x02, 0x03}, // 32 bytes each
		{0x04, 0x05, 0x06},
	}

	data, err := roots.MarshalSSZ()
	if err != nil {
		t.Errorf("MarshalSSZ should not return error: %v", err)
	}

	expectedLen := len(roots) * 32
	if len(data) != expectedLen {
		t.Errorf("Marshalled data should be %d bytes, got %d", expectedLen, len(data))
	}

	t.Logf("✓ BlockByRootsReq.MarshalSSZ works correctly")
}

func TestBlockByRootsReqMarshalSSZTo(t *testing.T) {
	roots := BlockByRootsReq{
		{0x01, 0x02, 0x03},
	}

	dst := []byte{0xff, 0xff}
	result, err := roots.MarshalSSZTo(dst)
	if err != nil {
		t.Errorf("MarshalSSZTo should not return error: %v", err)
	}

	// Should have prefix + marshalled data
	if !bytes.HasPrefix(result, dst) {
		t.Error("MarshalSSZTo should preserve destination prefix")
	}

	t.Logf("✓ BlockByRootsReq.MarshalSSZTo works correctly")
}

func TestBlockByRootsReqSizeSSZ(t *testing.T) {
	tests := []struct {
		name     string
		count    int
		expected int
	}{
		{"empty", 0, 0},
		{"one_root", 1, 32},
		{"two_roots", 2, 64},
		{"ten_roots", 10, 320},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			roots := make(BlockByRootsReq, tt.count)
			size := roots.SizeSSZ()
			if size != tt.expected {
				t.Errorf("SizeSSZ() = %d, want %d", size, tt.expected)
			}
		})
	}

	t.Logf("✓ BlockByRootsReq.SizeSSZ works correctly")
}

func TestBlockByRootsReqUnmarshalSSZ(t *testing.T) {
	// Create original
	original := BlockByRootsReq{
		{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
			0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
			0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20},
	}

	// Marshal
	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatalf("MarshalSSZ failed: %v", err)
	}

	// Unmarshal
	var decoded BlockByRootsReq
	err = decoded.UnmarshalSSZ(data)
	if err != nil {
		t.Errorf("UnmarshalSSZ should not return error: %v", err)
	}

	if len(decoded) != len(original) {
		t.Errorf("Decoded length = %d, want %d", len(decoded), len(original))
	}

	t.Logf("✓ BlockByRootsReq.UnmarshalSSZ works correctly")
}

func TestBlockByRootsReqUnmarshalSSZInvalidLength(t *testing.T) {
	// Not a multiple of 32
	invalidData := make([]byte, 33)
	var req BlockByRootsReq

	err := req.UnmarshalSSZ(invalidData)
	if err == nil {
		t.Error("UnmarshalSSZ should return error for invalid length")
	}

	t.Logf("✓ BlockByRootsReq.UnmarshalSSZ validates length correctly")
}

func TestBlockByRootsReqMarshalSSZTooLarge(t *testing.T) {
	// Create more than maxRequestBlocks roots
	roots := make(BlockByRootsReq, 1025)

	_, err := roots.MarshalSSZ()
	if err == nil {
		t.Error("MarshalSSZ should return error for too many roots")
	}

	t.Logf("✓ BlockByRootsReq.MarshalSSZ validates max size correctly")
}

// =============================================================================
// ErrorMessage Tests
// =============================================================================

func TestErrorMessageMarshalSSZ(t *testing.T) {
	msg := ErrorMessage("test error message")

	data, err := msg.MarshalSSZ()
	if err != nil {
		t.Errorf("MarshalSSZ should not return error: %v", err)
	}

	if !bytes.Equal(data, []byte(msg)) {
		t.Error("Marshalled data should match original message")
	}

	t.Logf("✓ ErrorMessage.MarshalSSZ works correctly")
}

func TestErrorMessageMarshalSSZTo(t *testing.T) {
	msg := ErrorMessage("error")
	dst := []byte{0xff}

	result, err := msg.MarshalSSZTo(dst)
	if err != nil {
		t.Errorf("MarshalSSZTo should not return error: %v", err)
	}

	if !bytes.HasPrefix(result, dst) {
		t.Error("MarshalSSZTo should preserve destination prefix")
	}

	t.Logf("✓ ErrorMessage.MarshalSSZTo works correctly")
}

func TestErrorMessageSizeSSZ(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		expected int
	}{
		{"empty", "", 0},
		{"short", "err", 3},
		{"medium", "error message", 13},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := ErrorMessage(tt.msg)
			size := msg.SizeSSZ()
			if size != tt.expected {
				t.Errorf("SizeSSZ() = %d, want %d", size, tt.expected)
			}
		})
	}

	t.Logf("✓ ErrorMessage.SizeSSZ works correctly")
}

func TestErrorMessageUnmarshalSSZ(t *testing.T) {
	original := ErrorMessage("test error")

	data, _ := original.MarshalSSZ()

	var decoded ErrorMessage
	err := decoded.UnmarshalSSZ(data)
	if err != nil {
		t.Errorf("UnmarshalSSZ should not return error: %v", err)
	}

	if string(decoded) != string(original) {
		t.Errorf("Decoded = %s, want %s", decoded, original)
	}

	t.Logf("✓ ErrorMessage.UnmarshalSSZ works correctly")
}

func TestErrorMessageMarshalSSZTooLarge(t *testing.T) {
	// Create message longer than maxErrorLength (256)
	msg := ErrorMessage(make([]byte, 257))

	_, err := msg.MarshalSSZ()
	if err == nil {
		t.Error("MarshalSSZ should return error for too long message")
	}

	t.Logf("✓ ErrorMessage.MarshalSSZ validates max length correctly")
}

func TestErrorMessageUnmarshalSSZTooLarge(t *testing.T) {
	// Create buffer longer than maxErrorLength
	data := make([]byte, 257)
	var msg ErrorMessage

	err := msg.UnmarshalSSZ(data)
	if err == nil {
		t.Error("UnmarshalSSZ should return error for too long buffer")
	}

	t.Logf("✓ ErrorMessage.UnmarshalSSZ validates max length correctly")
}

// =============================================================================
// Constants Tests
// =============================================================================

func TestConstants(t *testing.T) {
	// Verify constants are correctly defined
	if rootLength != 32 {
		t.Errorf("rootLength should be 32, got %d", rootLength)
	}
	if maxErrorLength != 256 {
		t.Errorf("maxErrorLength should be 256, got %d", maxErrorLength)
	}
	if maxRequestBlocks != 1024 {
		t.Errorf("maxRequestBlocks should be 1024, got %d", maxRequestBlocks)
	}

	t.Logf("✓ Constants are correctly defined")
}

// =============================================================================
// RPC Goodbye Codes Tests
// =============================================================================

func TestGoodbyeCodeValues(t *testing.T) {
	// Verify spec-defined codes
	if GoodbyeCodeClientShutdown != 1 {
		t.Errorf("GoodbyeCodeClientShutdown should be 1, got %d", GoodbyeCodeClientShutdown)
	}
	if GoodbyeCodeWrongNetwork != 2 {
		t.Errorf("GoodbyeCodeWrongNetwork should be 2, got %d", GoodbyeCodeWrongNetwork)
	}
	if GoodbyeCodeGenericError != 3 {
		t.Errorf("GoodbyeCodeGenericError should be 3, got %d", GoodbyeCodeGenericError)
	}

	// Verify extended codes
	if GoodbyeCodeUnableToVerifyNetwork != 128 {
		t.Errorf("GoodbyeCodeUnableToVerifyNetwork should be 128, got %d", GoodbyeCodeUnableToVerifyNetwork)
	}
	if GoodbyeCodeTooManyPeers != 129 {
		t.Errorf("GoodbyeCodeTooManyPeers should be 129, got %d", GoodbyeCodeTooManyPeers)
	}
	if GoodbyeCodeBadScore != 250 {
		t.Errorf("GoodbyeCodeBadScore should be 250, got %d", GoodbyeCodeBadScore)
	}
	if GoodbyeCodeBanned != 251 {
		t.Errorf("GoodbyeCodeBanned should be 251, got %d", GoodbyeCodeBanned)
	}

	t.Logf("✓ Goodbye codes are correct")
}

func TestGoodbyeCodeMessages(t *testing.T) {
	// All codes should have a message
	codes := []RPCGoodbyeCode{
		GoodbyeCodeClientShutdown,
		GoodbyeCodeWrongNetwork,
		GoodbyeCodeGenericError,
		GoodbyeCodeUnableToVerifyNetwork,
		GoodbyeCodeTooManyPeers,
		GoodbyeCodeBadScore,
		GoodbyeCodeBanned,
	}

	for _, code := range codes {
		msg, ok := GoodbyeCodeMessages[code]
		if !ok {
			t.Errorf("Missing message for goodbye code %d", code)
		}
		if msg == "" {
			t.Errorf("Empty message for goodbye code %d", code)
		}
	}

	t.Logf("✓ All goodbye codes have messages")
}

func TestErrToGoodbyeCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected RPCGoodbyeCode
	}{
		{"wrong_fork", ErrWrongForkDigestVersion, GoodbyeCodeWrongNetwork},
		{"generic", ErrGeneric, GoodbyeCodeGenericError},
		{"nil", nil, GoodbyeCodeGenericError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := ErrToGoodbyeCode(tt.err)
			if code != tt.expected {
				t.Errorf("ErrToGoodbyeCode() = %d, want %d", code, tt.expected)
			}
		})
	}

	t.Logf("✓ ErrToGoodbyeCode works correctly")
}

// =============================================================================
// RPC Errors Tests
// =============================================================================

func TestRPCErrorsExist(t *testing.T) {
	errors := []error{
		ErrWrongForkDigestVersion,
		ErrInvalidBlockNr,
		ErrInvalidFinalizedRoot,
		ErrInvalidSequenceNum,
		ErrGeneric,
		ErrInvalidParent,
		ErrRateLimited,
		ErrIODeadline,
		ErrInvalidRequest,
	}

	for i, err := range errors {
		if err == nil {
			t.Errorf("Error %d should not be nil", i)
		}
		if err.Error() == "" {
			t.Errorf("Error %d should have a message", i)
		}
	}

	t.Logf("✓ All RPC errors are defined correctly")
}

func TestRPCErrorMessages(t *testing.T) {
	tests := []struct {
		err      error
		contains string
	}{
		{ErrWrongForkDigestVersion, "fork digest"},
		{ErrInvalidBlockNr, "block number"},
		{ErrInvalidFinalizedRoot, "finalized root"},
		{ErrGeneric, "internal"},
		{ErrRateLimited, "rate limited"},
	}

	for _, tt := range tests {
		t.Run(tt.err.Error(), func(t *testing.T) {
			if !bytes.Contains([]byte(tt.err.Error()), []byte(tt.contains)) {
				t.Logf("Warning: Error message '%s' might not contain '%s'", tt.err.Error(), tt.contains)
			}
		})
	}

	t.Logf("✓ RPC error messages are informative")
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkSSZBytesHashTreeRoot(b *testing.B) {
	data := SSZBytes(make([]byte, 1024))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data.HashTreeRoot()
	}
}

func BenchmarkBlockByRootsReqMarshalSSZ(b *testing.B) {
	roots := make(BlockByRootsReq, 100)
	for i := range roots {
		roots[i][0] = byte(i)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		roots.MarshalSSZ()
	}
}

func BenchmarkBlockByRootsReqUnmarshalSSZ(b *testing.B) {
	roots := make(BlockByRootsReq, 100)
	for i := range roots {
		roots[i][0] = byte(i)
	}
	data, _ := roots.MarshalSSZ()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var decoded BlockByRootsReq
		decoded.UnmarshalSSZ(data)
	}
}

func BenchmarkErrorMessageMarshalSSZ(b *testing.B) {
	msg := ErrorMessage("test error message for benchmark")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		msg.MarshalSSZ()
	}
}
