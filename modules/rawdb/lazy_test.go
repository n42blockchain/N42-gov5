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

package rawdb

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// makeTestReceipt creates a Receipt with known field values for testing.
func makeTestReceipt(status uint64, gasUsed uint64, cumulativeGas uint64) *block.Receipt {
	return &block.Receipt{
		Status:            status,
		GasUsed:           gasUsed,
		CumulativeGasUsed: cumulativeGas,
		TxHash:            types.HexToHash("0xaaa"),
		BlockNumber:       uint256.NewInt(42),
		Logs: []*block.Log{
			{
				Address:     types.HexToAddress("0xbbb"),
				Topics:      []types.Hash{types.HexToHash("0xccc")},
				Data:        []byte("hello"),
				BlockNumber: uint256.NewInt(42),
			},
		},
	}
}

// marshalReceipt is a test helper that marshals a receipt or fails the test.
func marshalReceipt(t *testing.T, r *block.Receipt) []byte {
	t.Helper()
	data, err := r.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal receipt: %v", err)
	}
	return data
}

func TestLazyReceipt_Nil(t *testing.T) {
	lr := NewLazyReceipt(nil)
	if lr != nil {
		t.Fatal("NewLazyReceipt(nil) should return nil")
	}

	lr2 := NewLazyReceipt([]byte{})
	if lr2 != nil {
		t.Fatal("NewLazyReceipt(empty) should return nil")
	}
}

func TestLazyReceipt_LazyParsing(t *testing.T) {
	r := makeTestReceipt(1, 21000, 42000)
	raw := marshalReceipt(t, r)
	lr := NewLazyReceipt(raw)

	// Verify that Status() works without Full() being called first.
	if lr.parsed != nil {
		t.Fatal("expected parsed to be nil before any access")
	}

	status := lr.Status()
	if status != 1 {
		t.Fatalf("expected status 1, got %d", status)
	}

	// After Status(), the protobuf-level parse should have run, but not the full parse.
	if lr.pb == nil {
		t.Fatal("expected pb to be populated after Status()")
	}
	if lr.parsed != nil {
		t.Fatal("expected parsed to remain nil after Status()")
	}

	// Verify other lightweight accessors.
	if lr.GasUsed() != 21000 {
		t.Fatalf("expected GasUsed 21000, got %d", lr.GasUsed())
	}
	if lr.CumulativeGasUsed() != 42000 {
		t.Fatalf("expected CumulativeGasUsed 42000, got %d", lr.CumulativeGasUsed())
	}
}

func TestLazyReceipt_FullParse(t *testing.T) {
	r := makeTestReceipt(1, 21000, 42000)
	raw := marshalReceipt(t, r)
	lr := NewLazyReceipt(raw)

	full := lr.Full()
	if full == nil {
		t.Fatal("Full() returned nil")
	}

	if full.Status != 1 {
		t.Fatalf("expected status 1, got %d", full.Status)
	}
	if full.GasUsed != 21000 {
		t.Fatalf("expected GasUsed 21000, got %d", full.GasUsed)
	}
	if len(full.Logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(full.Logs))
	}
	if full.Logs[0].Address != types.HexToAddress("0xbbb") {
		t.Fatalf("log address mismatch")
	}

	// Calling Full() again should return the same pointer.
	full2 := lr.Full()
	if full != full2 {
		t.Fatal("Full() should return cached result")
	}
}

func TestLazyReceipt_Logs(t *testing.T) {
	r := makeTestReceipt(1, 21000, 42000)
	raw := marshalReceipt(t, r)
	lr := NewLazyReceipt(raw)

	logs := lr.Logs()
	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}
	if string(logs[0].Data) != "hello" {
		t.Fatalf("expected log data 'hello', got '%s'", string(logs[0].Data))
	}
}

func TestLazyReceipt_RawBytes(t *testing.T) {
	r := makeTestReceipt(1, 21000, 42000)
	raw := marshalReceipt(t, r)
	lr := NewLazyReceipt(raw)

	// RawBytes should return the exact same slice (same backing array).
	rb := lr.RawBytes()
	if &rb[0] != &raw[0] {
		t.Fatal("RawBytes should return zero-copy reference to original slice")
	}
}

