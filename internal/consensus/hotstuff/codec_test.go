// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// ============================================================================
// QC roundtrip
// ============================================================================

func TestEncodeDecodeQC(t *testing.T) {
	qc := QuorumCertificate{
		View:               42,
		BlockHash:          types.Hash{0xAA, 0xBB, 0xCC, 0xDD},
		AggregateSignature: bytes.Repeat([]byte{0x11}, 96),
		Signers:            []bool{true, false, true, true, false, false, true},
	}

	data, err := encodeQC(&qc)
	if err != nil {
		t.Fatalf("encodeQC failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("encoded QC should not be empty")
	}

	decoded, err := decodeQC(data)
	if err != nil {
		t.Fatalf("decodeQC failed: %v", err)
	}

	if decoded.View != qc.View {
		t.Fatalf("view mismatch: got %d, want %d", decoded.View, qc.View)
	}
	if decoded.BlockHash != qc.BlockHash {
		t.Fatalf("block hash mismatch: got %x, want %x", decoded.BlockHash, qc.BlockHash)
	}
	if !bytes.Equal(decoded.AggregateSignature, qc.AggregateSignature) {
		t.Fatal("aggregate signature mismatch")
	}
	if len(decoded.Signers) != len(qc.Signers) {
		t.Fatalf("signers length mismatch: got %d, want %d", len(decoded.Signers), len(qc.Signers))
	}
	for i, s := range qc.Signers {
		if decoded.Signers[i] != s {
			t.Fatalf("signer %d mismatch: got %v, want %v", i, decoded.Signers[i], s)
		}
	}
}

func TestEncodeDecodeQC_GenesisQC(t *testing.T) {
	qc := GenesisQC()

	data, err := encodeQC(&qc)
	if err != nil {
		t.Fatalf("encodeQC genesis failed: %v", err)
	}

	decoded, err := decodeQC(data)
	if err != nil {
		t.Fatalf("decodeQC genesis failed: %v", err)
	}

	if decoded.View != 0 {
		t.Fatalf("genesis QC view should be 0, got %d", decoded.View)
	}
	if decoded.BlockHash != (types.Hash{}) {
		t.Fatal("genesis QC block hash should be zero")
	}
}

// ============================================================================
// TC roundtrip
// ============================================================================

func TestEncodeDecodeTC(t *testing.T) {
	tc := TimeoutCertificate{
		View:               10,
		AggregateSignature: bytes.Repeat([]byte{0x22}, 96),
		Signers:            []bool{true, true, false, true},
		HighQC: QuorumCertificate{
			View:               8,
			BlockHash:          types.Hash{0xDE, 0xAD},
			AggregateSignature: bytes.Repeat([]byte{0x33}, 48),
			Signers:            []bool{true, false, true, true},
		},
	}

	data, err := encodeTC(&tc)
	if err != nil {
		t.Fatalf("encodeTC failed: %v", err)
	}

	decoded, err := decodeTC(data)
	if err != nil {
		t.Fatalf("decodeTC failed: %v", err)
	}

	if decoded.View != tc.View {
		t.Fatalf("TC view mismatch: got %d, want %d", decoded.View, tc.View)
	}
	if !bytes.Equal(decoded.AggregateSignature, tc.AggregateSignature) {
		t.Fatal("TC aggregate signature mismatch")
	}
	if decoded.HighQC.View != tc.HighQC.View {
		t.Fatalf("TC HighQC view mismatch: got %d, want %d", decoded.HighQC.View, tc.HighQC.View)
	}
	if decoded.HighQC.BlockHash != tc.HighQC.BlockHash {
		t.Fatal("TC HighQC block hash mismatch")
	}
	if !bytes.Equal(decoded.HighQC.AggregateSignature, tc.HighQC.AggregateSignature) {
		t.Fatal("TC HighQC aggregate signature mismatch")
	}
	if len(decoded.Signers) != len(tc.Signers) {
		t.Fatalf("TC signers length mismatch: got %d, want %d", len(decoded.Signers), len(tc.Signers))
	}
	for i, s := range tc.Signers {
		if decoded.Signers[i] != s {
			t.Fatalf("TC signer %d mismatch", i)
		}
	}
}

func TestEncodeDecodeTC_WithGenesisHighQC(t *testing.T) {
	tc := TimeoutCertificate{
		View:               1,
		AggregateSignature: bytes.Repeat([]byte{0x44}, 48),
		Signers:            []bool{true, true, true},
		HighQC:             GenesisQC(),
	}

	data, err := encodeTC(&tc)
	if err != nil {
		t.Fatalf("encodeTC with genesis HighQC failed: %v", err)
	}

	decoded, err := decodeTC(data)
	if err != nil {
		t.Fatalf("decodeTC with genesis HighQC failed: %v", err)
	}

	if decoded.HighQC.View != 0 {
		t.Fatalf("expected genesis HighQC view 0, got %d", decoded.HighQC.View)
	}
}

// ============================================================================
// ConsensusMsg roundtrip for all 7 message types
// ============================================================================

func makeTestQC() QuorumCertificate {
	return QuorumCertificate{
		View:               5,
		BlockHash:          types.Hash{0x01, 0x02, 0x03},
		AggregateSignature: bytes.Repeat([]byte{0xAA}, 96),
		Signers:            []bool{true, false, true, true},
	}
}

func makeTestTC() TimeoutCertificate {
	return TimeoutCertificate{
		View:               7,
		AggregateSignature: bytes.Repeat([]byte{0xBB}, 96),
		Signers:            []bool{true, true, false, true},
		HighQC:             makeTestQC(),
	}
}

func TestEncodeDecodeConsensusMsg_Proposal(t *testing.T) {
	justifyQC := makeTestQC()
	prepareQC := makeTestQC()
	prepareQC.View = 4

	original := &ConsensusMsg{
		Type: MsgProposal,
		Payload: &Proposal{
			View:      10,
			BlockHash: types.Hash{0xCA, 0xFE},
			JustifyQC: justifyQC,
			Proposer:  2,
			Signature: bytes.Repeat([]byte{0x55}, 96),
			PrepareQC: &prepareQC,
		},
	}

	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode Proposal failed: %v", err)
	}

	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode Proposal failed: %v", err)
	}

	if decoded.Type != MsgProposal {
		t.Fatalf("type mismatch: got %d, want %d", decoded.Type, MsgProposal)
	}
	p := decoded.Payload.(*Proposal)
	if p.View != 10 {
		t.Fatalf("proposal view mismatch: got %d", p.View)
	}
	if p.BlockHash != (types.Hash{0xCA, 0xFE}) {
		t.Fatal("proposal block hash mismatch")
	}
	if p.Proposer != 2 {
		t.Fatalf("proposer mismatch: got %d", p.Proposer)
	}
	if !bytes.Equal(p.Signature, bytes.Repeat([]byte{0x55}, 96)) {
		t.Fatal("proposal signature mismatch")
	}
	if p.JustifyQC.View != justifyQC.View {
		t.Fatal("proposal justify QC view mismatch")
	}
	if p.PrepareQC == nil {
		t.Fatal("proposal PrepareQC should not be nil")
	}
	if p.PrepareQC.View != 4 {
		t.Fatalf("proposal PrepareQC view mismatch: got %d", p.PrepareQC.View)
	}
}

