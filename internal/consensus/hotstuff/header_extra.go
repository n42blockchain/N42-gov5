// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

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
		return extra, nil
	}

	qcBytes, err := encodeQC(committedQC)
	if err != nil {
		return nil, fmt.Errorf("encode header QC: %w", err)
	}
	return append(extra, qcBytes...), nil
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
		qc, err := decodeQC(payload[:len(payload)-extraSealLen])
		if err == nil {
			return view, qc, true, nil
		}
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
