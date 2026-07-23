// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// HotStuff durable state persistence via the kv store.
// hotstuffStateKey is the single MDBX key under which the engine
// writes its view, locked QC, highest QC and vote history blobs.
// Load and Save functions round-trip the state across restarts so
// recovery cannot equivocate after a crash mid-round.

package hotstuff

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// persistence key
var hotstuffStateKey = []byte("state")
var hotstuffPhaseKey = []byte("state_phase_v1")

// ConsensusState holds the persisted consensus state for crash recovery.
type ConsensusState struct {
	View                ViewNumber
	Phase               Phase
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

	if err := tx.Put(modules.HotStuffState, hotstuffStateKey, buf); err != nil {
		return err
	}
	// Keep phase in a separate, versioned key so older binaries can still read
	// the existing consensus-state encoding during rollback.
	return tx.Put(modules.HotStuffState, hotstuffPhaseKey, []byte{byte(state.Phase)})
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

	phase := PhaseWaitingForProposal
	phaseData, err := tx.GetOne(modules.HotStuffState, hotstuffPhaseKey)
	if err != nil {
		return nil, fmt.Errorf("read hotstuff phase: %w", err)
	}
	if len(phaseData) > 0 {
		phase = Phase(phaseData[0])
		if phase > PhaseTimedOut {
			return nil, fmt.Errorf("hotstuff phase corrupted: %d", phase)
		}
	}

	return &ConsensusState{
		View:                view,
		Phase:               phase,
		ConsecutiveTimeouts: consecutiveTimeouts,
		LockedQC:            *lockedQC,
		LastCommittedQC:     *committedQC,
	}, nil
}

// Staged epoch persistence key
var stagedEpochKey = []byte("staged_epoch")

// SaveStagedEpoch persists the staged validator set so it survives crashes
// between commit and epoch boundary (Rust: staged_epoch_info).
func SaveStagedEpoch(tx kv.RwTx, epoch uint64, validators []ValidatorInfo, f uint32) error {
	// Format: epoch(8) + f(4) + count(4) + [addr(20) + pkLen(4) + pk(48)]...
	count := len(validators)
	size := 8 + 4 + 4
	for _, v := range validators {
		pkBytes := v.PublicKey.Marshal()
		size += 20 + 4 + len(pkBytes)
	}

	buf := make([]byte, size)
	binary.LittleEndian.PutUint64(buf[0:8], epoch)
	binary.LittleEndian.PutUint32(buf[8:12], f)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(count))

	offset := 16
	for _, v := range validators {
		copy(buf[offset:offset+20], v.Address[:])
		offset += 20
		pkBytes := v.PublicKey.Marshal()
		binary.LittleEndian.PutUint32(buf[offset:offset+4], uint32(len(pkBytes)))
		offset += 4
		copy(buf[offset:offset+len(pkBytes)], pkBytes)
		offset += len(pkBytes)
	}

	return tx.Put(modules.HotStuffState, stagedEpochKey, buf[:offset])
}

// LoadStagedEpoch loads a previously staged epoch. Returns nil if none exists.
func LoadStagedEpoch(tx kv.Tx) (epoch uint64, validators []ValidatorInfo, f uint32, err error) {
	val, err := tx.GetOne(modules.HotStuffState, stagedEpochKey)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("read staged epoch: %w", err)
	}
	if val == nil || len(val) < 16 {
		return 0, nil, 0, nil // no staged epoch
	}

	epoch = binary.LittleEndian.Uint64(val[0:8])
	f = binary.LittleEndian.Uint32(val[8:12])
	count := binary.LittleEndian.Uint32(val[12:16])
	validators = make([]ValidatorInfo, 0, count)

	offset := 16
	for i := uint32(0); i < count; i++ {
		if offset+24 > len(val) {
			return 0, nil, 0, fmt.Errorf("staged epoch data truncated at validator %d", i)
		}
		var addr types.Address
		copy(addr[:], val[offset:offset+20])
		offset += 20
		pkLen := binary.LittleEndian.Uint32(val[offset : offset+4])
		offset += 4
		if offset+int(pkLen) > len(val) {
			return 0, nil, 0, fmt.Errorf("staged epoch data truncated at validator %d pubkey", i)
		}
		pk, pErr := bls.PublicKeyFromBytes(val[offset : offset+int(pkLen)])
		if pErr != nil {
			return 0, nil, 0, fmt.Errorf("validator %d BLS key: %w", i, pErr)
		}
		offset += int(pkLen)
		validators = append(validators, ValidatorInfo{Address: addr, PublicKey: pk})
	}

	return epoch, validators, f, nil
}

