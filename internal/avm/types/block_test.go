package types

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/avmutil"
)

func TestEncodeNonceAndUint64(t *testing.T) {
	n := EncodeNonce(0x0102030405060708)
	if got := n.Uint64(); got != 0x0102030405060708 {
		t.Fatalf("Uint64() = %#x, want %#x", got, uint64(0x0102030405060708))
	}
}

func TestBlockNonceMarshalUnmarshalText(t *testing.T) {
	n := EncodeNonce(42)
	text, err := n.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error: %v", err)
	}
	var n2 BlockNonce
	if err := n2.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText error: %v", err)
	}
	if n != n2 {
		t.Fatalf("round-trip mismatch: %v != %v", n, n2)
	}
}

func TestBlockNonceUnmarshalTextInvalid(t *testing.T) {
	var n BlockNonce
	if err := n.UnmarshalText([]byte("zz")); err == nil {
		t.Fatal("expected error for invalid hex")
	}
}

func sampleHeader() *Header {
	return &Header{
		ParentHash:  avmutil.BytesToHash([]byte{0x01}),
		UncleHash:   avmutil.BytesToHash([]byte{0x02}),
		Coinbase:    avmutil.BytesToAddress([]byte{0x03}),
		Root:        avmutil.BytesToHash([]byte{0x04}),
		TxHash:      avmutil.BytesToHash([]byte{0x05}),
		ReceiptHash: avmutil.BytesToHash([]byte{0x06}),
		Difficulty:  big.NewInt(100),
		Number:      big.NewInt(1),
		GasLimit:    8000000,
		GasUsed:     21000,
		Time:        123456,
		Extra:       []byte{0xDE, 0xAD},
		MixDigest:   avmutil.BytesToHash([]byte{0x07}),
		Nonce:       EncodeNonce(99),
	}
}

func TestHeaderHash(t *testing.T) {
	h := sampleHeader()
	hash1 := h.Hash()
	hash2 := h.Hash()
	if hash1 != hash2 {
		t.Fatal("Hash() should be deterministic")
	}

	h2 := sampleHeader()
	h2.Number = big.NewInt(2)
	if h2.Hash() == hash1 {
		t.Fatal("different headers should produce different hashes")
	}
}

func TestHeaderSize(t *testing.T) {
	h := sampleHeader()
	size1 := h.Size()
	if size1 == 0 {
		t.Fatal("Size() should be non-zero")
	}

	h.BaseFee = big.NewInt(1_000_000_000)
	size2 := h.Size()
	if size2 < size1 {
		t.Fatal("Size() should grow when BaseFee is set")
	}

	empty := &Header{}
	if empty.Size() == 0 {
		t.Fatal("Size() on an empty header should still return the struct's base size")
	}
}

func TestHeaderMarshalUnmarshalJSONRoundTrip(t *testing.T) {
	h := sampleHeader()
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}

	var got Header
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if got.ParentHash != h.ParentHash || got.UncleHash != h.UncleHash || got.Coinbase != h.Coinbase ||
		got.GasLimit != h.GasLimit || got.GasUsed != h.GasUsed || got.Time != h.Time ||
		got.Number.Cmp(h.Number) != 0 || got.Difficulty.Cmp(h.Difficulty) != 0 {
		t.Fatalf("round-trip mismatch: got %#v, want %#v", got, h)
	}
}

