// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

func receiptsForCompactTest() Receipts {
	logA := &Log{
		BlockNumber: uint256.NewInt(7),
		Address:     types.Address{0x01},
		Topics:      []types.Hash{{0xa1}, {0xa2}, {0xa3}},
		Data:        []byte("transfer-event-data"),
	}
	logB := &Log{
		BlockNumber: uint256.NewInt(7),
		Address:     types.Address{0x02},
		Topics:      []types.Hash{{0xb1}},
	}
	withLogs := &Receipt{Type: 0, Status: 1, CumulativeGasUsed: 53000, Logs: []*Log{logA, logB}, BlockNumber: uint256.NewInt(7)}
	withLogs.Bloom = CreateBloom(Receipts{withLogs})

	failed := &Receipt{Type: 2, Status: 0, CumulativeGasUsed: 74000, Logs: nil, BlockNumber: uint256.NewInt(7)}
	failed.Bloom = CreateBloom(Receipts{failed})

	postState := &Receipt{PostState: bytes.Repeat([]byte{0x42}, 32), CumulativeGasUsed: 100000, BlockNumber: uint256.NewInt(7)}
	postState.Bloom = CreateBloom(Receipts{postState})

	// Contract-creation receipt: ContractAddress must survive (not derivable from
	// the receipt alone, and the read path does not re-derive it).
	creation := &Receipt{Type: 0, Status: 1, CumulativeGasUsed: 153000, ContractAddress: types.Address{0xde, 0xad, 0xbe, 0xef, 0x05}, BlockNumber: uint256.NewInt(7)}
	creation.Bloom = CreateBloom(Receipts{creation})

	return Receipts{withLogs, failed, postState, creation}
}

// TestCompactReceiptsRoundTrip: consensus fields and recomputed Bloom must be
// exact; the receipt-root inputs (EncodeIndex: Status/CumGas/Logs) identical.
func TestCompactReceiptsRoundTrip(t *testing.T) {
	rs := receiptsForCompactTest()
	enc := rs.MarshalCompact()
	if !IsCompactReceipts(enc) {
		t.Fatal("marker missing")
	}
	var got Receipts
	if err := got.Unmarshal(enc); err != nil { // via the dispatching Unmarshal
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != len(rs) {
		t.Fatalf("count: %d vs %d", len(got), len(rs))
	}
	for i := range rs {
		w, g := rs[i], got[i]
		if w.Type != g.Type || w.Status != g.Status || w.CumulativeGasUsed != g.CumulativeGasUsed {
			t.Fatalf("receipt %d: consensus scalars diverged", i)
		}
		if !bytes.Equal(w.PostState, g.PostState) {
			t.Fatalf("receipt %d: PostState diverged", i)
		}
		if w.ContractAddress != g.ContractAddress {
			t.Fatalf("receipt %d: ContractAddress diverged: %x vs %x", i, w.ContractAddress, g.ContractAddress)
		}
		if w.Bloom != g.Bloom {
			t.Fatalf("receipt %d: recomputed Bloom diverged", i)
		}
		if len(w.Logs) != len(g.Logs) {
			t.Fatalf("receipt %d: log count diverged", i)
		}
		for j := range w.Logs {
			if w.Logs[j].Address != g.Logs[j].Address ||
				len(w.Logs[j].Topics) != len(g.Logs[j].Topics) ||
				!bytes.Equal(w.Logs[j].Data, g.Logs[j].Data) {
				t.Fatalf("receipt %d log %d diverged", i, j)
			}
		}
	}
	// Receipt root derivation inputs identical: EncodeIndex byte-equality.
	for i := range rs {
		var a, b bytes.Buffer
		rs.EncodeIndex(i, &a)
		got.EncodeIndex(i, &b)
		if !bytes.Equal(a.Bytes(), b.Bytes()) {
			t.Fatalf("receipt %d: EncodeIndex (root input) diverged", i)
		}
	}
}

// TestCompactReceiptsProtoCrossCheck: proto and compact decode through the same
// Unmarshal to the same consensus content (mixed-table guarantee).
func TestCompactReceiptsProtoCrossCheck(t *testing.T) {
	rs := receiptsForCompactTest()
	protoEnc, err := rs.Marshal()
	if err != nil {
		t.Fatalf("proto marshal: %v", err)
	}
	if IsCompactReceipts(protoEnc) {
		t.Fatal("proto encoding collides with compact marker")
	}
	var fromProto, fromCompact Receipts
	if err := fromProto.Unmarshal(protoEnc); err != nil {
		t.Fatalf("proto unmarshal: %v", err)
	}
	if err := fromCompact.Unmarshal(rs.MarshalCompact()); err != nil {
		t.Fatalf("compact unmarshal: %v", err)
	}
	for i := range fromProto {
		var a, b bytes.Buffer
		fromProto.EncodeIndex(i, &a)
		fromCompact.EncodeIndex(i, &b)
		if !bytes.Equal(a.Bytes(), b.Bytes()) {
			t.Fatalf("receipt %d: proto vs compact EncodeIndex diverged", i)
		}
	}
	pe, _ := rs.Marshal()
	ce := rs.MarshalCompact()
	t.Logf("3 receipts: proto=%dB compact=%dB (%.1fx)", len(pe), len(ce), float64(len(pe))/float64(len(ce)))
}
