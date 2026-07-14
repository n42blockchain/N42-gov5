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
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/proto/types_pb"
)

func balSampleHeader() *Header {
	wh := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	pbr := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	rh := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	bgu := uint64(0)
	ebg := uint64(0)
	return &Header{
		ParentHash:       types.HexToHash("0x22"),
		UncleHash:        types.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:         types.HexToAddress("0xf7dc5c92fa9e812eb0c3157492da65457ae5de46"),
		Root:             types.HexToHash("0x33"),
		TxHash:           types.HexToHash("0x44"),
		ReceiptHash:      types.HexToHash("0x55"),
		Difficulty:       uint256.NewInt(0),
		Number:           uint256.NewInt(1001),
		GasLimit:         30000000,
		GasUsed:          21000,
		Time:             1700000000,
		Extra:            bytes.Repeat([]byte{0xab}, 200),
		MixDigest:        types.HexToHash("0x66"),
		BaseFee:          uint256.NewInt(1000000000),
		WithdrawalsHash:  &wh,
		BlobGasUsed:      &bgu,
		ExcessBlobGas:    &ebg,
		ParentBeaconRoot: &pbr,
		RequestsHash:     &rh,
	}
}

// TestHeaderBALHashParticipatesInHash checks the BAL fork's pre/post-fork
// invariant: a nil BlockAccessListHash leaves the header hash unchanged (byte
// compatibility for un-activated chains), and setting it changes the hash (so it
// is actually bound into consensus).
func TestHeaderBALHashParticipatesInHash(t *testing.T) {
	preFork := balSampleHeader()
	preForkHash := preFork.Hash()

	// Same header, BAL not set → identical hash (the trailing optional is omitted).
	control := balSampleHeader()
	if control.Hash() != preForkHash {
		t.Fatalf("nil BAL hash not stable: %s vs %s", control.Hash().Hex(), preForkHash.Hex())
	}

	// Now bind a BAL hash → the header hash must change.
	withBAL := balSampleHeader()
	blah := types.HexToHash("0x99aabbccddeeff00112233445566778899aabbccddeeff0011223344556677ff")
	withBAL.BlockAccessListHash = &blah
	if withBAL.Hash() == preForkHash {
		t.Fatal("binding BlockAccessListHash did not change the header hash")
	}
}

// TestHeaderBALMarshalTrailerRoundTrip checks the rawdb single-header path
// (header.Marshal / Unmarshal, which carries proto-external fields in the
// trailer) preserves BlockAccessListHash and the resulting hash.
func TestHeaderBALMarshalTrailerRoundTrip(t *testing.T) {
	h := balSampleHeader()
	blah := types.HexToHash("0x99aabbccddeeff00112233445566778899aabbccddeeff0011223344556677ff")
	h.BlockAccessListHash = &blah
	want := h.Hash()

	enc, err := h.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var h2 Header
	if err := h2.Unmarshal(enc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if h2.BlockAccessListHash == nil || *h2.BlockAccessListHash != blah {
		t.Fatalf("BlockAccessListHash not preserved through Marshal trailer: %v", h2.BlockAccessListHash)
	}
	if h2.Hash() != want {
		t.Fatalf("hash changed through Marshal round trip: want %s got %s", want.Hex(), h2.Hash().Hex())
	}
	if IsLegacyHeader(&h2) {
		t.Fatal("header with BAL hash must not be classified as legacy")
	}
}

// TestBlockRLPRoundTripCarriesBALHash pins the activation-critical guarantee: the
// consensus block wire form (Block.EncodeRLP — used by gossip broadcast and sync
// chunked responses) round-trips BlockAccessListHash and reproduces the identical
// header hash. protoc is NOT needed; the RLP path already carries the field.
func TestBlockRLPRoundTripCarriesBALHash(t *testing.T) {
	h := balSampleHeader()
	blah := types.HexToHash("0x99aabbccddeeff00112233445566778899aabbccddeeff0011223344556677ff")
	h.BlockAccessListHash = &blah
	want := h.Hash()
	blk := &Block{header: h, body: &Body{}}

	var buf bytes.Buffer
	if err := blk.EncodeRLP(&buf); err != nil {
		t.Fatalf("EncodeRLP: %v", err)
	}
	var got Block
	if err := rlp.DecodeBytes(buf.Bytes(), &got); err != nil {
		t.Fatalf("DecodeRLP: %v", err)
	}
	if got.header.BlockAccessListHash == nil || *got.header.BlockAccessListHash != blah {
		t.Fatalf("BlockAccessListHash lost over Block RLP: %v", got.header.BlockAccessListHash)
	}
	if got.header.Hash() != want {
		t.Fatalf("header hash changed over Block RLP: want %s got %s", want.Hex(), got.header.Hash().Hex())
	}
}

// TestHeaderBALProtoPathDropsHash documents the ONE remaining proto path that
// still discards BlockAccessListHash: the legacy types_pb.Header form produced by
// ToProtoMessage, which the direct-push (blockchain import) and download paths
// still use. Those must be moved to RLP (like gossip/sync/torrentsync already
// were) before the BAL fork can safely activate — otherwise a follower importing
// via the proto path would reconstruct a different header hash. protoc/proto
// regeneration is explicitly NOT the fix; de-proto-ing those paths is.
func TestHeaderBALProtoPathDropsHash(t *testing.T) {
	h := balSampleHeader()
	blah := types.HexToHash("0x99aabbccddeeff00112233445566778899aabbccddeeff0011223344556677ff")
	h.BlockAccessListHash = &blah

	data, err := proto.Marshal(h.ToProtoMessage())
	if err != nil {
		t.Fatalf("proto Marshal: %v", err)
	}
	var pb types_pb.Header
	if err := proto.Unmarshal(data, &pb); err != nil {
		t.Fatalf("proto Unmarshal: %v", err)
	}
	var h2 Header
	if err := h2.FromProtoMessage(&pb); err != nil {
		t.Fatalf("FromProtoMessage: %v", err)
	}
	if h2.BlockAccessListHash != nil {
		t.Fatal("proto path now carries BlockAccessListHash — the direct-push/download " +
			"paths can rely on it; lift the corresponding BALTime activation guard note")
	}
}
