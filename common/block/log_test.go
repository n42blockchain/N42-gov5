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

func TestLogFields(t *testing.T) {
	log := &Log{
		Address:     types.Address{0x01},
		Topics:      []types.Hash{{0x02}, {0x03}},
		Data:        []byte{0x04, 0x05},
		BlockNumber: uint256.NewInt(100),
		TxHash:      types.Hash{0x06},
		TxIndex:     1,
		BlockHash:   types.Hash{0x07},
		Index:       0,
		Removed:     false,
	}

	if log.Address[0] != 0x01 {
		t.Error("Log.Address mismatch")
	}
	if len(log.Topics) != 2 {
		t.Errorf("Log.Topics length = %d, want 2", len(log.Topics))
	}
	if len(log.Data) != 2 {
		t.Errorf("Log.Data length = %d, want 2", len(log.Data))
	}
	if log.BlockNumber.Uint64() != 100 {
		t.Error("Log.BlockNumber mismatch")
	}
}

func TestLogToProtoMessage(t *testing.T) {
	log := &Log{
		Address:     types.Address{0x01},
		Topics:      []types.Hash{{0x02}},
		Data:        []byte{0x03},
		BlockNumber: uint256.NewInt(100),
		TxHash:      types.Hash{0x04},
		TxIndex:     1,
		BlockHash:   types.Hash{0x05},
		Index:       0,
		Removed:     false,
	}

	proto := log.ToProtoMessage()
	if proto == nil {
		t.Error("Log.ToProtoMessage should not return nil")
	}
}

func TestLogsType(t *testing.T) {
	logs := Logs{
		&Log{Address: types.Address{0x01}},
		&Log{Address: types.Address{0x02}},
	}

	if len(logs) != 2 {
		t.Errorf("Logs length = %d, want 2", len(logs))
	}
}

func TestLogFromProtoMessage(t *testing.T) {
	original := &Log{
		Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
		Topics:      []types.Hash{{0x01}, {0x02}},
		Data:        []byte{0x03, 0x04},
		BlockNumber: uint256.NewInt(100),
		TxHash:      types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
		TxIndex:     1,
		BlockHash:   types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111"),
		Index:       0,
		Removed:     false,
	}

	protoMsg := original.ToProtoMessage()
	decoded := &Log{}
	err := decoded.FromProtoMessage(protoMsg)
	if err != nil {
		t.Fatalf("Log.FromProtoMessage error: %v", err)
	}

	if decoded.Address != original.Address {
		t.Error("Log.FromProtoMessage: Address mismatch")
	}
	if len(decoded.Topics) != len(original.Topics) {
		t.Error("Log.FromProtoMessage: Topics length mismatch")
	}
	if !bytes.Equal(decoded.Data, original.Data) {
		t.Error("Log.FromProtoMessage: Data mismatch")
	}
	if decoded.TxIndex != original.TxIndex {
		t.Error("Log.FromProtoMessage: TxIndex mismatch")
	}
	if decoded.Index != original.Index {
		t.Error("Log.FromProtoMessage: Index mismatch")
	}
	if decoded.Removed != original.Removed {
		t.Error("Log.FromProtoMessage: Removed mismatch")
	}
}

func TestLogFromProtoMessageTypeError(t *testing.T) {
	log := &Log{}
	err := log.FromProtoMessage(nil)
	if err == nil {
		t.Error("Log.FromProtoMessage should fail with nil message")
	}
}

func TestLogsMarshalUnmarshal(t *testing.T) {
	original := Logs{
		&Log{
			Address:     types.HexToAddress("0x1234567890123456789012345678901234567890"),
			Topics:      []types.Hash{{0x01}},
			Data:        []byte{0x02},
			BlockNumber: uint256.NewInt(100),
			TxHash:      types.HexToHash("0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"),
			TxIndex:     1,
		},
		&Log{
			Address:     types.HexToAddress("0xabcdefabcdefabcdefabcdefabcdefabcdefabcd"),
			Topics:      []types.Hash{{0x03}, {0x04}},
			Data:        []byte{0x05, 0x06},
			BlockNumber: uint256.NewInt(101),
			TxIndex:     2,
		},
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Logs.Marshal error: %v", err)
	}
	if len(data) == 0 {
		t.Error("Logs.Marshal returned empty data")
	}

	var decoded Logs
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Logs.Unmarshal error: %v", err)
	}

	if len(decoded) != len(original) {
		t.Fatalf("Logs.Unmarshal length mismatch: got %d, want %d", len(decoded), len(original))
	}

	if decoded[0].Address != original[0].Address {
		t.Error("Logs.Unmarshal: Address mismatch")
	}
	if decoded[0].TxIndex != original[0].TxIndex {
		t.Error("Logs.Unmarshal: TxIndex mismatch")
	}
	if !bytes.Equal(decoded[0].Data, original[0].Data) {
		t.Error("Logs.Unmarshal: Data mismatch")
	}
}

func TestLogsUnmarshalEmpty(t *testing.T) {
	original := Logs{}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Logs.Marshal error: %v", err)
	}

	var decoded Logs
	err = decoded.Unmarshal(data)
	if err != nil {
		t.Fatalf("Logs.Unmarshal error: %v", err)
	}

	if len(decoded) != 0 {
		t.Errorf("Logs.Unmarshal of empty logs should be empty, got %d", len(decoded))
	}
}

func TestLogsUnmarshalInvalidData(t *testing.T) {
	var logs Logs
	err := logs.Unmarshal([]byte{0x00, 0x01, 0x02})
	if err == nil {
		t.Error("Logs.Unmarshal should fail with invalid data")
	}
}
