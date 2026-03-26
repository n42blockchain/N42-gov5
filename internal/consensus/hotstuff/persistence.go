// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// persistence key
var hotstuffStateKey = []byte("state")

// ConsensusState holds the persisted consensus state for crash recovery.
type ConsensusState struct {
	View                ViewNumber
	ConsecutiveTimeouts uint32
	LockedQC            QuorumCertificate
	LastCommittedQC     QuorumCertificate
}

// SaveConsensusState persists the current consensus state to the database.
func SaveConsensusState(tx kv.RwTx, state *ConsensusState) error {
	lockedQCBytes, err := encodeQC(&state.LockedQC)
	if err != nil {
		return fmt.Errorf("encode locked_qc: %w", err)
	}
	committedQCBytes, err := encodeQC(&state.LastCommittedQC)
	if err != nil {
		return fmt.Errorf("encode committed_qc: %w", err)
	}

	// Format: view(8) + consecutiveTimeouts(4) + lockedQC_len(4) + lockedQC + committedQC
	size := 8 + 4 + 4 + len(lockedQCBytes) + len(committedQCBytes)
	buf := make([]byte, size)

	binary.LittleEndian.PutUint64(buf[0:8], state.View)
	binary.LittleEndian.PutUint32(buf[8:12], state.ConsecutiveTimeouts)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(len(lockedQCBytes)))
	copy(buf[16:16+len(lockedQCBytes)], lockedQCBytes)
	copy(buf[16+len(lockedQCBytes):], committedQCBytes)

	return tx.Put(modules.HotStuffState, hotstuffStateKey, buf)
}

// LoadConsensusState loads the persisted consensus state from the database.
// Returns nil if no state exists.
func LoadConsensusState(tx kv.Tx) (*ConsensusState, error) {
	val, err := tx.GetOne(modules.HotStuffState, hotstuffStateKey)
	if err != nil {
		return nil, fmt.Errorf("read hotstuff state: %w", err)
	}
	if val == nil || len(val) < 16 {
		return nil, nil // no persisted state
	}

	view := binary.LittleEndian.Uint64(val[0:8])
	consecutiveTimeouts := binary.LittleEndian.Uint32(val[8:12])
	lockedQCLen := binary.LittleEndian.Uint32(val[12:16])

	if uint64(len(val)) < 16+uint64(lockedQCLen) {
		return nil, fmt.Errorf("hotstuff state corrupted: buffer too short for locked_qc")
	}

	lockedQC, err := decodeQC(val[16 : 16+lockedQCLen])
	if err != nil {
		return nil, fmt.Errorf("decode locked_qc: %w", err)
	}

	committedQCData := val[16+lockedQCLen:]
	committedQC, err := decodeQC(committedQCData)
	if err != nil {
		return nil, fmt.Errorf("decode committed_qc: %w", err)
	}

	return &ConsensusState{
		View:                view,
		ConsecutiveTimeouts: consecutiveTimeouts,
		LockedQC:            *lockedQC,
		LastCommittedQC:     *committedQC,
	}, nil
}

// SaveEquivocationEvidence persists equivocation evidence for future slashing.
func SaveEquivocationEvidence(tx kv.RwTx, view ViewNumber, validator ValidatorIndex, prevHash, newHash types.Hash) error {
	key := fmt.Appendf(nil, "equivocation/%d/%d", view, validator)
	// Value: prevHash(32) + newHash(32) = 64 bytes
	val := make([]byte, 64)
	copy(val[0:32], prevHash[:])
	copy(val[32:64], newHash[:])
	return tx.Put(modules.HotStuffState, key, val)
}
