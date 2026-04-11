// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Ethereum-compatible receipt root computation. This is a copy of
// internal/ethel/receipt_hash.go intentionally local to evmsdk so the
// mobile SDK does not transitively depend on internal/ethel (which
// would pull in the freezer + executor and make the gomobile bind much
// larger). The two implementations must stay byte-identical.

package evmsdk

import (
	"bytes"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

type ethReceiptList []*block.Receipt

func (rs ethReceiptList) Len() int { return len(rs) }

type ethRLPLog struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
}

// EncodeIndex writes the Ethereum-standard RLP encoding of receipt i.
//
//	Post-Byzantium: RLP([Status, CumulativeGasUsed, Bloom, Logs])
//	EIP-2718:       type || RLP(...)
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

// EthReceiptHash computes the Ethereum-standard receipt root hash for a
// receipt list using the canonical MPT (erigon HashBuilder backend).
func EthReceiptHash(receipts []*block.Receipt) types.Hash {
	return hash.DeriveShaErigon(ethReceiptList(receipts))
}