func TestLazyReceipt_Copy(t *testing.T) {
	r := makeTestReceipt(1, 21000, 42000)
	raw := marshalReceipt(t, r)
	lr := NewLazyReceipt(raw)

	cp := lr.Copy()
	if cp == nil {
		t.Fatal("Copy() returned nil")
	}

	// The copy's raw bytes should equal but NOT share backing memory.
	cpRaw := cp.RawBytes()
	if &cpRaw[0] == &raw[0] {
		t.Fatal("Copy() should allocate new backing memory")
	}
	if len(cpRaw) != len(raw) {
		t.Fatalf("Copy() length mismatch: %d vs %d", len(cpRaw), len(raw))
	}

	// Verify the copy is still parseable.
	full := cp.Full()
	if full == nil {
		t.Fatal("copied LazyReceipt Full() returned nil")
	}
	if full.Status != 1 {
		t.Fatalf("copied receipt status mismatch: got %d", full.Status)
	}
}

func TestLazyReceipt_InvalidData(t *testing.T) {
	lr := NewLazyReceipt([]byte("invalid protobuf data"))

	// Status should return 0 on parse failure.
	if lr.Status() != 0 {
		t.Fatalf("expected 0 on invalid data, got %d", lr.Status())
	}

	// Full should return nil on parse failure.
	if lr.Full() != nil {
		t.Fatal("expected nil on invalid data")
	}
}

func TestBufPool_GetPut(t *testing.T) {
	b := GetBuf()
	if b == nil {
		t.Fatal("GetBuf returned nil")
	}
	if len(*b) != 0 {
		t.Fatalf("expected empty buffer, got len %d", len(*b))
	}
	if cap(*b) < defaultBufSize {
		t.Fatalf("expected capacity >= %d, got %d", defaultBufSize, cap(*b))
	}

	// Write some data and return.
	*b = append(*b, []byte("test data")...)
	PutBuf(b)

	// Get another buffer — may or may not be the same one, but should be reset.
	b2 := GetBuf()
	if len(*b2) != 0 {
		t.Fatalf("expected reset buffer, got len %d", len(*b2))
	}
	PutBuf(b2)
}

func TestBufPool_NilSafe(t *testing.T) {
	// PutBuf(nil) should not panic.
	PutBuf(nil)
}

func TestBufPool_OversizedDiscard(t *testing.T) {
	b := GetBuf()
	// Grow beyond the 1MB threshold.
	*b = make([]byte, 0, 2<<20)
	PutBuf(b)

	// The next GetBuf should return a fresh default-sized buffer
	// (the oversized one was discarded).
	b2 := GetBuf()
	if cap(*b2) > 1<<20 {
		t.Fatal("pool should not retain oversized buffers")
	}
	PutBuf(b2)
}

func TestLazyHeader_FullParse(t *testing.T) {
	h := &block.Header{
		Number:     uint256.NewInt(100),
		GasLimit:   8000000,
		GasUsed:    3000000,
		Time:       1234567890,
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(1000),
		Extra:      []byte("test"),
	}

	data, err := h.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal header: %v", err)
	}

	lh := NewLazyHeader(data)
	if lh == nil {
		t.Fatal("NewLazyHeader returned nil")
	}

	full := lh.Full()
	if full == nil {
		t.Fatal("Full() returned nil")
	}
	if full.Number.Uint64() != 100 {
		t.Fatalf("expected number 100, got %d", full.Number.Uint64())
	}
	if full.GasLimit != 8000000 {
		t.Fatalf("expected gas limit 8000000, got %d", full.GasLimit)
	}
}

func TestLazyHeader_Copy(t *testing.T) {
	h := &block.Header{
		Number:     uint256.NewInt(200),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}
	data, err := h.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal header: %v", err)
	}

	lh := NewLazyHeader(data)
	cp := lh.Copy()
	if cp == nil {
		t.Fatal("Copy() returned nil")
	}
	if &cp.RawBytes()[0] == &lh.RawBytes()[0] {
		t.Fatal("Copy() should not share backing memory")
	}

	full := cp.Full()
	if full == nil || full.Number.Uint64() != 200 {
		t.Fatal("copied header parse failed")
	}
}
