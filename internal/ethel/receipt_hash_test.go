// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

type referenceEthReceiptList []*block.Receipt

func (rs referenceEthReceiptList) Len() int { return len(rs) }

func (rs referenceEthReceiptList) EncodeIndex(i int, w *bytes.Buffer) {
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

func TestEthReceiptHashReusesExecutionBloom(t *testing.T) {
	receipts := []*block.Receipt{
		{
			Type:              2,
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 42_000,
			Logs: []*block.Log{{
				Address: types.HexToAddress("0x1234"),
				Topics:  []types.Hash{types.HexToHash("0xabcd"), types.HexToHash("0x9876")},
				Data:    []byte{1, 2, 3, 4},
			}},
		},
		{
			Status:            block.ReceiptStatusFailed,
			CumulativeGasUsed: 63_000,
		},
	}
	for _, receipt := range receipts {
		receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
	}

	want := hash.DeriveShaErigon(referenceEthReceiptList(receipts))
	if got := EthReceiptHash(receipts); got != want {
		t.Fatalf("execution bloom root mismatch: got %s want %s", got, want)
	}

	// Compact receipt decoders omit Bloom. The compatibility fallback must
	// preserve the same root for callers outside the replay execution path.
	receipts[0].Bloom = block.Bloom{}
	if got := EthReceiptHash(receipts); got != want {
		t.Fatalf("missing bloom fallback root mismatch: got %s want %s", got, want)
	}
}
