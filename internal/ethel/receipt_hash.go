// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// receipt_hash.go — Ethereum-compatible receipts-root computation.
//
// ethReceiptList adapts a slice of block.Receipt to the DeriveSha list
// interface. EncodeIndex writes the canonical post-Byzantium RLP layout
// (type byte for EIP-2718 plus Status, CumulativeGasUsed, Bloom, Logs)
// and uses the bloom produced by transaction execution. A compatibility
// fallback recomputes it only for callers that supply compactly decoded
// receipts, whose storage codec strips Bloom. EthReceiptHash feeds the list
// through DeriveShaErigon so the resulting root matches live mainnet headers
// bit-for-bit.

package ethel

import (
	"bytes"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

// ethReceiptList wraps receipts for Ethereum-compatible DeriveShaETH.
type ethReceiptList []*block.Receipt

func (rs ethReceiptList) Len() int { return len(rs) }

type ethRLPLog struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
}

// EncodeIndex writes the Ethereum-compatible RLP encoding of receipt i.
// Post-Byzantium: RLP([Status, CumulativeGasUsed, Bloom, Logs])
// EIP-2718:       type || RLP([Status, CumulativeGasUsed, Bloom, Logs])
//
// Bloom is recomputed from logs because the on-disk ReceiptForStorage format
// (used by both Geth ancient and our compact storage) does NOT persist Bloom —
// it is a deterministic function of the logs.
func (rs ethReceiptList) EncodeIndex(i int, w *bytes.Buffer) {
	r := rs[i]

	if r.Type > 0 {
		w.WriteByte(r.Type)
	}

	logs := make([]ethRLPLog, len(r.Logs))
	for k, l := range r.Logs {
		logs[k] = ethRLPLog{Address: l.Address, Topics: l.Topics, Data: l.Data}
	}

	bloom := r.Bloom
	if bloom == (block.Bloom{}) && len(r.Logs) > 0 {
		bloom = block.CreateBloom(block.Receipts{r})
	}

	rlp.Encode(w, &struct {
		Status            uint64
		CumulativeGasUsed uint64
		Bloom             block.Bloom
		Logs              []ethRLPLog
	}{r.Status, r.CumulativeGasUsed, bloom, logs})
}

// EthReceiptHash computes the Ethereum-standard receipt root hash
// using the canonical MPT (erigon HashBuilder backend).
func EthReceiptHash(receipts []*block.Receipt) types.Hash {
	return hash.DeriveShaErigon(ethReceiptList(receipts))
}