// ClearStagedEpoch removes persisted staged epoch data after advance.
func ClearStagedEpoch(tx kv.RwTx) error {
	return tx.Delete(modules.HotStuffState, stagedEpochKey)
}

// activeEpochKey stores the ACTIVE (applied) validator set, so a node that
// restarts after a reconfiguration restores the reconfigured set instead of
// reverting to the genesis set. LoadStagedEpoch's format is reused verbatim.
var activeEpochKey = []byte("active_epoch")

// SaveActiveEpoch persists the current active validator set + epoch. Called on
// every epoch transition (activation), so the on-disk record always reflects the
// live set. Reuses the staged-epoch wire format (same bytes, different key).
func SaveActiveEpoch(tx kv.RwTx, epoch uint64, validators []ValidatorInfo, f uint32) error {
	count := len(validators)
	size := 16
	for _, v := range validators {
		size += 20 + 4 + len(v.PublicKey.Marshal())
	}
	buf := make([]byte, size)
	binary.LittleEndian.PutUint64(buf[0:8], epoch)
	binary.LittleEndian.PutUint32(buf[8:12], f)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(count))
	offset := 16
	for _, v := range validators {
		copy(buf[offset:offset+20], v.Address[:])
		offset += 20
		pkBytes := v.PublicKey.Marshal()
		binary.LittleEndian.PutUint32(buf[offset:offset+4], uint32(len(pkBytes)))
		offset += 4
		copy(buf[offset:offset+len(pkBytes)], pkBytes)
		offset += len(pkBytes)
	}
	return tx.Put(modules.HotStuffState, activeEpochKey, buf[:offset])
}

// LoadActiveEpoch loads the persisted active validator set. Returns ok=false when
// none has been recorded (no reconfiguration has ever applied on this node).
func LoadActiveEpoch(tx kv.Tx) (epoch uint64, validators []ValidatorInfo, f uint32, ok bool, err error) {
	val, gErr := tx.GetOne(modules.HotStuffState, activeEpochKey)
	if gErr != nil {
		return 0, nil, 0, false, fmt.Errorf("read active epoch: %w", gErr)
	}
	if val == nil || len(val) < 16 {
		return 0, nil, 0, false, nil
	}
	epoch = binary.LittleEndian.Uint64(val[0:8])
	f = binary.LittleEndian.Uint32(val[8:12])
	count := binary.LittleEndian.Uint32(val[12:16])
	validators = make([]ValidatorInfo, 0, count)
	offset := 16
	for i := uint32(0); i < count; i++ {
		if offset+24 > len(val) {
			return 0, nil, 0, false, fmt.Errorf("active epoch data truncated at validator %d", i)
		}
		var addr types.Address
		copy(addr[:], val[offset:offset+20])
		offset += 20
		pkLen := binary.LittleEndian.Uint32(val[offset : offset+4])
		offset += 4
		if offset+int(pkLen) > len(val) {
			return 0, nil, 0, false, fmt.Errorf("active epoch data truncated at validator %d pubkey", i)
		}
		pk, pErr := bls.PublicKeyFromBytes(val[offset : offset+int(pkLen)])
		if pErr != nil {
			return 0, nil, 0, false, fmt.Errorf("validator %d BLS key: %w", i, pErr)
		}
		offset += int(pkLen)
		validators = append(validators, ValidatorInfo{Address: addr, PublicKey: pk})
	}
	return epoch, validators, f, true, nil
}

