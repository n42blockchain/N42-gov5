// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// ExecutedResult is what executing a block produced on this node: the
// header fields that, under deferred execution, the NEXT block's header
// carries. Stored by block hash when the block is written; read by the
// import to check the next header and by the builder to stamp it.
// RequestsHash is the block's EIP-7685 requests hash (carried by the next
// header on profile 1, AM25); it is sha256("") for a block without requests.
type ExecutedResult struct {
	Root         types.Hash
	ReceiptHash  types.Hash
	Bloom        block.Bloom
	GasUsed      uint64
	RequestsHash types.Hash
}

// EmptyRequestsHash is EIP-7685's hash of no requests, sha256("").
var EmptyRequestsHash = types.Hash(sha256.Sum256(nil))

const (
	// executedResultLenV1 is a record written before the requests hash was
	// stored; such a block produced no requests (AM24 stamped sha256("")).
	executedResultLenV1 = 32 + 32 + block.BloomByteLength + 8
	executedResultLen   = executedResultLenV1 + 32
)

// WriteExecutedResult stores r under hash.
func WriteExecutedResult(db kv.Putter, hash types.Hash, r ExecutedResult) error {
	var v [executedResultLen]byte
	copy(v[0:32], r.Root[:])
	copy(v[32:64], r.ReceiptHash[:])
	copy(v[64:64+block.BloomByteLength], r.Bloom[:])
	binary.BigEndian.PutUint64(v[64+block.BloomByteLength:executedResultLenV1], r.GasUsed)
	copy(v[executedResultLenV1:], r.RequestsHash[:])
	return db.Put(modules.ExecutedResult, hash[:], v[:])
}

// ReadExecutedResult returns the stored result of hash, if this node
// executed it.
func ReadExecutedResult(db kv.Getter, hash types.Hash) (ExecutedResult, bool, error) {
	var r ExecutedResult
	v, err := db.GetOne(modules.ExecutedResult, hash[:])
	if err != nil {
		return r, false, err
	}
	if len(v) != executedResultLen && len(v) != executedResultLenV1 {
		return r, false, nil
	}
	copy(r.Root[:], v[0:32])
	copy(r.ReceiptHash[:], v[32:64])
	copy(r.Bloom[:], v[64:64+block.BloomByteLength])
	r.GasUsed = binary.BigEndian.Uint64(v[64+block.BloomByteLength : executedResultLenV1])
	r.RequestsHash = EmptyRequestsHash
	if len(v) == executedResultLen {
		copy(r.RequestsHash[:], v[executedResultLenV1:])
	}
	return r, true, nil
}
