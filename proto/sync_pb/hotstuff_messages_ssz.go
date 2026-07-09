// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync_pb

// Hand-written SSZ codecs for HotStuff-2 consensus wire-format messages.
// Follows the same length-prefixed binary pattern as snap_messages_ssz.go.

// ============================================================================
// HotStuffQC
// ============================================================================

func (m *HotStuffQC) SizeSSZ() int {
	return 8 + sizeBytes(m.BlockHash) + sizeBytes(m.AggregateSignature) + sizeBytes(m.SignersBitmap)
}

func (m *HotStuffQC) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffQC) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.View)
	dst = putBytes(dst, m.BlockHash)
	dst = putBytes(dst, m.AggregateSignature)
	dst = putBytes(dst, m.SignersBitmap)
	return dst, nil
}

func (m *HotStuffQC) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.View, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.BlockHash, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.AggregateSignature, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.SignersBitmap, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

// ============================================================================
// HotStuffTC
// ============================================================================

func (m *HotStuffTC) SizeSSZ() int {
	return 8 + sizeBytes(m.AggregateSignature) + sizeBytes(m.SignersBitmap) + sizeBytes(m.HighQC)
}

func (m *HotStuffTC) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffTC) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.View)
	dst = putBytes(dst, m.AggregateSignature)
	dst = putBytes(dst, m.SignersBitmap)
	dst = putBytes(dst, m.HighQC)
	return dst, nil
}

func (m *HotStuffTC) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.View, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.AggregateSignature, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.SignersBitmap, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.HighQC, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

// ============================================================================
// HotStuffProposal
// ============================================================================

func (m *HotStuffProposal) SizeSSZ() int {
	return 8 + sizeBytes(m.BlockHash) + sizeBytes(m.JustifyQC) + 4 + sizeBytes(m.Signature) + sizeBytes(m.PrepareQC) + sizeBytes(m.TxRootHash)
}

func (m *HotStuffProposal) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffProposal) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.View)
	dst = putBytes(dst, m.BlockHash)
	dst = putBytes(dst, m.JustifyQC)
	dst = putUint32(dst, m.Proposer)
	dst = putBytes(dst, m.Signature)
	dst = putBytes(dst, m.PrepareQC)
	dst = putBytes(dst, m.TxRootHash)
	return dst, nil
}

func (m *HotStuffProposal) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.View, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.BlockHash, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.JustifyQC, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Proposer, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if m.Signature, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.PrepareQC, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.TxRootHash, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

// ============================================================================
// HotStuffVote
// ============================================================================

func (m *HotStuffVote) SizeSSZ() int {
	return 8 + sizeBytes(m.BlockHash) + 4 + sizeBytes(m.Signature) + sizeBytes(m.HighTC)
}

func (m *HotStuffVote) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffVote) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.View)
	dst = putBytes(dst, m.BlockHash)
	dst = putUint32(dst, m.Voter)
	dst = putBytes(dst, m.Signature)
	dst = putBytes(dst, m.HighTC)
	return dst, nil
}

func (m *HotStuffVote) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.View, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.BlockHash, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Voter, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if m.Signature, off, err = readBytes(buf, off); err != nil {
		return err
	}
	// HighTC is an optional trailing SyncInfo field; tolerate its absence for
	// forward/backward compatibility across binary versions.
	if off < len(buf) {
		if m.HighTC, _, err = readBytes(buf, off); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================================
// HotStuffCommitVote
// ============================================================================

func (m *HotStuffCommitVote) SizeSSZ() int {
	return 8 + sizeBytes(m.BlockHash) + 4 + sizeBytes(m.Signature) + sizeBytes(m.HighTC)
}

func (m *HotStuffCommitVote) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffCommitVote) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.View)
	dst = putBytes(dst, m.BlockHash)
	dst = putUint32(dst, m.Voter)
	dst = putBytes(dst, m.Signature)
	dst = putBytes(dst, m.HighTC)
	return dst, nil
}