// --- Vote persistence for crash recovery ---

var pendingVotesKey = []byte("pending_votes")

// PendingVotesState captures in-flight vote state for crash recovery.
type PendingVotesState struct {
	View         ViewNumber
	BlockHash    types.Hash
	PrepareVotes map[ValidatorIndex][]byte // validator → raw BLS sig bytes
	CommitVotes  map[ValidatorIndex][]byte
}

// SavePendingVotes persists pending votes to the database.
// Format: view(8) + blockHash(32) + prepareCount(4) + [idx(4)+sigLen(4)+sig(var)]... + commitCount(4) + ...
func SavePendingVotes(tx kv.RwTx, pv *PendingVotesState) error {
	if pv == nil {
		return tx.Delete(modules.HotStuffState, pendingVotesKey)
	}
	size := 8 + 32 + 4 + 4
	for _, sig := range pv.PrepareVotes {
		size += 4 + 4 + len(sig)
	}
	for _, sig := range pv.CommitVotes {
		size += 4 + 4 + len(sig)
	}
	buf := make([]byte, size)
	pos := 0
	binary.LittleEndian.PutUint64(buf[pos:], uint64(pv.View))
	pos += 8
	copy(buf[pos:], pv.BlockHash[:])
	pos += 32
	binary.LittleEndian.PutUint32(buf[pos:], uint32(len(pv.PrepareVotes)))
	pos += 4
	for idx, sig := range pv.PrepareVotes {
		binary.LittleEndian.PutUint32(buf[pos:], uint32(idx))
		pos += 4
		binary.LittleEndian.PutUint32(buf[pos:], uint32(len(sig)))
		pos += 4
		copy(buf[pos:], sig)
		pos += len(sig)
	}
	binary.LittleEndian.PutUint32(buf[pos:], uint32(len(pv.CommitVotes)))
	pos += 4
	for idx, sig := range pv.CommitVotes {
		binary.LittleEndian.PutUint32(buf[pos:], uint32(idx))
		pos += 4
		binary.LittleEndian.PutUint32(buf[pos:], uint32(len(sig)))
		pos += 4
		copy(buf[pos:], sig)
		pos += len(sig)
	}
	return tx.Put(modules.HotStuffState, pendingVotesKey, buf[:pos])
}

// LoadPendingVotes loads persisted pending votes.
func LoadPendingVotes(tx kv.Tx) (*PendingVotesState, error) {
	data, err := tx.GetOne(modules.HotStuffState, pendingVotesKey)
	if err != nil || len(data) < 44 {
		return nil, err
	}
	pv := &PendingVotesState{
		PrepareVotes: make(map[ValidatorIndex][]byte),
		CommitVotes:  make(map[ValidatorIndex][]byte),
	}
	pos := 0
	pv.View = ViewNumber(binary.LittleEndian.Uint64(data[pos:]))
	pos += 8
	copy(pv.BlockHash[:], data[pos:])
	pos += 32

	readVotes := func() (map[ValidatorIndex][]byte, int) {
		count := int(binary.LittleEndian.Uint32(data[pos:]))
		pos += 4
		m := make(map[ValidatorIndex][]byte, count)
		for i := 0; i < count && pos+8 <= len(data); i++ {
			idx := ValidatorIndex(binary.LittleEndian.Uint32(data[pos:]))
			pos += 4
			sLen := int(binary.LittleEndian.Uint32(data[pos:]))
			pos += 4
			if pos+sLen > len(data) {
				break
			}
			sig := make([]byte, sLen)
			copy(sig, data[pos:pos+sLen])
			pos += sLen
			m[idx] = sig
		}
		return m, pos
	}
	pv.PrepareVotes, pos = readVotes()
	pv.CommitVotes, _ = readVotes()
	return pv, nil
}

// ClearPendingVotes removes persisted pending votes after commit.
func ClearPendingVotes(tx kv.RwTx) error {
	return tx.Delete(modules.HotStuffState, pendingVotesKey)
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
