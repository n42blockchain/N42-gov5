// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Wire format codec for HotStuff ConsensusMsg messages.
// EncodeConsensusMsg serialises a ConsensusMsg to bytes using the
// proto/sync_pb payload shapes. Switches on msg.Type to pick the
// correct subtype encoding (proposal, vote, timeout, view-change, etc.)
// and returns the framed byte slice ready for libp2p dispatch.

package hotstuff

import (
	"fmt"

	"github.com/n42blockchain/N42/proto/sync_pb"
	"github.com/n42blockchain/N42/common/types"
)

// EncodeConsensusMsg serializes a ConsensusMsg to wire format bytes.
func EncodeConsensusMsg(msg *ConsensusMsg) ([]byte, error) {
	var payload []byte
	var err error

	switch msg.Type {
	case MsgProposal:
		payload, err = encodeProposal(msg.Payload.(*Proposal))
	case MsgVote:
		payload, err = encodeVote(msg.Payload.(*Vote))
	case MsgCommitVote:
		payload, err = encodeCommitVote(msg.Payload.(*CommitVote))
	case MsgPrepareQC:
		payload, err = encodePrepareQCMsg(msg.Payload.(*PrepareQCMsg))
	case MsgTimeout:
		payload, err = encodeTimeoutMsg(msg.Payload.(*TimeoutMessage))
	case MsgNewView:
		payload, err = encodeNewViewMsg(msg.Payload.(*NewViewMsg))
	case MsgDecide:
		payload, err = encodeDecide(msg.Payload.(*Decide))
	default:
		return nil, fmt.Errorf("unknown message type: %d", msg.Type)
	}
	if err != nil {
		return nil, err
	}

	envelope := &sync_pb.HotStuffConsensusMsg{
		Type:    uint8(msg.Type),
		Payload: payload,
	}
	return envelope.MarshalSSZ()
}

// DecodeConsensusMsg deserializes wire format bytes into a ConsensusMsg.
func DecodeConsensusMsg(data []byte) (*ConsensusMsg, error) {
	envelope := &sync_pb.HotStuffConsensusMsg{}
	if err := envelope.UnmarshalSSZ(data); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}

	msgType := ConsensusMsgType(envelope.Type)
	var payload interface{}
	var err error

	switch msgType {
	case MsgProposal:
		payload, err = decodeProposal(envelope.Payload)
	case MsgVote:
		payload, err = decodeVote(envelope.Payload)
	case MsgCommitVote:
		payload, err = decodeCommitVote(envelope.Payload)
	case MsgPrepareQC:
		payload, err = decodePrepareQCMsg(envelope.Payload)
	case MsgTimeout:
		payload, err = decodeTimeoutMsg(envelope.Payload)
	case MsgNewView:
		payload, err = decodeNewViewMsg(envelope.Payload)
	case MsgDecide:
		payload, err = decodeDecide(envelope.Payload)
	default:
		return nil, fmt.Errorf("unknown message type: %d", envelope.Type)
	}
	if err != nil {
		return nil, err
	}

	return &ConsensusMsg{Type: msgType, Payload: payload}, nil
}

// ============================================================================
// QC / TC codec
// ============================================================================

func encodeQC(qc *QuorumCertificate) ([]byte, error) {
	wire := &sync_pb.HotStuffQC{
		View:               qc.View,
		BlockHash:          qc.BlockHash[:],
		AggregateSignature: qc.AggregateSignature,
		SignersBitmap:      packBoolBitmap(qc.Signers),
	}
	return wire.MarshalSSZ()
}

// DecodeQC decodes a QuorumCertificate from SSZ-encoded bytes.
// Exported for use by the cross-chain bridge (QC extraction from block headers).
func DecodeQC(data []byte) (*QuorumCertificate, error) { return decodeQC(data) }

func decodeQC(data []byte) (*QuorumCertificate, error) {
	wire := &sync_pb.HotStuffQC{}
	if err := wire.UnmarshalSSZ(data); err != nil {
		return nil, err
	}
	var hash types.Hash
	copy(hash[:], wire.BlockHash)
	return &QuorumCertificate{
		View:               wire.View,
		BlockHash:          hash,
		AggregateSignature: wire.AggregateSignature,
		Signers:            unpackBoolBitmap(wire.SignersBitmap),
	}, nil
}

func encodeTC(tc *TimeoutCertificate) ([]byte, error) {
	highQCBytes, err := encodeQC(&tc.HighQC)
	if err != nil {
		return nil, err
	}
	wire := &sync_pb.HotStuffTC{
		View:               tc.View,
		AggregateSignature: tc.AggregateSignature,
		SignersBitmap:      packBoolBitmap(tc.Signers),
		HighQC:             highQCBytes,
	}
	return wire.MarshalSSZ()
}

