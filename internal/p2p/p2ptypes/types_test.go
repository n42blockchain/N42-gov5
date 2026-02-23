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
	"strings"
	"testing"
)

// =============================================================================
// SSZBytes Tests
// =============================================================================

func TestSSZBytesHashTreeRoot(t *testing.T) {
	data := SSZBytes([]byte{0x01, 0x02, 0x03, 0x04})

	root, err := data.HashTreeRoot()
	if err != nil {
		t.Fatalf("HashTreeRoot returned error: %v", err)
	}
	if len(root) != 32 {
		t.Errorf("root should be 32 bytes, got %d", len(root))
	}
}

func TestSSZBytesHashTreeRootConsistency(t *testing.T) {
	data := SSZBytes([]byte{0x01, 0x02, 0x03, 0x04})

	root1, _ := data.HashTreeRoot()
	root2, _ := data.HashTreeRoot()

	if root1 != root2 {
		t.Error("HashTreeRoot should be deterministic")
	}
}

// =============================================================================
// BlockByRootsReq Tests
// =============================================================================

func TestBlockByRootsReqMarshalSSZ(t *testing.T) {
	roots := BlockByRootsReq{
		{0x01, 0x02, 0x03},
		{0x04, 0x05, 0x06},
	}

	data, err := roots.MarshalSSZ()
	if err != nil {
		t.Fatalf("MarshalSSZ returned error: %v", err)
	}

	expectedLen := len(roots) * 32
	if len(data) != expectedLen {
		t.Errorf("marshalled data length = %d, want %d", len(data), expectedLen)
	}
}

func TestBlockByRootsReqMarshalSSZTo(t *testing.T) {
	roots := BlockByRootsReq{
		{0x01, 0x02, 0x03},
	}

	dst := []byte{0xff, 0xff}
	result, err := roots.MarshalSSZTo(dst)
	if err != nil {
		t.Fatalf("MarshalSSZTo returned error: %v", err)
	}
	if !bytes.HasPrefix(result, dst) {
		t.Error("MarshalSSZTo should preserve destination prefix")
	}
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
			if size := roots.SizeSSZ(); size != tt.expected {
				t.Errorf("SizeSSZ() = %d, want %d", size, tt.expected)
			}
		})
	}
}

func TestBlockByRootsReqUnmarshalSSZ(t *testing.T) {
	original := BlockByRootsReq{
		{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
			0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
			0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
			0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20},
	}

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatalf("MarshalSSZ failed: %v", err)
	}

	var decoded BlockByRootsReq
	if err = decoded.UnmarshalSSZ(data); err != nil {
		t.Fatalf("UnmarshalSSZ returned error: %v", err)
	}
	if len(decoded) != len(original) {
		t.Errorf("decoded length = %d, want %d", len(decoded), len(original))
	}
}

func TestBlockByRootsReqUnmarshalSSZInvalidLength(t *testing.T) {
	invalidData := make([]byte, 33)
	var req BlockByRootsReq

	if err := req.UnmarshalSSZ(invalidData); err == nil {
		t.Error("UnmarshalSSZ should return error for invalid length")
	}
}

func TestBlockByRootsReqMarshalSSZTooLarge(t *testing.T) {
	roots := make(BlockByRootsReq, 1025)

	if _, err := roots.MarshalSSZ(); err == nil {
		t.Error("MarshalSSZ should return error for too many roots")
	}
}

// =============================================================================
// ErrorMessage Tests
// =============================================================================

func TestErrorMessageMarshalSSZ(t *testing.T) {
	msg := ErrorMessage("test error message")

	data, err := msg.MarshalSSZ()
	if err != nil {
		t.Fatalf("MarshalSSZ returned error: %v", err)
	}
	if !bytes.Equal(data, []byte(msg)) {
		t.Error("marshalled data should match original message")
	}
}

func TestErrorMessageMarshalSSZTo(t *testing.T) {
	msg := ErrorMessage("error")
	dst := []byte{0xff}

	result, err := msg.MarshalSSZTo(dst)
	if err != nil {
		t.Fatalf("MarshalSSZTo returned error: %v", err)
	}
	if !bytes.HasPrefix(result, dst) {
		t.Error("MarshalSSZTo should preserve destination prefix")
	}
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
			if size := msg.SizeSSZ(); size != tt.expected {
				t.Errorf("SizeSSZ() = %d, want %d", size, tt.expected)
			}
		})
	}
}

func TestErrorMessageUnmarshalSSZ(t *testing.T) {
	original := ErrorMessage("test error")
	data, _ := original.MarshalSSZ()

	var decoded ErrorMessage
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatalf("UnmarshalSSZ returned error: %v", err)
	}
	if string(decoded) != string(original) {
		t.Errorf("decoded = %s, want %s", decoded, original)
	}
}

func TestErrorMessageMarshalSSZTooLarge(t *testing.T) {
	msg := ErrorMessage(make([]byte, 257))

	if _, err := msg.MarshalSSZ(); err == nil {
		t.Error("MarshalSSZ should return error for too long message")
	}
}

func TestErrorMessageUnmarshalSSZTooLarge(t *testing.T) {
	data := make([]byte, 257)
	var msg ErrorMessage

	if err := msg.UnmarshalSSZ(data); err == nil {
		t.Error("UnmarshalSSZ should return error for too long buffer")
	}
}

// =============================================================================
// Constants Tests
// =============================================================================

func TestConstants(t *testing.T) {
	if rootLength != 32 {
		t.Errorf("rootLength should be 32, got %d", rootLength)
	}
	if maxErrorLength != 256 {
		t.Errorf("maxErrorLength should be 256, got %d", maxErrorLength)
	}
	if maxRequestBlocks != 1024 {
		t.Errorf("maxRequestBlocks should be 1024, got %d", maxRequestBlocks)
	}
}

// =============================================================================
// RPC Goodbye Codes Tests
// =============================================================================

func TestGoodbyeCodeValues(t *testing.T) {
	if GoodbyeCodeClientShutdown != 1 {
		t.Errorf("GoodbyeCodeClientShutdown should be 1, got %d", GoodbyeCodeClientShutdown)
	}
	if GoodbyeCodeWrongNetwork != 2 {
		t.Errorf("GoodbyeCodeWrongNetwork should be 2, got %d", GoodbyeCodeWrongNetwork)
	}
	if GoodbyeCodeGenericError != 3 {
		t.Errorf("GoodbyeCodeGenericError should be 3, got %d", GoodbyeCodeGenericError)
	}
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
}

func TestGoodbyeCodeMessages(t *testing.T) {
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
			t.Errorf("missing message for goodbye code %d", code)
		}
		if msg == "" {
			t.Errorf("empty message for goodbye code %d", code)
		}
	}
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
			if code := ErrToGoodbyeCode(tt.err); code != tt.expected {
				t.Errorf("ErrToGoodbyeCode() = %d, want %d", code, tt.expected)
			}
		})
	}
}

// =============================================================================
// RPC Errors Tests
// =============================================================================

func TestRPCErrorsExist(t *testing.T) {
	allErrors := []error{
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

	for i, err := range allErrors {
		if err == nil {
			t.Errorf("error at index %d should not be nil", i)
		}
		if err.Error() == "" {
			t.Errorf("error at index %d should have a message", i)
		}
	}
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
			if !strings.Contains(tt.err.Error(), tt.contains) {
				t.Errorf("error message %q should contain %q", tt.err.Error(), tt.contains)
			}
		})
	}
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
