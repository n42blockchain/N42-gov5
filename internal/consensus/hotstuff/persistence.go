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
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// persistence key
var hotstuffStateKey = []byte("state")

// consensusStateMagicV2 tags the versioned ConsensusState record. It doubles as
// the version discriminator: a legacy (v1) record starts with the view as a
// little-endian uint64, so its first eight bytes can never equal this magic —
// interpreted that way the magic is ~3.6e18, a view number no chain can reach.
// LoadConsensusState therefore distinguishes the two layouts with no ambiguity
// and no migration step.
var consensusStateMagicV2 = []byte("N42HSSv2")

// consensusStateHeaderV2 is the fixed-size prefix of a v2 record:
// magic(8) + view(8) + consecutiveTimeouts(4) + lastVotedView(8) +
// lastVotedHash(32) + lastCommitVotedView(8) + lastCommitVotedHash(32).
const consensusStateHeaderV2 = 8 + 8 + 4 + 8 + 32 + 8 + 32

// ConsensusState holds the persisted consensus state for crash recovery.
//
// The LastVoted* fields are the durable record of this node's outstanding vote
// commitments. HotStuff safety requires a vote to be on disk BEFORE it is
// released to the network: without it a node that restarts inside a view can
// cast a second, conflicting vote for the same view — equivocation, which this
// node's own detector (voting.go) would slash — and can resume with a LockedQC
// that names a branch it never applied, which wedges block production
// permanently (see cmd/hotstuff-reset).
type ConsensusState struct {
	View                ViewNumber
	ConsecutiveTimeouts uint32
	LockedQC            QuorumCertificate
	LastCommittedQC     QuorumCertificate

	// LastVotedView/LastVotedHash record the Round 1 (Prepare) vote this node
	// last released. Zero view = never voted.
	LastVotedView ViewNumber
	LastVotedHash types.Hash

	// LastCommitVotedView/LastCommitVotedHash record the Round 2 (Commit) vote
	// this node last released. Zero view = never commit-voted.
	LastCommitVotedView ViewNumber
	LastCommitVotedHash types.Hash
}

// SaveConsensusState persists the current consensus state to the database.
// Always writes the v2 layout; LoadConsensusState still reads v1 records.
//
// The write is monotonic: it can only ever move the record FORWARD. Two
// goroutines write this key — the engine goroutine via JournalVote (always
// current, holding the engine mutex) and the service's periodic/shutdown
// persistState, which snapshots under the mutex and writes later, so its
// snapshot can be stale by the time its transaction runs. Without the merge
// below, that stale write un-says a vote already on disk, and a restart in that
// window re-votes in a view it had already committed to — the exact
// equivocation the vote journal exists to prevent.
func SaveConsensusState(tx kv.RwTx, state *ConsensusState) error {
	state, err := mergeMonotonic(tx, state)
	if err != nil {
		return err
	}
	lockedQCBytes, err := encodeQC(&state.LockedQC)
	if err != nil {
		return fmt.Errorf("encode locked_qc: %w", err)
	}
	committedQCBytes, err := encodeQC(&state.LastCommittedQC)
	if err != nil {
		return fmt.Errorf("encode committed_qc: %w", err)
	}

	// v2: header + lockedQC_len(4) + lockedQC + committedQC_len(4) + committedQC.
	// Both QCs are length-prefixed (v1 length-prefixed only the first and let the
	// second run to the end of the buffer), so a future revision can append new
	// trailing fields without breaking this decoder.
	size := consensusStateHeaderV2 + 4 + len(lockedQCBytes) + 4 + len(committedQCBytes)
	buf := make([]byte, size)

	pos := copy(buf, consensusStateMagicV2)
	binary.LittleEndian.PutUint64(buf[pos:], state.View)
	pos += 8
	binary.LittleEndian.PutUint32(buf[pos:], state.ConsecutiveTimeouts)
	pos += 4
	binary.LittleEndian.PutUint64(buf[pos:], state.LastVotedView)
	pos += 8
	pos += copy(buf[pos:], state.LastVotedHash[:])
	binary.LittleEndian.PutUint64(buf[pos:], state.LastCommitVotedView)
	pos += 8
	pos += copy(buf[pos:], state.LastCommitVotedHash[:])

	binary.LittleEndian.PutUint32(buf[pos:], uint32(len(lockedQCBytes)))
	pos += 4
	pos += copy(buf[pos:], lockedQCBytes)
	binary.LittleEndian.PutUint32(buf[pos:], uint32(len(committedQCBytes)))
	pos += 4
	copy(buf[pos:], committedQCBytes)

	return tx.Put(modules.HotStuffState, hotstuffStateKey, buf)
}

