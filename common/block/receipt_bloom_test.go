// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// inlineBloomReceipt is Receipt as it was with an inline, exported Bloom.
type inlineBloomReceipt struct {
	Type              uint8  `json:"type,omitempty"`
	PostState         []byte `json:"root"`
	Status            uint64 `json:"status"`
	CumulativeGasUsed uint64 `json:"cumulativeGasUsed" gencodec:"required"`
	Logs              []*Log `json:"logs"              gencodec:"required"`

	TxHash          types.Hash    `json:"transactionHash" gencodec:"required"`
	ContractAddress types.Address `json:"contractAddress"`
	GasUsed         uint64        `json:"gasUsed" gencodec:"required"`

	BlockHash        types.Hash   `json:"blockHash,omitempty"`
	BlockNumber      *uint256.Int `json:"blockNumber,omitempty"`
	TransactionIndex uint         `json:"transactionIndex"`

	Bloom Bloom `json:"logsBloom" gencodec:"required"`
}

func bloomTestReceipts() []*Receipt {
	withLogs := &Receipt{Type: 2, Status: ReceiptStatusSuccessful, CumulativeGasUsed: 42000, GasUsed: 21000,
		TxHash: types.Hash{1}, BlockHash: types.Hash{2}, BlockNumber: uint256.NewInt(9), TransactionIndex: 3,
		Logs: []*Log{{Address: types.Address{7}, Topics: []types.Hash{{8}}, Data: []byte{1, 2}}}}
	withLogs.SetBloom(CreateBloom(Receipts{withLogs}))
	plain := &Receipt{Status: ReceiptStatusSuccessful, CumulativeGasUsed: 21000, GasUsed: 21000, TxHash: types.Hash{3},
		BlockNumber: uint256.NewInt(9)}
	return []*Receipt{withLogs, plain, {}}
}

func inlineOf(r *Receipt) inlineBloomReceipt {
	return inlineBloomReceipt{Type: r.Type, PostState: r.PostState, Status: r.Status, CumulativeGasUsed: r.CumulativeGasUsed,
		Logs: r.Logs, TxHash: r.TxHash, ContractAddress: r.ContractAddress, GasUsed: r.GasUsed, BlockHash: r.BlockHash,
		BlockNumber: r.BlockNumber, TransactionIndex: r.TransactionIndex, Bloom: r.GetBloom()}
}

// TestReceiptBloomJSONUnchanged: a receipt encodes to the JSON the inline
// field encoded to, as a pointer and as a value, and decodes back to an
// equal receipt.
func TestReceiptBloomJSONUnchanged(t *testing.T) {
	for i, r := range bloomTestReceipts() {
		want, err := json.Marshal(inlineOf(r))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []any{r, *r, []*Receipt{r}} {
			got, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			if _, isList := v.([]*Receipt); isList {
				want := append(append([]byte{'['}, want...), ']')
				if string(got) != string(want) {
					t.Fatalf("receipt %d list: %s, want %s", i, got, want)
				}
				continue
			}
			if string(got) != string(want) {
				t.Fatalf("receipt %d: %s, want %s", i, got, want)
			}
		}
		var back Receipt
		if err := json.Unmarshal(want, &back); err != nil {
			t.Fatal(err)
		}
		var wantBack inlineBloomReceipt
		if err := json.Unmarshal(want, &wantBack); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(inlineOf(&back), wantBack) {
			t.Fatalf("receipt %d: decoded %+v, want %+v", i, inlineOf(&back), wantBack)
		}
		if (back.bloom == nil) != (r.GetBloom() == Bloom{}) {
			t.Fatalf("receipt %d: zero bloom not held as none", i)
		}
	}
}

// TestReceiptBloomProtoRoundTrip: the proto bridge carries the bloom both
// ways, the zero bloom as none.
func TestReceiptBloomProtoRoundTrip(t *testing.T) {
	for i, r := range bloomTestReceipts() {
		enc, err := r.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var back Receipt
		if err := back.Unmarshal(enc); err != nil {
			t.Fatal(err)
		}
		if back.GetBloom() != r.GetBloom() || (back.bloom == nil) != (r.bloom == nil) {
			t.Fatalf("receipt %d: bloom did not round-trip", i)
		}
	}
	var r Receipt
	r.SetBloom(Bloom{})
	if r.bloom != nil {
		t.Fatal("SetBloom(zero) kept a bloom")
	}
}
