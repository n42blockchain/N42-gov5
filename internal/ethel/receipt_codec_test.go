package ethel

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

func makeTestReceipts() block.Receipts {
	return block.Receipts{
		// Simple transfer: no logs.
		{
			Type:              0,
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			Logs:              []*block.Log{},
		},
		// Contract call: 2 logs with topics.
		{
			Type:              2, // DynamicFee
			Status:            block.ReceiptStatusSuccessful,
			CumulativeGasUsed: 150000,
			Logs: []*block.Log{
				{
					Address: types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7"),
					Topics: []types.Hash{
						types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"), // Transfer
						types.HexToHash("0x0000000000000000000000001234567890abcdef1234567890abcdef12345678"),
						types.HexToHash("0x000000000000000000000000abcdefabcdefabcdefabcdefabcdefabcdefabcd"),
					},
					Data: make([]byte, 32), // amount
				},
				{
					Address: types.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7"),
					Topics: []types.Hash{
						types.HexToHash("0x8c5be1e5ebec7d5bd14f71427d1e84f3dd0314c0f7b2291e5b200ac8c7c3b925"), // Approval
					},
					Data: make([]byte, 64),
				},
			},
		},
		// Failed tx.
		{
			Type:              2,
			Status:            block.ReceiptStatusFailed,
			CumulativeGasUsed: 250000,
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