// mergeMonotonic folds the record already on disk into the state about to be
// written so no field can regress. It runs inside the caller's write
// transaction, and MDBX serializes writers, so the read-modify-write is atomic
// against the other writer.
//
// The independent monotonic commitments are compared separately:
//
//   - the vote commitments (LastVoted*, LastCommitVoted*) — a released vote is
//     append-only and must never be withdrawn. Conflicting non-zero hashes at
//     the same view are rejected because accepting either would hide an SR9
//     double-vote;
//   - the round snapshot (View, ConsecutiveTimeouts, LockedQC, LastCommittedQC)
//     — the view and timeout counter follow the newer snapshot, while each QC is
//     independently kept at its highest view. A delayed same-view snapshot must
//     never overwrite a newer lock learned later in that view.
//
// Both reset tools (cmd/hotstuff-reset, cmd/qs-hsreset) DELETE the key rather
// than writing a lower state, so deliberate rollback still works.
func mergeMonotonic(tx kv.RwTx, state *ConsensusState) (*ConsensusState, error) {
	prev, err := LoadConsensusState(tx)
	if err != nil {
		// An unreadable record must not silently become a blank slate: refusing
		// the write keeps whatever is on disk, which is the safe direction.
		return nil, fmt.Errorf("read previous hotstuff state: %w", err)
	}
	if prev == nil {
		return state, nil
	}

	merged := *state
	if err := mergeVoteCommitment(
		&merged.LastVotedView, &merged.LastVotedHash,
		prev.LastVotedView, prev.LastVotedHash,
		"prepare",
	); err != nil {
		return nil, err
	}
	if err := mergeVoteCommitment(
		&merged.LastCommitVotedView, &merged.LastCommitVotedHash,
		prev.LastCommitVotedView, prev.LastCommitVotedHash,
		"commit",
	); err != nil {
		return nil, err
	}
	if prev.View > merged.View {
		merged.View = prev.View
		merged.ConsecutiveTimeouts = prev.ConsecutiveTimeouts
	} else if prev.View == merged.View && prev.ConsecutiveTimeouts > merged.ConsecutiveTimeouts {
		merged.ConsecutiveTimeouts = prev.ConsecutiveTimeouts
	}
	if err := mergeQCMonotonic(&merged.LockedQC, prev.LockedQC, "locked"); err != nil {
		return nil, err
	}
	if err := mergeQCMonotonic(&merged.LastCommittedQC, prev.LastCommittedQC, "last committed"); err != nil {
		return nil, err
	}
	return &merged, nil
}

func mergeVoteCommitment(dstView *ViewNumber, dstHash *types.Hash, prevView ViewNumber, prevHash types.Hash, round string) error {
	if prevView > *dstView {
		*dstView = prevView
		*dstHash = prevHash
		return nil
	}
	if prevView != *dstView || prevView == 0 {
		return nil
	}
	if *dstHash == (types.Hash{}) {
		*dstHash = prevHash
		return nil
	}
	if prevHash != (types.Hash{}) && prevHash != *dstHash {
		return fmt.Errorf("conflicting durable %s votes at view %d: %x != %x", round, prevView, prevHash, *dstHash)
	}
	return nil
}

func mergeQCMonotonic(dst *QuorumCertificate, prev QuorumCertificate, name string) error {
	if prev.View > dst.View {
		*dst = prev
		return nil
	}
	if prev.View != dst.View || prev.View == 0 {
		return nil
	}
	if dst.BlockHash == (types.Hash{}) {
		*dst = prev
		return nil
	}
	if prev.BlockHash != (types.Hash{}) && prev.BlockHash != dst.BlockHash {
		return fmt.Errorf("conflicting %s QCs at view %d: %x != %x", name, prev.View, prev.BlockHash, dst.BlockHash)
	}
	return nil
}

