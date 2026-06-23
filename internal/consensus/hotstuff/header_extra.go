// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Header extra-data encoding for HotStuff-2 blocks.
// buildHeaderExtra writes the fixed-size magic prefix, the current
// view number in little-endian uint64 form and the committed
// QuorumCertificate into the block header extra field. extraSealLen
// reserves 96 bytes for the trailing BLS seal appended at sealing time.

package hotstuff

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
)

const extraSealLen = 96

func buildHeaderExtra(view ViewNumber, committedQC *QuorumCertificate) ([]byte, error) {
	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], extraMagic[:])
	binary.LittleEndian.PutUint64(extra[extraMagicLen:], view)

	if isEmptyHeaderQC(committedQC) {
		// Reserve the trailing seal area (filled in place by Seal) so SealHash —
		// which strips the last extraSealLen bytes — yields the SAME hash before
		// and after sealing. Without this reserve, an unsealed header with a large
		// Extra (QC present) gets its QC truncated by the strip while a sealed one
		// only loses the seal, so the miner's pendingTasks key never matches.
		return append(extra, make([]byte, extraSealLen)...), nil
	}

	qcBytes, err := encodeQC(committedQC)
	if err != nil {
		return nil, fmt.Errorf("encode header QC: %w", err)
	}
	extra = append(extra, qcBytes...)
	// Reserve the trailing seal area (see above).
	return append(extra, make([]byte, extraSealLen)...), nil
}

func decodeHeaderExtra(extra []byte) (ViewNumber, *QuorumCertificate, bool, error) {
	view, err := ExtractViewFromExtra(extra)
	if err != nil {
		return 0, nil, false, err
	}

	payload := extra[extraMinLen:]
	switch len(payload) {
	case 0:
		return view, nil, false, nil
	case extraSealLen:
		return view, nil, true, nil
	}

	if len(payload) > extraSealLen {
		// Current layout: QC + BLS seal (96 bytes)
		qc, err := decodeQC(payload[:len(payload)-extraSealLen])
		if err == nil {
			return view, qc, true, nil
		}
		// Fallback: try without stripping seal (legacy layout)
		qc2, err2 := decodeQC(payload)
		if err2 != nil {
			// Both layouts failed — report both errors
			return 0, nil, false, fmt.Errorf("invalid QC in extra-data (seal-stripped: %v; raw: %w)", err, err2)
		}
		return view, qc2, false, nil
	}

	qc, err := decodeQC(payload)
	if err != nil {
		return 0, nil, false, fmt.Errorf("invalid QC in extra-data: %w", err)
	}
	return view, qc, false, nil
}

// ExtractHeaderQC returns the optional QC embedded in HotStuff header extra-data.
// It accepts both legacy layouts (magic+view+QC or magic+view+seal) and the
// current layout (magic+view+QC+seal).
func ExtractHeaderQC(extra []byte) (*QuorumCertificate, error) {
	_, qc, _, err := decodeHeaderExtra(extra)
	return qc, err
}

func isEmptyHeaderQC(qc *QuorumCertificate) bool {
	if qc == nil {
		return true
	}
	return qc.View == 0 &&
		qc.BlockHash == (types.Hash{}) &&
		len(qc.AggregateSignature) == 0 &&
		len(qc.Signers) == 0
}
