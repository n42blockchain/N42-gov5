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
	"sync"

	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common/block"
)

// LazyReceipt wraps raw protobuf-encoded receipt bytes and defers full
// deserialization until individual fields are accessed. This is useful when
// callers only need a subset of receipt data (e.g., just the status or logs)
// and want to avoid the overhead of a full protobuf decode.
//
// IMPORTANT: MDBX transaction lifetime constraint.
// The raw bytes returned by kv.Tx.GetOne() point into MDBX's memory-mapped
// pages and are only valid for the duration of the enclosing read transaction.
// If the LazyReceipt must outlive the transaction, call Copy() first to
// create a standalone copy of the underlying bytes.
type LazyReceipt struct {
	raw    []byte          // original bytes from DB (zero-copy reference)
	parsed *block.Receipt  // lazily populated on first full parse
	pb     *types_pb.Receipt // lazily populated protobuf intermediate
	once   sync.Once       // guards full parse
	pbOnce sync.Once       // guards protobuf-level parse
}

// NewLazyReceipt creates a LazyReceipt backed by the given raw protobuf bytes.
// The raw slice is NOT copied — it references the original memory (zero-copy).
// See the type-level doc for MDBX transaction lifetime constraints.
func NewLazyReceipt(raw []byte) *LazyReceipt {
	if len(raw) == 0 {
		return nil
	}
	return &LazyReceipt{raw: raw}
}

// ensurePB performs a protobuf-level unmarshal without converting to the
// domain Receipt struct. This is cheaper than a full parse when only
// simple scalar fields are needed.
func (lr *LazyReceipt) ensurePB() *types_pb.Receipt {
	lr.pbOnce.Do(func() {
		pb := new(types_pb.Receipt)
		if err := proto.Unmarshal(lr.raw, pb); err != nil {
			// Store nil; callers check for it.
			lr.pb = nil
			return
		}
		lr.pb = pb
	})
	return lr.pb
}

// Status returns the receipt's execution status (0=failed, 1=success)
// by performing a lightweight protobuf decode without building the full
// Receipt struct.
func (lr *LazyReceipt) Status() uint64 {
	pb := lr.ensurePB()
	if pb == nil {
		return 0
	}
	return pb.Status
}

// CumulativeGasUsed returns the cumulative gas used up to and including
// the transaction that produced this receipt.
func (lr *LazyReceipt) CumulativeGasUsed() uint64 {
	pb := lr.ensurePB()
	if pb == nil {
		return 0
	}
	return pb.CumulativeGasUsed
}

// GasUsed returns the gas consumed by the individual transaction.
func (lr *LazyReceipt) GasUsed() uint64 {
	pb := lr.ensurePB()
	if pb == nil {
		return 0
	}
	return pb.GasUsed
}

// Logs performs a full parse and returns the receipt's event logs.
// Once parsed, the result is cached for subsequent calls.
func (lr *LazyReceipt) Logs() []*block.Log {
	r := lr.Full()
	if r == nil {
		return nil
	}
	return r.Logs
}

// Full performs a complete deserialization from the raw protobuf bytes
// into a block.Receipt. The result is cached — repeated calls are free.
func (lr *LazyReceipt) Full() *block.Receipt {
	lr.once.Do(func() {
		r := new(block.Receipt)
		if err := r.Unmarshal(lr.raw); err != nil {
			lr.parsed = nil
			return
		}
		lr.parsed = r
	})
	return lr.parsed
}

// RawBytes returns the underlying raw bytes without any copy.
// The returned slice references MDBX memory-mapped pages when created
// via NewLazyReceipt on data from kv.Tx.GetOne(). Do NOT use the
// returned bytes after the enclosing read transaction has ended.
func (lr *LazyReceipt) RawBytes() []byte {
	return lr.raw
}

// Copy creates a new LazyReceipt whose raw bytes are a standalone heap
// allocation, safe to use after the originating MDBX transaction ends.
func (lr *LazyReceipt) Copy() *LazyReceipt {
	if lr == nil || len(lr.raw) == 0 {
		return nil
	}
	cp := make([]byte, len(lr.raw))
	copy(cp, lr.raw)
	return &LazyReceipt{raw: cp}
}

// LazyHeader wraps raw protobuf-encoded header bytes with deferred parsing.
// Same MDBX transaction lifetime constraints as LazyReceipt apply.
type LazyHeader struct {
	raw    []byte
	parsed *block.Header
	once   sync.Once
}

// NewLazyHeader creates a LazyHeader backed by the given raw protobuf bytes.
func NewLazyHeader(raw []byte) *LazyHeader {
	if len(raw) == 0 {
		return nil
	}
	return &LazyHeader{raw: raw}
}

// Full performs a complete deserialization into a block.Header.
func (lh *LazyHeader) Full() *block.Header {
	lh.once.Do(func() {
		h := new(block.Header)
		if err := h.Unmarshal(lh.raw); err != nil {
			lh.parsed = nil
			return
		}
		lh.parsed = h
	})
	return lh.parsed
}

// RawBytes returns the underlying raw bytes (zero-copy).
func (lh *LazyHeader) RawBytes() []byte {
	return lh.raw
}

// Copy creates a standalone copy safe to use after the MDBX transaction ends.
func (lh *LazyHeader) Copy() *LazyHeader {
	if lh == nil || len(lh.raw) == 0 {
		return nil
	}
	cp := make([]byte, len(lh.raw))
	copy(cp, lh.raw)
	return &LazyHeader{raw: cp}
}
