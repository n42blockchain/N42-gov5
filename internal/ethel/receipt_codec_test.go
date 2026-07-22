package ethel

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

func TestCompactNativeRootCrossClientVector(t *testing.T) {
	receipts := block.Receipts{{
		Type:              0,
		Status:            block.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		Logs:              []*block.Log{},
	}}
	if got, want := EncodeReceiptsCompact(receipts), []byte{0x14, 0x52, 0x08, 0x00}; !bytes.Equal(got, want) {
		t.Fatalf("compact receipt = %x, want %x", got, want)
	}
	wantRoot := types.HexToHash("0x9ec602b25fc63e86a5feb8943d52cf66b24ed8e8021f3f74f077271ffae88c75")
	if got := hash.DeriveSha(receipts); got != wantRoot {
		t.Fatalf("native receipt root = %s, want %s", got, wantRoot)
	}
}

func makeTestReceipts() block.Receipts {
	return block.Receipts{
		// Simple transfer: no logs, Legacy type.
		{
			Type:              0,
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			Logs:              []*block.Log{},
		},
		// EIP-2930 AccessList (type 1).
		{
			Type:              1,
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 42000,
			Logs:              []*block.Log{},
		},
		// EIP-1559 DynamicFee (type 2): 2 logs with topics.
		{
			Type:              2,
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 150000,
			Logs: []*block.Log{
				{
					Address: types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7"),
					Topics: []types.Hash{
						types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"),
						types.HexToHash("0x0000000000000000000000001234567890abcdef1234567890abcdef12345678"),
						types.HexToHash("0x000000000000000000000000abcdefabcdefabcdefabcdefabcdefabcdefabcd"),
					},
					Data: make([]byte, 32),
				},
				{
					Address: types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7"),
					Topics: []types.Hash{
						types.HexToHash("0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"),
					},
					Data: make([]byte, 64),
				},
			},
		},
		// Failed tx (type 2).
		{
			Type:              2,
			Status:            block.ReceiptStatusFailed,
			CumulativeGasUsed: 250000,
			Logs:              []*block.Log{},
		},
		// EIP-4844 Blob (type 3) — extended type.
		{
			Type:              3,
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 300000,
			Logs:              []*block.Log{},
		},
		// EIP-7702 SetCode (type 4) — extended type.
		{
			Type:              4,
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 400000,
			Logs:              []*block.Log{},
		},
		// Edge case: gas = 0 (gasLen = 0 bytes).
		{
			Type:              0,
			Status:            block.ReceiptStatusFailed,
			CumulativeGasUsed: 0,
			Logs:              []*block.Log{},
		},
	}
}

func TestCompactReceiptRoundtrip(t *testing.T) {
	original := makeTestReceipts()
	encoded := EncodeReceiptsCompact(original)

	decoded, err := DecodeReceiptsCompact(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(decoded) != len(original) {
		t.Fatalf("count: got %d, want %d", len(decoded), len(original))
	}

	for i, r := range decoded {
		o := original[i]
		if r.Status != o.Status {
			t.Errorf("receipt %d: status %d != %d", i, r.Status, o.Status)
		}
		if r.Type != o.Type {
			t.Errorf("receipt %d: type %d != %d", i, r.Type, o.Type)
		}
		if r.CumulativeGasUsed != o.CumulativeGasUsed {
			t.Errorf("receipt %d: gas %d != %d", i, r.CumulativeGasUsed, o.CumulativeGasUsed)
		}
		if len(r.Logs) != len(o.Logs) {
			t.Errorf("receipt %d: logs %d != %d", i, len(r.Logs), len(o.Logs))
			continue
		}
		for j, l := range r.Logs {
			ol := o.Logs[j]
			if l.Address != ol.Address {
				t.Errorf("receipt %d log %d: addr mismatch", i, j)
			}
			if len(l.Topics) != len(ol.Topics) {
				t.Errorf("receipt %d log %d: topics %d != %d", i, j, len(l.Topics), len(ol.Topics))
			}
			if len(l.Data) != len(ol.Data) {
				t.Errorf("receipt %d log %d: data len %d != %d", i, j, len(l.Data), len(ol.Data))
			}
		}
	}
}

// TestCompactExtendedTypes verifies types >= 3 use the extended byte path.
func TestCompactExtendedTypes(t *testing.T) {
	for _, txType := range []uint8{3, 4, 5, 127} {
		r := block.Receipts{{
			Type:              txType,
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 100000,
			Logs:              []*block.Log{},
		}}
		data := EncodeReceiptsCompact(r)
		// flags byte should have identifier 3.
		if data[0]&0x03 != 3 {
			t.Errorf("type %d: identifier bits = %d, want 3", txType, data[0]&0x03)
		}
		// Second byte should be actual type.
		if data[1] != txType {
			t.Errorf("type %d: extended byte = %d", txType, data[1])
		}
		decoded, err := DecodeReceiptsCompact(data)
		if err != nil {
			t.Fatalf("type %d: decode: %v", txType, err)
		}
		if decoded[0].Type != txType {
			t.Errorf("type %d: roundtrip got %d", txType, decoded[0].Type)
		}
	}
}

func TestCompactVsRLP(t *testing.T) {
	receipts := makeTestReceipts()

	compactData := EncodeReceiptsCompact(receipts)
	rlpData, err := rlp.EncodeToBytes(receipts)
	if err != nil {
		t.Fatalf("rlp encode: %v", err)
	}

	t.Logf("Compact: %d bytes", len(compactData))
	t.Logf("RLP:     %d bytes", len(rlpData))
	t.Logf("Ratio:   %.1f%% (compact/rlp)", float64(len(compactData))/float64(len(rlpData))*100)
	t.Logf("Saved:   %d bytes (%.0f%%)", len(rlpData)-len(compactData),
		float64(len(rlpData)-len(compactData))/float64(len(rlpData))*100)
}

func TestCompactEmpty(t *testing.T) {
	data := EncodeReceiptsCompact(nil)
	if data != nil {
		t.Error("nil receipts should produce nil")
	}
	decoded, err := DecodeReceiptsCompact(nil)
	if err != nil || decoded != nil {
		t.Error("nil decode should be nil")
	}
}

func BenchmarkEncodeCompact(b *testing.B) {
	receipts := makeTestReceipts()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EncodeReceiptsCompact(receipts)
	}
}

func BenchmarkEncodeRLP(b *testing.B) {
	receipts := makeTestReceipts()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rlp.EncodeToBytes(receipts)
	}
}

func BenchmarkDecodeCompact(b *testing.B) {
	data := EncodeReceiptsCompact(makeTestReceipts())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DecodeReceiptsCompact(data)
	}
}
