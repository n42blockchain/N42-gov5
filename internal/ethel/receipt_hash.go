// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// receipt_hash.go — Ethereum-compatible receipts-root computation.
//
// ethReceiptList adapts a slice of block.Receipt to the DeriveSha list
// interface. EncodeIndex writes the canonical post-Byzantium RLP layout
// (type byte for EIP-2718 plus Status, CumulativeGasUsed, Bloom, Logs)
// and recomputes the bloom from logs because the on-disk compact receipt
// codec strips it. EthReceiptHash feeds the list through DeriveShaErigon
// so the resulting root matches live mainnet headers bit-for-bit.

package ethel

import (
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// EthReceiptHash computes the Ethereum-standard receipt root hash
// using the canonical MPT (erigon HashBuilder backend).
func EthReceiptHash(receipts []*block.Receipt) types.Hash {
	return block.EthereumReceiptRoot(receipts, true)
}
