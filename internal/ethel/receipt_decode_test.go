// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"testing"

	"github.com/golang/snappy"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

// ethTGethReceiptForStorage mirrors Geth's ReceiptForStorage RLP shape:
// optionally prefixed with a raw type byte (<0x80), followed by
// rlp([statusOrPostState, cumulativeGas, logs]).
type ethTGethLogShape struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
}

func ethTEncodeGethReceipt(t *testing.T, typ byte, legacy bool, status uint64, postState []byte, cumGas uint64, logs []ethTGethLogShape) []byte {
	t.Helper()
	logRaws := make([]interface{}, len(logs))
	for i, l := range logs {
		logRaws[i] = []interface{}{l.Address, l.Topics, l.Data}
	}
	var statusField interface{}
	if legacy {
		statusField = postState
	} else {
		statusField = status
	}
	body, err := rlp.EncodeToBytes([]interface{}{statusField, cumGas, logRaws})
	if err != nil {
		t.Fatal(err)
	}
	if typ == 0 {
		return body
	}
	// Typed receipt: a raw type byte followed by the body, the whole
	// thing then wrapped as an RLP string by the caller when building
	// the top-level receipts list (decodeGethReceipt unwraps it first).
	withType := append([]byte{typ}, body...)
	wrapped, err := rlp.EncodeToBytes(withType)
	if err != nil {
		t.Fatal(err)
	}
	return wrapped
}

func TestDecodeGethReceiptsLegacyPostByzantiumStatus(t *testing.T) {
	logs := []ethTGethLogShape{{
		Address: types.HexToAddress("0xaa"),
		Topics:  []types.Hash{types.HexToHash("0x01")},
		Data:    []byte{1, 2, 3},
	}}
	r0 := ethTEncodeGethReceipt(t, 0, true, 0, []byte{1}, 21000, logs)
	r1 := ethTEncodeGethReceipt(t, 0, true, 0, []byte{0}, 42000, nil)

	list, err := rlp.EncodeToBytes([]rlp.RawValue{r0, r1})
	if err != nil {
		t.Fatal(err)
	}
	compressed := snappy.Encode(nil, list)

	receipts, err := DecodeGethReceipts(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 2 {
		t.Fatalf("got %d receipts, want 2", len(receipts))
	}
	if receipts[0].Status != 1 {
		t.Fatalf("receipts[0].Status = %d, want 1 (success)", receipts[0].Status)
	}
	if len(receipts[0].Logs) != 1 || receipts[0].Logs[0].Address != types.HexToAddress("0xaa") {
		t.Fatalf("receipts[0].Logs unexpected: %+v", receipts[0].Logs)
	}
	if receipts[1].Status != 0 {
		t.Fatalf("receipts[1].Status = %d, want 0 (failed)", receipts[1].Status)
	}
	if receipts[1].CumulativeGasUsed != 42000 {
		t.Fatalf("receipts[1].CumulativeGasUsed = %d, want 42000", receipts[1].CumulativeGasUsed)
	}
}

func TestDecodeGethReceiptsPreByzantiumPostState(t *testing.T) {
	root := make([]byte, 32)
	root[0] = 0xAB
	r0 := ethTEncodeGethReceipt(t, 0, true, 0, root, 100, nil)
	list, err := rlp.EncodeToBytes([]rlp.RawValue{r0})
	if err != nil {
		t.Fatal(err)
	}
	compressed := snappy.Encode(nil, list)

	receipts, err := DecodeGethReceipts(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 || receipts[0].Status != 1 {
		t.Fatalf("pre-Byzantium receipt should report success, got %+v", receipts)
	}
}

func TestDecodeGethReceiptsTypedReceipt(t *testing.T) {
	r0 := ethTEncodeGethReceipt(t, 2, false, 1, nil, 5000, []ethTGethLogShape{{
		Address: types.HexToAddress("0xbb"),
		Topics:  nil,
		Data:    nil,
	}})
	list, err := rlp.EncodeToBytes([]rlp.RawValue{r0})
	if err != nil {
		t.Fatal(err)
	}
	compressed := snappy.Encode(nil, list)

	receipts, err := DecodeGethReceipts(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 {
		t.Fatalf("got %d receipts, want 1", len(receipts))
	}
	if receipts[0].Type != 2 {
		t.Fatalf("receipts[0].Type = %d, want 2", receipts[0].Type)
	}
	if receipts[0].Status != 1 {
		t.Fatalf("receipts[0].Status = %d, want 1", receipts[0].Status)
	}
}

func TestDecodeGethReceiptsEmptyBlock(t *testing.T) {
	compressed := snappy.Encode(nil, []byte{})
	receipts, err := DecodeGethReceipts(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if receipts != nil {
		t.Fatalf("expected nil receipts for an empty block, got %+v", receipts)
	}
}

func TestDecodeGethReceiptsBadSnappy(t *testing.T) {
	if _, err := DecodeGethReceipts([]byte{0xff, 0xff, 0xff}); err == nil {
		t.Fatal("expected decompression error for invalid snappy data")
	}
}

func TestDecodeGethReceiptsMalformedReceiptList(t *testing.T) {
	compressed := snappy.Encode(nil, []byte{0x01}) // not a valid RLP list
	if _, err := DecodeGethReceipts(compressed); err == nil {
		t.Fatal("expected error decoding malformed receipts list")
	}
}

func TestDecodeGethReceiptsTooFewFields(t *testing.T) {
	body, err := rlp.EncodeToBytes([]interface{}{uint64(1), uint64(100)}) // only 2 fields
	if err != nil {
		t.Fatal(err)
	}
	list, err := rlp.EncodeToBytes([]rlp.RawValue{body})
	if err != nil {
		t.Fatal(err)
	}
	compressed := snappy.Encode(nil, list)
	if _, err := DecodeGethReceipts(compressed); err == nil {
		t.Fatal("expected error for a receipt with too few fields")
	}
}

func TestDecodeGethReceiptsMalformedLog(t *testing.T) {
	// Log with only 2 fields instead of 3.
	badLog := []interface{}{types.HexToAddress("0xaa"), []types.Hash{}}
	body, err := rlp.EncodeToBytes([]interface{}{uint64(1), uint64(100), []interface{}{badLog}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := rlp.EncodeToBytes([]rlp.RawValue{body})
	if err != nil {
		t.Fatal(err)
	}
	compressed := snappy.Encode(nil, list)
	if _, err := DecodeGethReceipts(compressed); err == nil {
		t.Fatal("expected error for a log with too few fields")
	}
}