func (m *HotStuffCommitVote) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.View, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.BlockHash, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Voter, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if m.Signature, off, err = readBytes(buf, off); err != nil {
		return err
	}
	// HighTC is an optional trailing SyncInfo field; tolerate its absence.
	if off < len(buf) {
		if m.HighTC, _, err = readBytes(buf, off); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================================
// HotStuffPrepareQCMsg
// ============================================================================

func (m *HotStuffPrepareQCMsg) SizeSSZ() int {
	return 8 + sizeBytes(m.BlockHash) + sizeBytes(m.QC)
}

func (m *HotStuffPrepareQCMsg) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffPrepareQCMsg) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.View)
	dst = putBytes(dst, m.BlockHash)
	dst = putBytes(dst, m.QC)
	return dst, nil
}

func (m *HotStuffPrepareQCMsg) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.View, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.BlockHash, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.QC, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

// ============================================================================
// HotStuffTimeoutMsg
// ============================================================================

func (m *HotStuffTimeoutMsg) SizeSSZ() int {
	return 8 + sizeBytes(m.HighQC) + 4 + sizeBytes(m.Signature) + sizeBytes(m.HighTC)
}

func (m *HotStuffTimeoutMsg) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffTimeoutMsg) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.View)
	dst = putBytes(dst, m.HighQC)
	dst = putUint32(dst, m.Sender)
	dst = putBytes(dst, m.Signature)
	dst = putBytes(dst, m.HighTC)
	return dst, nil
}

func (m *HotStuffTimeoutMsg) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.View, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.HighQC, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Sender, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if m.Signature, off, err = readBytes(buf, off); err != nil {
		return err
	}
	// HighTC is an optional trailing SyncInfo field; tolerate its absence.
	if off < len(buf) {
		if m.HighTC, _, err = readBytes(buf, off); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================================
// HotStuffNewViewMsg
// ============================================================================

func (m *HotStuffNewViewMsg) SizeSSZ() int {
	return 8 + sizeBytes(m.TimeoutCert) + 4 + sizeBytes(m.Signature)
}

func (m *HotStuffNewViewMsg) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffNewViewMsg) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.View)
	dst = putBytes(dst, m.TimeoutCert)
	dst = putUint32(dst, m.Leader)
	dst = putBytes(dst, m.Signature)
	return dst, nil
}

func (m *HotStuffNewViewMsg) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.View, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.TimeoutCert, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.Leader, off, err = readUint32(buf, off); err != nil {
		return err
	}
	if m.Signature, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

// ============================================================================
// HotStuffDecide
// ============================================================================

func (m *HotStuffDecide) SizeSSZ() int {
	return 8 + sizeBytes(m.BlockHash) + sizeBytes(m.CommitQC)
}

func (m *HotStuffDecide) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffDecide) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = putUint64(dst, m.View)
	dst = putBytes(dst, m.BlockHash)
	dst = putBytes(dst, m.CommitQC)
	return dst, nil
}

func (m *HotStuffDecide) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if m.View, off, err = readUint64(buf, off); err != nil {
		return err
	}
	if m.BlockHash, off, err = readBytes(buf, off); err != nil {
		return err
	}
	if m.CommitQC, _, err = readBytes(buf, off); err != nil {
		return err
	}
	return nil
}

// ============================================================================
// HotStuffConsensusMsg (envelope)
// ============================================================================

func (m *HotStuffConsensusMsg) SizeSSZ() int {
	return 1 + sizeBytes(m.Payload)
}

func (m *HotStuffConsensusMsg) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, m.SizeSSZ())
	return m.MarshalSSZTo(buf)
}

func (m *HotStuffConsensusMsg) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = append(dst, m.Type)
	dst = putBytes(dst, m.Payload)
	return dst, nil
}

func (m *HotStuffConsensusMsg) UnmarshalSSZ(buf []byte) error {
	if len(buf) < 1 {
		return errShortBuf
	}
	m.Type = buf[0]
	var err error
	if m.Payload, _, err = readBytes(buf, 1); err != nil {
		return err
	}
	return nil
}
