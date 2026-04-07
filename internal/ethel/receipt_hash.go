// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// receipt_hash.go — Ethereum-compatible receipt root hash for verification.

package ethel

import (
	"bytes"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

// ethReceiptList wraps receipts for Ethereum-compatible DeriveSha.
type ethReceiptList []*block.Receipt

func (rs ethReceiptList) Len() int { return len(rs) }

type ethRLPLog struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
}

// EncodeIndex writes the Ethereum-compatible RLP encoding of receipt i.
//
// Format: Pre-Byzantium  RLP([PostState, CumulativeGasUsed, Bloom, Logs])
//
//	Post-Byzantium RLP([Status, CumulativeGasUsed, Bloom, Logs])
//	EIP-2718       type || RLP([Status, CumulativeGasUsed, Bloom, Logs])
func (rs ethReceiptList) EncodeIndex(i int, w *bytes.Buffer) {
	r := rs[i]

	if r.Type > 0 {
		w.WriteByte(r.Type)
	}

	logs := make([]*ethRLPLog, len(r.Logs))
	for k, l := range r.Logs {
		logs[k] = &ethRLPLog{Address: l.Address, Topics: l.Topics, Data: l.Data}
	}

	if len(r.PostState) > 0 {
		rlp.Encode(w, &struct {
			PostState         []byte
			CumulativeGasUsed uint64
			Bloom             block.Bloom
			Logs              []*ethRLPLog
		}{r.PostState, r.CumulativeGasUsed, r.Bloom, logs})
	} else {
		rlp.Encode(w, &struct {
			Status            uint64
			CumulativeGasUsed uint64
			Bloom             block.Bloom
			Logs              []*ethRLPLog
		}{r.Status, r.CumulativeGasUsed, r.Bloom, logs})
	}
}

// EthReceiptHash computes the Ethereum-standard receipt root hash.
func EthReceiptHash(receipts []*block.Receipt) types.Hash {
	return hash.DeriveSha(ethReceiptList(receipts))
}
