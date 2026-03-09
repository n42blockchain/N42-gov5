package snapsync

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/api/protocol/sync_pb"
	"github.com/n42blockchain/N42/internal/snapshot"
)

func TestValidateAccountBatch_Valid(t *testing.T) {
	entries := []snapshot.BatchEntry{
		{Key: make([]byte, 20), Value: []byte{1}},
		{Key: append(make([]byte, 19), 1), Value: []byte{2}},
		{Key: append(make([]byte, 19), 2), Value: []byte{3}},
	}
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 3, Completed: true}
	if err := validateAccountBatch(resp, entries, nil, nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateAccountBatch_CountMismatch(t *testing.T) {
	entries := []snapshot.BatchEntry{
		{Key: make([]byte, 20), Value: []byte{1}},
	}
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 5}
	err := validateAccountBatch(resp, entries, nil, nil)
	if !errors.Is(err, ErrBatchCountMismatch) {
		t.Fatalf("expected ErrBatchCountMismatch, got %v", err)
	}
}

func TestValidateAccountBatch_WrongKeyLength(t *testing.T) {
	entries := []snapshot.BatchEntry{
		{Key: make([]byte, 16), Value: []byte{1}}, // 16 instead of 20
	}
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 1}
	err := validateAccountBatch(resp, entries, nil, nil)
	if !errors.Is(err, ErrBatchKeyLength) {
		t.Fatalf("expected ErrBatchKeyLength, got %v", err)
	}
}

func TestValidateAccountBatch_KeysNotOrdered(t *testing.T) {
	entries := []snapshot.BatchEntry{
		{Key: append(make([]byte, 19), 5), Value: []byte{1}},
		{Key: append(make([]byte, 19), 3), Value: []byte{2}}, // out of order
	}
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 2}
	err := validateAccountBatch(resp, entries, nil, nil)
	if !errors.Is(err, ErrBatchKeyOrder) {
		t.Fatalf("expected ErrBatchKeyOrder, got %v", err)
	}
}

func TestValidateAccountBatch_DuplicateKeys(t *testing.T) {
	key := append(make([]byte, 19), 1)
	entries := []snapshot.BatchEntry{
		{Key: key, Value: []byte{1}},
		{Key: key, Value: []byte{2}}, // duplicate
	}
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 2}
	err := validateAccountBatch(resp, entries, nil, nil)
	if !errors.Is(err, ErrBatchKeyOrder) {
		t.Fatalf("expected ErrBatchKeyOrder for duplicates, got %v", err)
	}
}

func TestValidateAccountBatch_KeyBelowStartRange(t *testing.T) {
	startKey := append(make([]byte, 19), 5)
	entries := []snapshot.BatchEntry{
		{Key: append(make([]byte, 19), 3), Value: []byte{1}}, // below start
	}
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 1}
	err := validateAccountBatch(resp, entries, startKey, nil)
	if !errors.Is(err, ErrBatchKeyOutOfRange) {
		t.Fatalf("expected ErrBatchKeyOutOfRange, got %v", err)
	}
}

func TestValidateAccountBatch_KeyAboveEndRange(t *testing.T) {
	endKey := append(make([]byte, 19), 5)
	entries := []snapshot.BatchEntry{
		{Key: append(make([]byte, 19), 5), Value: []byte{1}}, // at end (exclusive)
	}
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 1}
	err := validateAccountBatch(resp, entries, nil, endKey)
	if !errors.Is(err, ErrBatchKeyOutOfRange) {
		t.Fatalf("expected ErrBatchKeyOutOfRange, got %v", err)
	}
}

func TestValidateAccountBatch_EmptyCompleted(t *testing.T) {
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 0, Completed: true}
	if err := validateAccountBatch(resp, nil, nil, nil); err != nil {
		t.Fatalf("expected no error for completed empty batch, got %v", err)
	}
}

func TestValidateAccountBatch_EmptyNotCompleted(t *testing.T) {
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 0, Completed: false}
	err := validateAccountBatch(resp, nil, nil, nil)
	if !errors.Is(err, ErrBatchEmpty) {
		t.Fatalf("expected ErrBatchEmpty, got %v", err)
	}
}

func TestValidateStorageBatch_Valid(t *testing.T) {
	entries := []snapshot.BatchEntry{
		{Key: make([]byte, 32), Value: []byte{1}},
		{Key: append(make([]byte, 31), 1), Value: []byte{2}},
	}
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 2, Completed: true}
	if err := validateStorageBatch(resp, entries, nil, nil); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateStorageBatch_WrongKeyLength(t *testing.T) {
	entries := []snapshot.BatchEntry{
		{Key: make([]byte, 20), Value: []byte{1}}, // 20 instead of 32
	}
	resp := &sync_pb.CompressedBatchResponse{EntryCount: 1}
	err := validateStorageBatch(resp, entries, nil, nil)
	if !errors.Is(err, ErrBatchKeyLength) {
		t.Fatalf("expected ErrBatchKeyLength, got %v", err)
	}
}

func TestValidateCodeResponse_Valid(t *testing.T) {
	hashes := [][]byte{make([]byte, 32)}
	resp := &sync_pb.CodeResponse{
		Codes: []*sync_pb.CodeEntry{
			{Hash: hashes[0], Code: []byte{0x60, 0x00}},
		},
	}
	if err := validateCodeResponse(resp, hashes); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestValidateCodeResponse_UnrequestedHash(t *testing.T) {
	requested := [][]byte{make([]byte, 32)}
	unrequested := append(make([]byte, 31), 0xFF)
	resp := &sync_pb.CodeResponse{
		Codes: []*sync_pb.CodeEntry{
			{Hash: unrequested, Code: []byte{0x60}},
		},
	}
	err := validateCodeResponse(resp, requested)
	if err == nil {
		t.Fatal("expected error for unrequested hash")
	}
}

func TestValidateCodeResponse_EmptyCode(t *testing.T) {
	hash := make([]byte, 32)
	resp := &sync_pb.CodeResponse{
		Codes: []*sync_pb.CodeEntry{
			{Hash: hash, Code: nil},
		},
	}
	err := validateCodeResponse(resp, [][]byte{hash})
	if err == nil {
		t.Fatal("expected error for empty code")
	}
}

func TestValidateCodeResponse_InvalidHashLength(t *testing.T) {
	hash := make([]byte, 16) // wrong length
	resp := &sync_pb.CodeResponse{
		Codes: []*sync_pb.CodeEntry{
			{Hash: hash, Code: []byte{0x60}},
		},
	}
	err := validateCodeResponse(resp, [][]byte{hash})
	if err == nil {
		t.Fatal("expected error for invalid hash length")
	}
}

func TestValidateCodeResponse_Nil(t *testing.T) {
	if err := validateCodeResponse(nil, nil); err != nil {
		t.Fatalf("expected no error for nil response, got %v", err)
	}
}
