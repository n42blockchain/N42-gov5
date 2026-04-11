// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

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

	logs := make([]*ethRLPLog, len(r.Logs))
	for k, l := range r.Logs {
		logs[k] = &ethRLPLog{Address: l.Address, Topics: l.Topics, Data: l.Data}
	}

	var bloom block.Bloom
	copy(bloom[:], block.LogsBloom(r.Logs))

	rlp.Encode(w, &struct {
		Status            uint64
		CumulativeGasUsed uint64
		Bloom             block.Bloom
		Logs              []*ethRLPLog
	}{r.Status, r.CumulativeGasUsed, bloom, logs})
}

// EthReceiptHash computes the Ethereum-standard receipt root hash
// using the canonical MPT (erigon HashBuilder backend).
func EthReceiptHash(receipts []*block.Receipt) types.Hash {
	return hash.DeriveShaErigon(ethReceiptList(receipts))
}
