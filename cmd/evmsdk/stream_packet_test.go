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

package evmsdk

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

func makeTestHeader(t *testing.T, number uint64) []byte {
	t.Helper()
	hdr := &block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(1),
		GasLimit:   8000000,
		BaseFee:    uint256.NewInt(1000000000),
	}
	data, err := hdr.Marshal()
	if err != nil {
		t.Fatalf("Header.Marshal() error = %v", err)
	}
	return data
}

func TestStreamPacketRoundtrip(t *testing.T) {
	blockHash := types.BytesToHash([]byte("test-block-hash-0000000000000"))
	headerData := makeTestHeader(t, 42)

	tx1 := []byte{0x01, 0x02, 0x03}
	tx2 := []byte{0x04, 0x05, 0x06, 0x07}
	readLog := []byte{0xAA, 0xBB, 0xCC}

	codeHash1 := types.BytesToHash([]byte("code-hash-1-padding-to-32-bytes"))
	code1 := []byte{0x60, 0x00, 0x60, 0x00, 0xFD} // PUSH1 0 PUSH1 0 REVERT

	codeHash2 := types.BytesToHash([]byte("code-hash-2-padding-to-32-bytes"))
	code2 := []byte{0x60, 0x01, 0x60, 0x00, 0x53} // PUSH1 1 PUSH1 0 MSTORE8

	original := &StreamPacket{
		BlockHash:    blockHash,
		HeaderRLP:    headerData,
		Transactions: [][]byte{tx1, tx2},
		ReadLogData:  readLog,
		Bytecodes: []CodeEntry{
			{Hash: codeHash1, Code: code1},
			{Hash: codeHash2, Code: code2},
		},
	}

	encoded, err := EncodeStreamPacket(original)
	if err != nil {
		t.Fatalf("EncodeStreamPacket() error = %v", err)
	}

	// Verify V2 wire format magic.
	if !IsV2WireFormat(encoded) {
		t.Fatal("encoded data is not V2 wire format")
	}

	decoded, err := DecodeStreamPacket(encoded)
	if err != nil {
		t.Fatalf("DecodeStreamPacket() error = %v", err)
	}

	// Verify all fields.
	if decoded.BlockHash != original.BlockHash {
		t.Errorf("BlockHash = %x, want %x", decoded.BlockHash, original.BlockHash)
	}
	if !bytes.Equal(decoded.HeaderRLP, original.HeaderRLP) {
		t.Error("HeaderRLP mismatch")
	}
	if len(decoded.Transactions) != len(original.Transactions) {
		t.Fatalf("Transactions len = %d, want %d", len(decoded.Transactions), len(original.Transactions))
	}
	for i := range original.Transactions {
		if !bytes.Equal(decoded.Transactions[i], original.Transactions[i]) {
			t.Errorf("Transactions[%d] mismatch", i)
		}
	}
	if !bytes.Equal(decoded.ReadLogData, original.ReadLogData) {
		t.Error("ReadLogData mismatch")
	}
	if len(decoded.Bytecodes) != len(original.Bytecodes) {
		t.Fatalf("Bytecodes len = %d, want %d", len(decoded.Bytecodes), len(original.Bytecodes))
	}
	for i := range original.Bytecodes {
		if decoded.Bytecodes[i].Hash != original.Bytecodes[i].Hash {
			t.Errorf("Bytecodes[%d].Hash mismatch", i)
		}
		if !bytes.Equal(decoded.Bytecodes[i].Code, original.Bytecodes[i].Code) {
			t.Errorf("Bytecodes[%d].Code mismatch", i)
		}
	}

	// Verify HeaderInfo extracts block number.
	blockNum, err := decoded.HeaderInfo()
	if err != nil {
		t.Fatalf("HeaderInfo() error = %v", err)
	}
	if blockNum != 42 {
		t.Errorf("HeaderInfo() blockNumber = %d, want 42", blockNum)
	}
}

func TestStreamPacketEmpty(t *testing.T) {
	original := &StreamPacket{
		HeaderRLP:    []byte{},
		Transactions: nil,
		ReadLogData:  nil,
		Bytecodes:    nil,
	}

	encoded, err := EncodeStreamPacket(original)
	if err != nil {
		t.Fatalf("EncodeStreamPacket() error = %v", err)
	}

	decoded, err := DecodeStreamPacket(encoded)
	if err != nil {
		t.Fatalf("DecodeStreamPacket() error = %v", err)
	}

	if decoded.BlockHash != (types.Hash{}) {
		t.Errorf("BlockHash = %x, want zero hash", decoded.BlockHash)
	}
	if len(decoded.Transactions) != 0 {
		t.Errorf("Transactions len = %d, want 0", len(decoded.Transactions))
	}
	if len(decoded.ReadLogData) != 0 {
		t.Errorf("ReadLogData len = %d, want 0", len(decoded.ReadLogData))
	}
	if len(decoded.Bytecodes) != 0 {
		t.Errorf("Bytecodes len = %d, want 0", len(decoded.Bytecodes))
	}
}

func TestStreamPacketEstimatedSize(t *testing.T) {
	pkt := &StreamPacket{
		HeaderRLP:    make([]byte, 100),
		Transactions: [][]byte{make([]byte, 50), make([]byte, 75)},
		ReadLogData:  make([]byte, 200),
		Bytecodes: []CodeEntry{
			{Code: make([]byte, 300)},
		},
	}

	estimated := pkt.EstimatedSize()
	encoded, err := EncodeStreamPacket(pkt)
	if err != nil {
		t.Fatalf("EncodeStreamPacket() error = %v", err)
	}

	if estimated != len(encoded) {
		t.Errorf("EstimatedSize() = %d, actual encoded len = %d", estimated, len(encoded))
	}
}

func TestStreamPacketV1V2Detection(t *testing.T) {
	// V2 packet.
	v2Pkt := &StreamPacket{
		HeaderRLP: makeTestHeader(t, 1),
	}
	v2Data, err := EncodeStreamPacket(v2Pkt)
	if err != nil {
		t.Fatalf("EncodeStreamPacket() error = %v", err)
	}

	if !IsV2WireFormat(v2Data) {
		t.Error("V2 packet not detected as V2")
	}

	// V1 data (JSON).
	v1Data := []byte(`{"entire":{"header":{}}}`)
	if IsV2WireFormat(v1Data) {
		t.Error("V1 JSON data incorrectly detected as V2")
	}

	// Random binary that doesn't start with N2.
	randomData := []byte{0xFF, 0xFE, 0x01, 0x02}
	if IsV2WireFormat(randomData) {
		t.Error("random data incorrectly detected as V2")
	}
}

func TestStreamPacketNilReturnsError(t *testing.T) {
	_, err := EncodeStreamPacket(nil)
	if err == nil {
		t.Error("EncodeStreamPacket(nil) should return error")
	}
}

func TestStreamPacketDecodeTruncated(t *testing.T) {
	// Valid header but no payload.
	headerOnly := EncodeWireHeader(WireVersion1, 0x00)
	_, err := DecodeStreamPacket(headerOnly)
	if err == nil {
		t.Error("DecodeStreamPacket() should fail on truncated data")
	}
}

func TestStreamPacketHeaderInfoEmptyHeader(t *testing.T) {
	pkt := &StreamPacket{
		HeaderRLP: []byte{},
	}
	_, err := pkt.HeaderInfo()
	if err == nil {
		t.Error("HeaderInfo() should fail on empty header")
	}
}
