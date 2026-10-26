// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// ExecutedResult is what executing a block produced on this node: the four
// header fields that, under deferred execution, the NEXT block's header
// carries. Stored by block hash when the block is written; read by the
// import to check the next header and by the builder to stamp it.
type ExecutedResult struct {
	Root        types.Hash
	ReceiptHash types.Hash
	Bloom       block.Bloom
	GasUsed     uint64
}

const executedResultLen = 32 + 32 + block.BloomByteLength + 8

// WriteExecutedResult stores r under hash.
func WriteExecutedResult(db kv.Putter, hash types.Hash, r ExecutedResult) error {
	return db.Put(modules.ExecutedResult, hash[:], EncodeExecutedResult(r))
}

// EncodeExecutedResult is r's stored encoding (also its E=1 wire form).
func EncodeExecutedResult(r ExecutedResult) []byte {
	v := make([]byte, executedResultLen)
	copy(v[0:32], r.Root[:])
	copy(v[32:64], r.ReceiptHash[:])
	copy(v[64:64+block.BloomByteLength], r.Bloom[:])
	binary.BigEndian.PutUint64(v[64+block.BloomByteLength:], r.GasUsed)
	return v
}

// DecodeExecutedResult reverses EncodeExecutedResult; ok is false for a
// value of the wrong length.
func DecodeExecutedResult(v []byte) (r ExecutedResult, ok bool) {
	if len(v) != executedResultLen {
		return r, false
	}
	copy(r.Root[:], v[0:32])
	copy(r.ReceiptHash[:], v[32:64])
	copy(r.Bloom[:], v[64:64+block.BloomByteLength])
	r.GasUsed = binary.BigEndian.Uint64(v[64+block.BloomByteLength:])
	return r, true
}

// ReadExecutedResult returns the stored result of hash, if this node
// executed it.
func ReadExecutedResult(db kv.Getter, hash types.Hash) (ExecutedResult, bool, error) {
	v, err := db.GetOne(modules.ExecutedResult, hash[:])
	if err != nil {
		return ExecutedResult{}, false, err
	}
	r, ok := DecodeExecutedResult(v)
	return r, ok, nil
}