func TestEncodeDecodeConsensusMsg_Proposal_NilPrepareQC(t *testing.T) {
	original := &ConsensusMsg{
		Type: MsgProposal,
		Payload: &Proposal{
			View:      1,
			BlockHash: types.Hash{0x01},
			JustifyQC: GenesisQC(),
			Proposer:  0,
			Signature: bytes.Repeat([]byte{0x66}, 48),
			PrepareQC: nil,
		},
	}

	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode Proposal (nil PrepareQC) failed: %v", err)
	}

	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode Proposal (nil PrepareQC) failed: %v", err)
	}

	p := decoded.Payload.(*Proposal)
	if p.PrepareQC != nil {
		t.Fatal("PrepareQC should be nil after roundtrip")
	}
}

func TestEncodeDecodeConsensusMsg_Vote(t *testing.T) {
	original := &ConsensusMsg{
		Type: MsgVote,
		Payload: &Vote{
			View:      3,
			BlockHash: types.Hash{0xBE, 0xEF},
			Voter:     1,
			Signature: bytes.Repeat([]byte{0x77}, 96),
		},
	}

	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode Vote failed: %v", err)
	}

	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode Vote failed: %v", err)
	}

	if decoded.Type != MsgVote {
		t.Fatalf("type mismatch: got %d", decoded.Type)
	}
	v := decoded.Payload.(*Vote)
	if v.View != 3 || v.Voter != 1 || v.BlockHash != (types.Hash{0xBE, 0xEF}) {
		t.Fatal("vote field mismatch")
	}
	if !bytes.Equal(v.Signature, bytes.Repeat([]byte{0x77}, 96)) {
		t.Fatal("vote signature mismatch")
	}
}