// LoadConsensusState loads the persisted consensus state from the database.
// Returns nil if no state exists. Records written by a pre-v2 binary decode
// with the vote fields left zero ("never voted"), which is the conservative
// reading: the node treats every view as un-voted and re-derives its position
// from the network.
func LoadConsensusState(tx kv.Tx) (*ConsensusState, error) {
	val, err := tx.GetOne(modules.HotStuffState, hotstuffStateKey)
	if err != nil {
		return nil, fmt.Errorf("read hotstuff state: %w", err)
	}
	if val == nil || len(val) < 16 {
		return nil, nil // no persisted state
	}
	if len(val) >= len(consensusStateMagicV2) && bytes.Equal(val[:len(consensusStateMagicV2)], consensusStateMagicV2) {
		return decodeConsensusStateV2(val)
	}
	return decodeConsensusStateV1(val)
}

func decodeConsensusStateV2(val []byte) (*ConsensusState, error) {
	if len(val) < consensusStateHeaderV2+8 {
		return nil, fmt.Errorf("hotstuff state corrupted: v2 record too short (%d bytes)", len(val))
	}
	st := &ConsensusState{}
	pos := len(consensusStateMagicV2)
	st.View = binary.LittleEndian.Uint64(val[pos:])
	pos += 8
	st.ConsecutiveTimeouts = binary.LittleEndian.Uint32(val[pos:])
	pos += 4
	st.LastVotedView = binary.LittleEndian.Uint64(val[pos:])
	pos += 8
	copy(st.LastVotedHash[:], val[pos:pos+32])
	pos += 32
	st.LastCommitVotedView = binary.LittleEndian.Uint64(val[pos:])
	pos += 8
	copy(st.LastCommitVotedHash[:], val[pos:pos+32])
	pos += 32

	lockedQCBytes, next, err := readLenPrefixed(val, pos, "locked_qc")
	if err != nil {
		return nil, err
	}
	lockedQC, err := decodeQC(lockedQCBytes)
	if err != nil {
		return nil, fmt.Errorf("decode locked_qc: %w", err)
	}
	committedQCBytes, _, err := readLenPrefixed(val, next, "committed_qc")
	if err != nil {
		return nil, err
	}
	committedQC, err := decodeQC(committedQCBytes)
	if err != nil {
		return nil, fmt.Errorf("decode committed_qc: %w", err)
	}
	st.LockedQC = *lockedQC
	st.LastCommittedQC = *committedQC
	return st, nil
}

// decodeConsensusStateV1 reads the original layout:
// view(8) + consecutiveTimeouts(4) + lockedQC_len(4) + lockedQC + committedQC.
func decodeConsensusStateV1(val []byte) (*ConsensusState, error) {
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
		// Vote fields intentionally zero: a v1 record carries no vote history.
	}, nil
}

// readLenPrefixed reads a uint32 length prefix at pos and returns the payload
// plus the offset just past it.
func readLenPrefixed(val []byte, pos int, field string) ([]byte, int, error) {
	if pos+4 > len(val) {
		return nil, 0, fmt.Errorf("hotstuff state corrupted: missing %s length", field)
	}
	n := int(binary.LittleEndian.Uint32(val[pos:]))
	pos += 4
	if n < 0 || pos+n > len(val) {
		return nil, 0, fmt.Errorf("hotstuff state corrupted: buffer too short for %s", field)
	}
	return val[pos : pos+n], pos + n, nil
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

// ActivatePersistedEpoch atomically promotes an epoch record and consumes its
// staged record. Keeping both writes in one transaction prevents crash recovery
// from observing a half-transition.
func ActivatePersistedEpoch(tx kv.RwTx, epoch uint64, validators []ValidatorInfo, f uint32) error {
	if len(validators) == 0 {
		return fmt.Errorf("refuse to activate empty validator set for epoch %d", epoch)
	}
	if err := SaveActiveEpoch(tx, epoch, validators, f); err != nil {
		return err
	}
	return ClearStagedEpoch(tx)
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