func decodeTC(data []byte) (*TimeoutCertificate, error) {
	wire := &sync_pb.HotStuffTC{}
	if err := wire.UnmarshalSSZ(data); err != nil {
		return nil, err
	}
	highQC, err := decodeQC(wire.HighQC)
	if err != nil {
		return nil, fmt.Errorf("decode TC high_qc: %w", err)
	}
	return &TimeoutCertificate{
		View:               wire.View,
		AggregateSignature: wire.AggregateSignature,
		Signers:            unpackBoolBitmap(wire.SignersBitmap),
		HighQC:             *highQC,
	}, nil
}

// ============================================================================
// Message-specific codecs
// ============================================================================

func encodeProposal(p *Proposal) ([]byte, error) {
	justifyBytes, err := encodeQC(&p.JustifyQC)
	if err != nil {
		return nil, err
	}
	var prepareQCBytes []byte
	if p.PrepareQC != nil {
		prepareQCBytes, err = encodeQC(p.PrepareQC)
		if err != nil {
			return nil, err
		}
	}
	wire := &sync_pb.HotStuffProposal{
		View:       p.View,
		BlockHash:  p.BlockHash[:],
		JustifyQC:  justifyBytes,
		Proposer:   p.Proposer,
		Signature:  p.Signature,
		PrepareQC:  prepareQCBytes,
		TxRootHash: p.TxRootHash[:],
	}
	return wire.MarshalSSZ()
}

func decodeProposal(data []byte) (*Proposal, error) {
	wire := &sync_pb.HotStuffProposal{}
	if err := wire.UnmarshalSSZ(data); err != nil {
		return nil, err
	}
	justifyQC, err := decodeQC(wire.JustifyQC)
	if err != nil {
		return nil, fmt.Errorf("decode proposal justify_qc: %w", err)
	}
	var hash types.Hash
	copy(hash[:], wire.BlockHash)
	var txRootHash types.Hash
	copy(txRootHash[:], wire.TxRootHash)
	p := &Proposal{
		View:       wire.View,
		BlockHash:  hash,
		JustifyQC:  *justifyQC,
		Proposer:   wire.Proposer,
		Signature:  wire.Signature,
		TxRootHash: txRootHash,
	}
	if len(wire.PrepareQC) > 0 {
		pqc, err := decodeQC(wire.PrepareQC)
		if err != nil {
			return nil, fmt.Errorf("decode proposal prepare_qc: %w", err)
		}
		p.PrepareQC = pqc
	}
	return p, nil
}

func encodeVote(v *Vote) ([]byte, error) {
	wire := &sync_pb.HotStuffVote{
		View:      v.View,
		BlockHash: v.BlockHash[:],
		Voter:     v.Voter,
		Signature: v.Signature,
	}
	return wire.MarshalSSZ()
}

func decodeVote(data []byte) (*Vote, error) {
	wire := &sync_pb.HotStuffVote{}
	if err := wire.UnmarshalSSZ(data); err != nil {
		return nil, err
	}
	var hash types.Hash
	copy(hash[:], wire.BlockHash)
	return &Vote{
		View:      wire.View,
		BlockHash: hash,
		Voter:     wire.Voter,
		Signature: wire.Signature,
	}, nil
}

func encodeCommitVote(cv *CommitVote) ([]byte, error) {
	wire := &sync_pb.HotStuffCommitVote{
		View:      cv.View,
		BlockHash: cv.BlockHash[:],
		Voter:     cv.Voter,
		Signature: cv.Signature,
	}
	return wire.MarshalSSZ()
}

func decodeCommitVote(data []byte) (*CommitVote, error) {
	wire := &sync_pb.HotStuffCommitVote{}
	if err := wire.UnmarshalSSZ(data); err != nil {
		return nil, err
	}
	var hash types.Hash
	copy(hash[:], wire.BlockHash)
	return &CommitVote{
		View:      wire.View,
		BlockHash: hash,
		Voter:     wire.Voter,
		Signature: wire.Signature,
	}, nil
}

func encodePrepareQCMsg(pqc *PrepareQCMsg) ([]byte, error) {
	qcBytes, err := encodeQC(&pqc.QC)
	if err != nil {
		return nil, err
	}
	wire := &sync_pb.HotStuffPrepareQCMsg{
		View:      pqc.View,
		BlockHash: pqc.BlockHash[:],
		QC:        qcBytes,
	}
	return wire.MarshalSSZ()
}