func TestEncodeDecodeConsensusMsg_CommitVote(t *testing.T) {
	original := &ConsensusMsg{
		Type: MsgCommitVote,
		Payload: &CommitVote{
			View:      4,
			BlockHash: types.Hash{0xAB, 0xCD},
			Voter:     3,
			Signature: bytes.Repeat([]byte{0x88}, 96),
		},
	}

	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode CommitVote failed: %v", err)
	}

	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode CommitVote failed: %v", err)
	}

	if decoded.Type != MsgCommitVote {
		t.Fatalf("type mismatch: got %d", decoded.Type)
	}
	cv := decoded.Payload.(*CommitVote)
	if cv.View != 4 || cv.Voter != 3 || cv.BlockHash != (types.Hash{0xAB, 0xCD}) {
		t.Fatal("commit vote field mismatch")
	}
	if !bytes.Equal(cv.Signature, bytes.Repeat([]byte{0x88}, 96)) {
		t.Fatal("commit vote signature mismatch")
	}
}

func TestEncodeDecodeConsensusMsg_PrepareQCMsg(t *testing.T) {
	qc := makeTestQC()
	original := &ConsensusMsg{
		Type: MsgPrepareQC,
		Payload: &PrepareQCMsg{
			View:      5,
			BlockHash: types.Hash{0x01, 0x02, 0x03},
			QC:        qc,
		},
	}

	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode PrepareQCMsg failed: %v", err)
	}

	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode PrepareQCMsg failed: %v", err)
	}

	if decoded.Type != MsgPrepareQC {
		t.Fatalf("type mismatch: got %d", decoded.Type)
	}
	pqc := decoded.Payload.(*PrepareQCMsg)
	if pqc.View != 5 {
		t.Fatalf("PrepareQCMsg view mismatch: got %d", pqc.View)
	}
	if pqc.BlockHash != (types.Hash{0x01, 0x02, 0x03}) {
		t.Fatal("PrepareQCMsg block hash mismatch")
	}
	if pqc.QC.View != qc.View {
		t.Fatal("PrepareQCMsg inner QC view mismatch")
	}
}

func TestEncodeDecodeConsensusMsg_Timeout(t *testing.T) {
	highQC := makeTestQC()
	original := &ConsensusMsg{
		Type: MsgTimeout,
		Payload: &TimeoutMessage{
			View:      6,
			HighQC:    highQC,
			Sender:    2,
			Signature: bytes.Repeat([]byte{0x99}, 96),
		},
	}

	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode TimeoutMessage failed: %v", err)
	}

	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode TimeoutMessage failed: %v", err)
	}

	if decoded.Type != MsgTimeout {
		t.Fatalf("type mismatch: got %d", decoded.Type)
	}
	tm := decoded.Payload.(*TimeoutMessage)
	if tm.View != 6 || tm.Sender != 2 {
		t.Fatal("timeout message field mismatch")
	}
	if tm.HighQC.View != highQC.View {
		t.Fatal("timeout message HighQC view mismatch")
	}
	if !bytes.Equal(tm.Signature, bytes.Repeat([]byte{0x99}, 96)) {
		t.Fatal("timeout message signature mismatch")
	}
}

func TestEncodeDecodeConsensusMsg_NewView(t *testing.T) {
	tc := makeTestTC()
	original := &ConsensusMsg{
		Type: MsgNewView,
		Payload: &NewViewMsg{
			View:        8,
			TimeoutCert: tc,
			Leader:      1,
			Signature:   bytes.Repeat([]byte{0xCC}, 96),
		},
	}

	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode NewViewMsg failed: %v", err)
	}

	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode NewViewMsg failed: %v", err)
	}

	if decoded.Type != MsgNewView {
		t.Fatalf("type mismatch: got %d", decoded.Type)
	}
	nv := decoded.Payload.(*NewViewMsg)
	if nv.View != 8 || nv.Leader != 1 {
		t.Fatal("new view message field mismatch")
	}
	if nv.TimeoutCert.View != tc.View {
		t.Fatal("new view TC view mismatch")
	}
	if nv.TimeoutCert.HighQC.View != tc.HighQC.View {
		t.Fatal("new view TC HighQC view mismatch")
	}
	if !bytes.Equal(nv.Signature, bytes.Repeat([]byte{0xCC}, 96)) {
		t.Fatal("new view signature mismatch")
	}
}

func TestEncodeDecodeConsensusMsg_Decide(t *testing.T) {
	commitQC := makeTestQC()
	commitQC.View = 9
	original := &ConsensusMsg{
		Type: MsgDecide,
		Payload: &Decide{
			View:      9,
			BlockHash: types.Hash{0xFF, 0xEE},
			CommitQC:  commitQC,
		},
	}

	data, err := EncodeConsensusMsg(original)
	if err != nil {
		t.Fatalf("encode Decide failed: %v", err)
	}

	decoded, err := DecodeConsensusMsg(data)
	if err != nil {
		t.Fatalf("decode Decide failed: %v", err)
	}

	if decoded.Type != MsgDecide {
		t.Fatalf("type mismatch: got %d", decoded.Type)
	}
	d := decoded.Payload.(*Decide)
	if d.View != 9 {
		t.Fatalf("decide view mismatch: got %d", d.View)
	}
	if d.BlockHash != (types.Hash{0xFF, 0xEE}) {
		t.Fatal("decide block hash mismatch")
	}
	if d.CommitQC.View != 9 {
		t.Fatal("decide commit QC view mismatch")
	}
}

