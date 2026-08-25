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

func TestEthReceiptEncodingMatchesReference(t *testing.T) {
	for _, tc := range []struct {
		name       string
		typ        uint8
		status     uint64
		cumulative uint64
		data       []byte
		topics     []types.Hash
	}{
		{name: "empty legacy"},
		{name: "typed single byte", typ: 2, status: 1, cumulative: 127, data: []byte{0x7f}},
		{name: "single byte with prefix", typ: 3, status: 1, cumulative: 128, data: []byte{0x80}},
		{name: "short boundary", typ: 4, status: 1, cumulative: 30_000_000, data: bytes.Repeat([]byte{0xaa}, 55)},
		{name: "long boundary", typ: 4, status: 1, cumulative: 30_000_000, data: bytes.Repeat([]byte{0xbb}, 56)},
		{name: "topics", typ: 2, status: 1, cumulative: 21_000, data: bytes.Repeat([]byte{0xcc}, 257), topics: []types.Hash{{1}, {2}, {3}, {4}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := &block.Receipt{Type: tc.typ, Status: tc.status, CumulativeGasUsed: tc.cumulative}
			if tc.data != nil || tc.topics != nil {
				receipt.Logs = []*block.Log{{Address: types.Address{0x80}, Topics: tc.topics, Data: tc.data}}
				receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
			}
			var got, want bytes.Buffer
			ethReceiptList{receipt}.EncodeIndex(0, &got)
			referenceEthReceiptList{receipt}.EncodeIndex(0, &want)
			if !bytes.Equal(got.Bytes(), want.Bytes()) {
				t.Fatalf("encoding mismatch:\n got %x\nwant %x", got.Bytes(), want.Bytes())
			}
		})
	}
}

func BenchmarkEthReceiptEncodeIndex(b *testing.B) {
	receipt := &block.Receipt{
		Type:              2,
		Status:            block.ReceiptStatusSuccessful,
		CumulativeGasUsed: 30_000_000,
	}
	for i := 0; i < 3; i++ {
		receipt.Logs = append(receipt.Logs, &block.Log{
			Address: types.Address{byte(i + 1)},
			Topics: []types.Hash{
				{byte(i + 1)}, {byte(i + 2)}, {byte(i + 3)}, {byte(i + 4)},
			},
			Data: bytes.Repeat([]byte{byte(0x80 + i)}, 128),
		})
	}
	receipt.Bloom = block.CreateBloom(block.Receipts{receipt})
	receipts := ethReceiptList{receipt}
	var buf bytes.Buffer
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Reset()
		receipts.EncodeIndex(0, &buf)
	}
}
