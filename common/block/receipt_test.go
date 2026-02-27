// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package block

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
)

func TestReceiptStatusConstants(t *testing.T) {
	if ReceiptStatusFailed != 0 {
		t.Errorf("ReceiptStatusFailed = %d, want 0", ReceiptStatusFailed)
	}
	if ReceiptStatusSuccessful != 1 {
		t.Errorf("ReceiptStatusSuccessful = %d, want 1", ReceiptStatusSuccessful)
	}
}

func TestReceiptMarshalUnmarshal(t *testing.T) {
	original := &Receipt{
		Type:              0,
		PostState:         []byte{0x01},
		Status:            ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		Logs: []*Log{
			{
				Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
				Topics:      []types.Hash{{0x01}},
				Data:        []byte{0x02},
				BlockNumber: uint256.NewInt(100),
			},
		},
		TxHash:           types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		ContractAddress:  types.Address{},
		GasUsed:          21000,
		BlockHash:        types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		BlockNumber:      uint256.NewInt(100),
		TransactionIndex: 0,
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Receipt.Marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Receipt.Marshal returned empty data")
	}

	decoded := &Receipt{}
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Receipt.Unmarshal error: %v", err)
	}

	if decoded.Status != original.Status {
		t.Error("Receipt.Unmarshal: Status mismatch")
	}
	if decoded.CumulativeGasUsed != original.CumulativeGasUsed {
		t.Error("Receipt.Unmarshal: CumulativeGasUsed mismatch")
	}
	if decoded.GasUsed != original.GasUsed {
		t.Error("Receipt.Unmarshal: GasUsed mismatch")
	}
	if decoded.TransactionIndex != original.TransactionIndex {
		t.Error("Receipt.Unmarshal: TransactionIndex mismatch")
	}
	if len(decoded.Logs) != len(original.Logs) {
		t.Error("Receipt.Unmarshal: Logs length mismatch")
	}
}

func TestReceiptUnmarshalInvalidData(t *testing.T) {
	r := &Receipt{}
	err := r.Unmarshal([]byte{0x00, 0x01, 0x02})
	if err == nil {
		t.Error("Receipt.Unmarshal should fail with invalid data")
	}
}

func TestReceiptsLen(t *testing.T) {
	rs := Receipts{
		&Receipt{Status: ReceiptStatusSuccessful, BlockNumber: uint256.NewInt(1)},
		&Receipt{Status: ReceiptStatusFailed, BlockNumber: uint256.NewInt(1)},
		&Receipt{Status: ReceiptStatusSuccessful, BlockNumber: uint256.NewInt(1)},
	}

	if rs.Len() != 3 {
		t.Errorf("Receipts.Len() = %d, want 3", rs.Len())
	}
}

func TestReceiptsEncodeIndex(t *testing.T) {
	rs := Receipts{
		&Receipt{
			Status:            ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			BlockNumber:       uint256.NewInt(100),
			Logs: []*Log{
				{
					Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
					Topics:      []types.Hash{{0x01}},
					Data:        []byte{0x02},
					BlockNumber: uint256.NewInt(100),
				},
			},
		},
	}

	var buf bytes.Buffer
	rs.EncodeIndex(0, &buf)

	if buf.Len() == 0 {
		t.Error("Receipts.EncodeIndex produced empty output")
	}
}

func TestReceiptsMarshalUnmarshal(t *testing.T) {
	original := Receipts{
		&Receipt{
			Type:              0,
			Status:            ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			GasUsed:           21000,
			Logs: []*Log{
				{
					Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
					Topics:      []types.Hash{{0x01}},
					Data:        []byte{0x02},
					BlockNumber: uint256.NewInt(100),
				},
			},
			TxHash:      types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
			BlockNumber: uint256.NewInt(100),
		},
		&Receipt{
			Type:              1,
			Status:            ReceiptStatusFailed,
			CumulativeGasUsed: 42000,
			GasUsed:           21000,
			Logs:              []*Log{},
			TxHash:            types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
			BlockNumber:       uint256.NewInt(100),
		},
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Receipts.Marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Receipts.Marshal returned empty data")
	}

	decoded := &Receipts{}
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Receipts.Unmarshal error: %v", err)
	}

	if len(*decoded) != len(original) {
		t.Errorf("Receipts.Unmarshal length mismatch: got %d, want %d", len(*decoded), len(original))
	}
}

func TestReceiptsUnmarshalInvalidData(t *testing.T) {
	rs := &Receipts{}
	err := rs.Unmarshal([]byte{0x00, 0x01, 0x02})
	if err == nil {
		t.Error("Receipts.Unmarshal should fail with invalid data")
	}
}

func TestReceiptsToProtoMessage(t *testing.T) {
	rs := Receipts{
		&Receipt{
			Status:            ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			GasUsed:           21000,
			BlockNumber:       uint256.NewInt(100),
		},
	}

	proto := rs.ToProtoMessage()
	if proto == nil {
		t.Error("Receipts.ToProtoMessage should not return nil")
	}
}

func BenchmarkReceiptMarshal(b *testing.B) {
	r := &Receipt{
		Status:            ReceiptStatusSuccessful,
		CumulativeGasUsed: 21000,
		GasUsed:           21000,
		Logs: []*Log{
			{
				Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
				Topics:      []types.Hash{{0x01}},
				Data:        []byte{0x02},
				BlockNumber: uint256.NewInt(100),
			},
		},
		BlockNumber: uint256.NewInt(100),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Marshal()
	}
}

func BenchmarkReceiptsEncodeIndex(b *testing.B) {
	rs := Receipts{
		&Receipt{
			Status:            ReceiptStatusSuccessful,
			CumulativeGasUsed: 21000,
			BlockNumber:       uint256.NewInt(100),
			Logs: []*Log{
				{
					Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
					Topics:      []types.Hash{{0x01}},
					Data:        []byte{0x02},
					BlockNumber: uint256.NewInt(100),
				},
			},
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf bytes.Buffer
		rs.EncodeIndex(0, &buf)
	}
}