// ============================================================================
// Bitmap packing/unpacking
// ============================================================================

func TestPackUnpackBoolBitmap(t *testing.T) {
	tests := []struct {
		name  string
		input []bool
	}{
		{"empty", nil},
		{"single_true", []bool{true}},
		{"single_false", []bool{false}},
		{"four_mixed", []bool{true, false, true, true}},
		{"eight_all_true", []bool{true, true, true, true, true, true, true, true}},
		{"nine_crossing_byte_boundary", []bool{true, false, true, false, true, false, true, false, true}},
		{"sixteen_alternating", func() []bool {
			b := make([]bool, 16)
			for i := range b {
				b[i] = i%2 == 0
			}
			return b
		}()},
		{"hundred_validators", func() []bool {
			b := make([]bool, 100)
			for i := range b {
				b[i] = i%3 == 0
			}
			return b
		}()},
		{"all_false", make([]bool, 10)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			packed := packBoolBitmap(tc.input)
			unpacked := unpackBoolBitmap(packed)

			if tc.input == nil || len(tc.input) == 0 {
				if unpacked != nil {
					t.Fatalf("expected nil for empty input, got %v", unpacked)
				}
				return
			}

			if len(unpacked) != len(tc.input) {
				t.Fatalf("length mismatch: got %d, want %d", len(unpacked), len(tc.input))
			}
			for i, expected := range tc.input {
				if unpacked[i] != expected {
					t.Fatalf("bit %d mismatch: got %v, want %v", i, unpacked[i], expected)
				}
			}
		})
	}
}

func TestPackBoolBitmap_SizeEfficiency(t *testing.T) {
	// 100 bools should pack into 2 (header) + 13 (ceil(100/8)) = 15 bytes
	bools := make([]bool, 100)
	for i := range bools {
		bools[i] = true
	}
	packed := packBoolBitmap(bools)
	expectedSize := 2 + (100+7)/8 // 2 + 13 = 15
	if len(packed) != expectedSize {
		t.Fatalf("expected packed size %d, got %d", expectedSize, len(packed))
	}
}

func TestUnpackBoolBitmap_TooShort(t *testing.T) {
	// Less than 2 bytes should return nil
	if result := unpackBoolBitmap(nil); result != nil {
		t.Fatal("nil data should return nil")
	}
	if result := unpackBoolBitmap([]byte{1}); result != nil {
		t.Fatal("1-byte data should return nil")
	}
}

func TestUnpackBoolBitmap_ZeroCount(t *testing.T) {
	// Header says 0 bools
	if result := unpackBoolBitmap([]byte{0, 0}); result != nil {
		t.Fatal("zero-count should return nil")
	}
}

// ============================================================================
// Error cases
// ============================================================================

func TestDecodeInvalidData(t *testing.T) {
	// Completely invalid data should fail to decode
	_, err := DecodeConsensusMsg(nil)
	if err == nil {
		t.Fatal("nil data should fail")
	}

	_, err = DecodeConsensusMsg([]byte{})
	if err == nil {
		t.Fatal("empty data should fail")
	}

	_, err = DecodeConsensusMsg([]byte{0xFF, 0xFE, 0xFD})
	if err == nil {
		t.Fatal("garbage data should fail")
	}

	// Invalid QC data
	_, err = decodeQC(nil)
	if err == nil {
		t.Fatal("nil QC data should fail")
	}

	_, err = decodeQC([]byte{0x01, 0x02})
	if err == nil {
		t.Fatal("truncated QC data should fail")
	}

	// Invalid TC data
	_, err = decodeTC(nil)
	if err == nil {
		t.Fatal("nil TC data should fail")
	}

	_, err = decodeTC([]byte{0x01, 0x02, 0x03})
	if err == nil {
		t.Fatal("truncated TC data should fail")
	}
}

func TestEncodeConsensusMsg_UnknownType(t *testing.T) {
	msg := &ConsensusMsg{
		Type:    ConsensusMsgType(255),
		Payload: nil,
	}
	_, err := EncodeConsensusMsg(msg)
	if err == nil {
		t.Fatal("unknown message type should fail")
	}
}