func decodePrepareQCMsg(data []byte) (*PrepareQCMsg, error) {
	wire := &sync_pb.HotStuffPrepareQCMsg{}
	if err := wire.UnmarshalSSZ(data); err != nil {
		return nil, err
	}
	qc, err := decodeQC(wire.QC)
	if err != nil {
		return nil, fmt.Errorf("decode prepare_qc: %w", err)
	}
	var hash types.Hash
	copy(hash[:], wire.BlockHash)
	return &PrepareQCMsg{
		View:      wire.View,
		BlockHash: hash,
		QC:        *qc,
	}, nil
}

func encodeTimeoutMsg(tm *TimeoutMessage) ([]byte, error) {
	highQCBytes, err := encodeQC(&tm.HighQC)
	if err != nil {
		return nil, err
	}
	wire := &sync_pb.HotStuffTimeoutMsg{
		View:      tm.View,
		HighQC:    highQCBytes,
		Sender:    tm.Sender,
		Signature: tm.Signature,
	}
	return wire.MarshalSSZ()
}

func decodeTimeoutMsg(data []byte) (*TimeoutMessage, error) {
	wire := &sync_pb.HotStuffTimeoutMsg{}
	if err := wire.UnmarshalSSZ(data); err != nil {
		return nil, err
	}
	highQC, err := decodeQC(wire.HighQC)
	if err != nil {
		return nil, fmt.Errorf("decode timeout high_qc: %w", err)
	}
	return &TimeoutMessage{
		View:      wire.View,
		HighQC:    *highQC,
		Sender:    wire.Sender,
		Signature: wire.Signature,
	}, nil
}

func encodeNewViewMsg(nv *NewViewMsg) ([]byte, error) {
	tcBytes, err := encodeTC(&nv.TimeoutCert)
	if err != nil {
		return nil, err
	}
	wire := &sync_pb.HotStuffNewViewMsg{
		View:        nv.View,
		TimeoutCert: tcBytes,
		Leader:      nv.Leader,
		Signature:   nv.Signature,
	}
	return wire.MarshalSSZ()
}

func decodeNewViewMsg(data []byte) (*NewViewMsg, error) {
	wire := &sync_pb.HotStuffNewViewMsg{}
	if err := wire.UnmarshalSSZ(data); err != nil {
		return nil, err
	}
	tc, err := decodeTC(wire.TimeoutCert)
	if err != nil {
		return nil, fmt.Errorf("decode new_view tc: %w", err)
	}
	return &NewViewMsg{
		View:        wire.View,
		TimeoutCert: *tc,
		Leader:      wire.Leader,
		Signature:   wire.Signature,
	}, nil
}

func encodeDecide(d *Decide) ([]byte, error) {
	qcBytes, err := encodeQC(&d.CommitQC)
	if err != nil {
		return nil, err
	}
	wire := &sync_pb.HotStuffDecide{
		View:      d.View,
		BlockHash: d.BlockHash[:],
		CommitQC:  qcBytes,
	}
	return wire.MarshalSSZ()
}

func decodeDecide(data []byte) (*Decide, error) {
	wire := &sync_pb.HotStuffDecide{}
	if err := wire.UnmarshalSSZ(data); err != nil {
		return nil, err
	}
	qc, err := decodeQC(wire.CommitQC)
	if err != nil {
		return nil, fmt.Errorf("decode decide commit_qc: %w", err)
	}
	var hash types.Hash
	copy(hash[:], wire.BlockHash)
	return &Decide{
		View:      wire.View,
		BlockHash: hash,
		CommitQC:  *qc,
	}, nil
}

// ============================================================================
// Bitmap helpers
// ============================================================================

// packBoolBitmap packs []bool into a compact byte bitmap.
func packBoolBitmap(bools []bool) []byte {
	if len(bools) == 0 {
		return nil
	}
	nBytes := (len(bools) + 7) / 8
	// First 2 bytes: count of bools (LE uint16)
	out := make([]byte, 2+nBytes)
	out[0] = byte(len(bools))
	out[1] = byte(len(bools) >> 8)
	for i, b := range bools {
		if b {
			out[2+i/8] |= 1 << (uint(i) % 8)
		}
	}
	return out
}

// MaxValidators is the maximum number of validators supported in a bitmap.
const MaxValidators = 4096

// unpackBoolBitmap unpacks a compact byte bitmap into []bool.
func unpackBoolBitmap(data []byte) []bool {
	if len(data) < 2 {
		return nil
	}
	count := int(data[0]) | int(data[1])<<8
	if count == 0 {
		return nil
	}
	// Validate count against available bitmap data and hard cap.
	if count > MaxValidators {
		return nil
	}
	bitmapBytes := len(data) - 2
	maxFromData := bitmapBytes * 8
	if count > maxFromData {
		return nil
	}
	bools := make([]bool, count)
	for i := 0; i < count; i++ {
		byteIdx := 2 + i/8
		bools[i] = data[byteIdx]&(1<<(uint(i)%8)) != 0
	}
	return bools
}
