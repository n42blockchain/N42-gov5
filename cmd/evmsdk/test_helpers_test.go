// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Shared test helpers used across read_log_test.go, verify_v2_test.go,
// and mobile_facade_test.go. Lives in a _test.go file so it isn't
// compiled into the production binary or exposed via gomobile bind.

package evmsdk

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// emptyEthTrieRoot is the canonical empty MPT root: keccak256(rlp([])).
// Used as the receipts root for empty blocks (no txs → no receipts).
var emptyEthTrieRoot = types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

// minimalHeader returns a header with every required field filled in so
// header.Marshal() does not panic on nil pointer fields. The defaults are
// what an empty block at the given height would look like; tests that
// care about specific values override only what they need.
func minimalHeader(t *testing.T, blockNumber uint64, receiptRoot types.Hash) *block.Header {
	t.Helper()
	return &block.Header{
		ParentHash:  types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000001"),
		UncleHash:   types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:    types.HexToAddress("0x000000000000000000000000000000000000beef"),
		Root:        types.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000002"),
		TxHash:      emptyEthTrieRoot,
		ReceiptHash: receiptRoot,
		Difficulty:  uint256.NewInt(0),
		Number:      uint256.NewInt(blockNumber),
		GasLimit:    30_000_000,
		GasUsed:     0,
		Time:        1700000000,
		Extra:       []byte{},
		BaseFee:     uint256.NewInt(1_000_000_000),
	}
}

// emptyPacket builds an empty-block StreamPacket suitable for round-trip
// tests. The packet has no transactions, no read log entries, no
// bytecodes — re-execution should produce empty receipts.
func emptyPacket(t *testing.T, hdr *block.Header) *StreamPacket {
	t.Helper()
	hdrBytes, err := hdr.Marshal()
	if err != nil {
		t.Fatalf("header marshal: %v", err)
	}
	return &StreamPacket{
		BlockHash:    hdr.Hash(),
		HeaderRLP:    hdrBytes,
		Transactions: nil,
		ReadLogData:  EncodeReadLog(nil),
		Bytecodes:    nil,
	}
}