func TestHeaderMarshalJSONIncludesForkFields(t *testing.T) {
	h := sampleHeader()
	withdrawals := avmutil.BytesToHash([]byte{0x08})
	parentBeacon := avmutil.BytesToHash([]byte{0x09})
	requests := avmutil.BytesToHash([]byte{0x0A})
	blobUsed := uint64(1)
	excessBlob := uint64(2)
	h.BaseFee = big.NewInt(7)
	h.WithdrawalsHash = &withdrawals
	h.BlobGasUsed = &blobUsed
	h.ExcessBlobGas = &excessBlob
	h.ParentBeaconRoot = &parentBeacon
	h.RequestsHash = &requests

	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var got Header
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if got.BaseFee == nil || got.BaseFee.Cmp(h.BaseFee) != 0 {
		t.Errorf("BaseFee mismatch: %v", got.BaseFee)
	}
	if got.WithdrawalsHash == nil || *got.WithdrawalsHash != withdrawals {
		t.Errorf("WithdrawalsHash mismatch: %v", got.WithdrawalsHash)
	}
	if got.BlobGasUsed == nil || *got.BlobGasUsed != blobUsed {
		t.Errorf("BlobGasUsed mismatch: %v", got.BlobGasUsed)
	}
	if got.ExcessBlobGas == nil || *got.ExcessBlobGas != excessBlob {
		t.Errorf("ExcessBlobGas mismatch: %v", got.ExcessBlobGas)
	}
	if got.ParentBeaconRoot == nil || *got.ParentBeaconRoot != parentBeacon {
		t.Errorf("ParentBeaconRoot mismatch: %v", got.ParentBeaconRoot)
	}
	if got.RequestsHash == nil || *got.RequestsHash != requests {
		t.Errorf("RequestsHash mismatch: %v", got.RequestsHash)
	}

	// The marshaled "hash" field should be present and match h.Hash().
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal to map error: %v", err)
	}
	if _, ok := raw["hash"]; !ok {
		t.Error("marshaled header JSON should include a 'hash' field")
	}
}

func TestHeaderUnmarshalJSONMissingRequiredFields(t *testing.T) {
	base := map[string]interface{}{
		"parentHash":       "0x" + "01",
		"sha3Uncles":       "0x" + "02",
		"stateRoot":        "0x" + "03",
		"transactionsRoot": "0x" + "04",
		"receiptsRoot":     "0x" + "05",
		"logsBloom":        "0x" + "00",
		"difficulty":       "0x1",
		"number":           "0x1",
		"gasLimit":         "0x1",
		"gasUsed":          "0x1",
		"timestamp":        "0x1",
		"extraData":        "0x",
	}
	requiredKeys := []string{"parentHash", "sha3Uncles", "stateRoot", "transactionsRoot", "receiptsRoot", "difficulty", "number", "gasLimit", "gasUsed", "timestamp", "extraData"}
	// logsBloom must be a full 256-byte value to parse; build a valid full header first
	// and then delete one required field at a time.
	full := sampleHeader()
	data, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var fullMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &fullMap); err != nil {
		t.Fatalf("Unmarshal to map error: %v", err)
	}

	for _, key := range requiredKeys {
		t.Run(key, func(t *testing.T) {
			m := make(map[string]json.RawMessage, len(fullMap))
			for k, v := range fullMap {
				m[k] = v
			}
			delete(m, key)
			payload, err := json.Marshal(m)
			if err != nil {
				t.Fatalf("Marshal error: %v", err)
			}
			var h Header
			if err := json.Unmarshal(payload, &h); err == nil {
				t.Errorf("expected error when %q is missing", key)
			}
		})
	}
	_ = base
}

func TestHeaderUnmarshalJSONOptionalFieldsDefault(t *testing.T) {
	full := sampleHeader()
	data, err := json.Marshal(full)
	if err != nil {
		t.Fatalf("Marshal error: %v", err)
	}
	var h Header
	if err := json.Unmarshal(data, &h); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if h.BaseFee != nil {
		t.Errorf("expected nil BaseFee when omitted, got %v", h.BaseFee)
	}
	if h.WithdrawalsHash != nil || h.BlobGasUsed != nil || h.ExcessBlobGas != nil || h.ParentBeaconRoot != nil || h.RequestsHash != nil {
		t.Errorf("expected nil optional fork fields when omitted")
	}
}

func TestHeaderUnmarshalJSONInvalid(t *testing.T) {
	var h Header
	if err := h.UnmarshalJSON([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestUint64PtrToHexutilAndBack(t *testing.T) {
	if uint64PtrToHexutil(nil) != nil {
		t.Error("uint64PtrToHexutil(nil) should be nil")
	}
	if hexutilUint64PtrToUint64(nil) != nil {
		t.Error("hexutilUint64PtrToUint64(nil) should be nil")
	}

	v := uint64(42)
	hx := uint64PtrToHexutil(&v)
	if hx == nil || uint64(*hx) != v {
		t.Fatalf("uint64PtrToHexutil(%d) = %v", v, hx)
	}
	back := hexutilUint64PtrToUint64(hx)
	if back == nil || *back != v {
		t.Fatalf("hexutilUint64PtrToUint64 round trip = %v, want %d", back, v)
	}
}
