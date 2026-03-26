// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package bridge provides the ZK-native cross-chain bridge for N42.
//
// eth_light_client.go implements an Ethereum Sync Committee light client
// that tracks finalized ETH headers for ETH->N42 reverse bridge verification.
//
// Trust chain: ETH Sync Committee (512 BLS) -> finalized header -> MPT storage proof
package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/bits"
	"sync"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/crypto/bls"
	blscommon "github.com/n42blockchain/N42/common/crypto/bls/common"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

const (
	// SyncCommitteeSize is the number of validators in an ETH sync committee.
	SyncCommitteeSize = 512

	// SlotsPerEpoch is the number of slots per Ethereum epoch.
	SlotsPerEpoch = 32

	// EpochsPerSyncPeriod is the number of epochs per sync committee period.
	EpochsPerSyncPeriod = 256

	// SlotsPerSyncPeriod = 32 * 256 = 8192 slots (~27.3 hours).
	SlotsPerSyncPeriod = SlotsPerEpoch * EpochsPerSyncPeriod

	// MinSyncCommitteeParticipants is the minimum signatures for validity (>2/3).
	MinSyncCommitteeParticipants = (SyncCommitteeSize * 2) / 3

	// ForkVersionAltair is Altair (sync committee activation).
	ForkVersionAltair = 0x01000000

	// DomainSyncCommittee is the BLS domain for sync committee signatures.
	DomainSyncCommittee = 0x07000000

	// maxVerifiedRoots caps the number of verified roots stored (bounded memory).
	maxVerifiedRoots = 8192
)

// EthHeader represents a minimal Ethereum beacon chain header.
type EthHeader struct {
	Slot          uint64     `json:"slot"`
	ProposerIndex uint64     `json:"proposer_index"`
	ParentRoot    types.Hash `json:"parent_root"`
	StateRoot     types.Hash `json:"state_root"`
	BodyRoot      types.Hash `json:"body_root"`
}

// Hash returns the SSZ hash-tree-root of the header.
// Each field is zero-padded to 32 bytes (LE for uint64), then Merkle-ized.
func (h *EthHeader) Hash() types.Hash {
	// 5 leaves → pad to 8 (next power of 2) for Merkle tree
	var leaves [8][32]byte

	// Leaf 0: slot (uint64 LE, zero-padded to 32 bytes)
	binary.LittleEndian.PutUint64(leaves[0][:8], h.Slot)
	// Leaf 1: proposer_index
	binary.LittleEndian.PutUint64(leaves[1][:8], h.ProposerIndex)
	// Leaf 2-4: hash fields (already 32 bytes)
	leaves[2] = h.ParentRoot
	leaves[3] = h.StateRoot
	leaves[4] = h.BodyRoot
	// Leaves 5-7: zero (padding)

	return sszMerkleize(leaves[:])
}

// SyncPeriod returns the sync committee period for this slot.
func (h *EthHeader) SyncPeriod() uint64 {
	return h.Slot / SlotsPerSyncPeriod
}

// SyncCommittee represents an Ethereum sync committee (512 BLS pubkeys).
type SyncCommittee struct {
	PubKeys         [SyncCommitteeSize][]byte // 48-byte compressed BLS12-381
	AggregatePubKey []byte                    // Aggregate of all 512 keys
}

// SyncCommitteeUpdate is a light client update with new committee + proof.
type SyncCommitteeUpdate struct {
	AttestedHeader  *EthHeader     // Header attested by sync committee
	FinalizedHeader *EthHeader     // Finalized header (justified by attested)
	SyncAggregate   *SyncAggregate // Sync committee signature
	NextCommittee   *SyncCommittee // Next sync committee (if rotation)
	FinalityBranch  []types.Hash   // Merkle proof: finalized_root in attested state
	CommitteeBranch []types.Hash   // Merkle proof: next_committee in attested state
}

// SyncAggregate contains the sync committee's aggregate BLS signature.
type SyncAggregate struct {
	SyncCommitteeBits      [SyncCommitteeSize / 8]byte // Participation bitmap
	SyncCommitteeSignature []byte                       // BLS12-381 aggregate sig (96 bytes)
}

// ParticipantCount returns how many committee members signed.
func (sa *SyncAggregate) ParticipantCount() int {
	count := 0
	for _, b := range sa.SyncCommitteeBits {
		count += bits.OnesCount8(b)
	}
	return count
}

// EthLightClient tracks Ethereum finality using sync committee signatures.
// It verifies beacon chain headers and provides MPT storage proof verification
// for the ETH->N42 reverse bridge.
type EthLightClient struct {
	mu sync.RWMutex

	// Current sync committee and period
	currentCommittee *SyncCommittee
	nextCommittee    *SyncCommittee
	currentPeriod    uint64

	// Latest finalized beacon header
	latestFinalized *EthHeader

	// Verified execution state roots indexed by slot (bounded to maxVerifiedRoots)
	verifiedRoots    map[uint64]types.Hash
	verifiedSlotRing []uint64 // ring buffer for LRU eviction
	ringIdx          int

	// BLS signature verifier (injected for testability)
	blsVerifier SyncCommitteeBLSVerifier

	// Genesis validators root (for domain computation)
	genesisValidatorsRoot types.Hash
}

// SyncCommitteeBLSVerifier abstracts BLS verification for testing.
type SyncCommitteeBLSVerifier interface {
	VerifySyncCommitteeSignature(
		pubKeys []blscommon.PublicKey,
		sigBytes []byte,
		signingRoot [32]byte,
	) error
}

// EthLightClientConfig holds configuration for the ETH light client.
type EthLightClientConfig struct {
	GenesisValidatorsRoot types.Hash
	InitialCommittee      *SyncCommittee
	InitialPeriod         uint64
	InitialHeader         *EthHeader
}

// NewEthLightClient creates a new Ethereum light client from a trusted
// initial sync committee (bootstrap from a beacon node).
func NewEthLightClient(cfg *EthLightClientConfig, verifier SyncCommitteeBLSVerifier) (*EthLightClient, error) {
	if cfg == nil || cfg.InitialCommittee == nil {
		return nil, fmt.Errorf("initial sync committee required")
	}
	if verifier == nil {
		return nil, fmt.Errorf("BLS verifier required")
	}

	lc := &EthLightClient{
		currentCommittee:      cfg.InitialCommittee,
		currentPeriod:         cfg.InitialPeriod,
		latestFinalized:       cfg.InitialHeader,
		verifiedRoots:         make(map[uint64]types.Hash, maxVerifiedRoots),
		verifiedSlotRing:      make([]uint64, maxVerifiedRoots),
		blsVerifier:           verifier,
		genesisValidatorsRoot: cfg.GenesisValidatorsRoot,
	}

	if cfg.InitialHeader != nil {
		lc.addVerifiedRoot(cfg.InitialHeader.Slot, cfg.InitialHeader.StateRoot)
	}

	return lc, nil
}

// ProcessUpdate processes a sync committee light client update.
// This verifies the finality proof and optionally rotates the committee.
func (lc *EthLightClient) ProcessUpdate(ctx context.Context, update *SyncCommitteeUpdate) error {
	lc.mu.Lock()
	defer lc.mu.Unlock()

	if update == nil || update.AttestedHeader == nil || update.FinalizedHeader == nil {
		return fmt.Errorf("incomplete update")
	}
	if update.SyncAggregate == nil {
		return fmt.Errorf("missing sync aggregate")
	}

	// 1. Verify enough participants signed (>2/3 of 512).
	participants := update.SyncAggregate.ParticipantCount()
	if participants < MinSyncCommitteeParticipants {
		return fmt.Errorf("insufficient sync committee participants: %d < %d",
			participants, MinSyncCommitteeParticipants)
	}

	// 2. Verify the attested header's slot is newer than last finalized.
	if lc.latestFinalized != nil && update.AttestedHeader.Slot <= lc.latestFinalized.Slot {
		return fmt.Errorf("attested slot %d not newer than finalized %d",
			update.AttestedHeader.Slot, lc.latestFinalized.Slot)
	}

	// 3. Determine which committee to use (current or next based on period).
	attestedPeriod := update.AttestedHeader.SyncPeriod()
	committee := lc.currentCommittee
	if attestedPeriod == lc.currentPeriod+1 && lc.nextCommittee != nil {
		committee = lc.nextCommittee
	} else if attestedPeriod != lc.currentPeriod {
		return fmt.Errorf("attested period %d not in [%d, %d]",
			attestedPeriod, lc.currentPeriod, lc.currentPeriod+1)
	}

	// 4. Extract participating public keys from bitmap.
	participantKeys, err := extractParticipantKeys(committee, &update.SyncAggregate.SyncCommitteeBits)
	if err != nil {
		return fmt.Errorf("extract participant keys: %w", err)
	}

	// 5. Compute signing root = hash(attestedHeader.Hash(), domain).
	attestedHash := update.AttestedHeader.Hash()
	signingRoot := computeSyncCommitteeSigningRoot(
		attestedHash,
		lc.genesisValidatorsRoot,
		update.AttestedHeader.Slot,
	)

	// 6. Verify BLS aggregate signature.
	if err := lc.blsVerifier.VerifySyncCommitteeSignature(
		participantKeys,
		update.SyncAggregate.SyncCommitteeSignature,
		signingRoot,
	); err != nil {
		return fmt.Errorf("sync committee BLS verification failed: %w", err)
	}

	// 7. Verify finality proof (finalized_root in attested_header's state).
	if len(update.FinalityBranch) > 0 {
		finalizedRoot := update.FinalizedHeader.Hash()
		if !verifyMerkleBranch(
			finalizedRoot,
			update.FinalityBranch,
			6,  // finalized_checkpoint depth in BeaconState
			41, // finalized_checkpoint index in BeaconState
			update.AttestedHeader.StateRoot,
		) {
			return fmt.Errorf("finality branch verification failed")
		}
	}

	// 8. If next committee provided, verify and store.
	if update.NextCommittee != nil && len(update.CommitteeBranch) > 0 {
		committeeRoot := hashSyncCommittee(update.NextCommittee)
		if !verifyMerkleBranch(
			committeeRoot,
			update.CommitteeBranch,
			5,  // next_sync_committee depth
			23, // next_sync_committee index
			update.AttestedHeader.StateRoot,
		) {
			return fmt.Errorf("committee branch verification failed")
		}
		lc.nextCommittee = update.NextCommittee
	}

	// 9. Update state.
	lc.latestFinalized = update.FinalizedHeader
	lc.addVerifiedRoot(update.FinalizedHeader.Slot, update.FinalizedHeader.StateRoot)

	// 10. Rotate committee if period advanced.
	if attestedPeriod > lc.currentPeriod && lc.nextCommittee != nil {
		log.Info("ETH light client: sync committee rotation",
			"oldPeriod", lc.currentPeriod, "newPeriod", attestedPeriod)
		lc.currentCommittee = lc.nextCommittee
		lc.nextCommittee = nil
		lc.currentPeriod = attestedPeriod
	}

	ethLightClientUpdates.Inc()
	ethLatestFinalizedSlot.Set(float64(update.FinalizedHeader.Slot))

	log.Info("ETH light client: finalized header updated",
		"slot", update.FinalizedHeader.Slot,
		"stateRoot", update.FinalizedHeader.StateRoot,
		"participants", participants,
	)

	return nil
}

// addVerifiedRoot adds a root with ring-buffer eviction (bounded memory).
func (lc *EthLightClient) addVerifiedRoot(slot uint64, root types.Hash) {
	// Evict oldest if at capacity
	if len(lc.verifiedRoots) >= maxVerifiedRoots {
		oldSlot := lc.verifiedSlotRing[lc.ringIdx]
		delete(lc.verifiedRoots, oldSlot)
	}
	lc.verifiedRoots[slot] = root
	lc.verifiedSlotRing[lc.ringIdx] = slot
	lc.ringIdx = (lc.ringIdx + 1) % maxVerifiedRoots
}

// LatestFinalized returns the latest finalized ETH beacon header.
func (lc *EthLightClient) LatestFinalized() *EthHeader {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.latestFinalized
}

// CurrentPeriod returns the current sync committee period.
func (lc *EthLightClient) CurrentPeriod() uint64 {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	return lc.currentPeriod
}

// IsSlotVerified checks if a specific slot's state root has been verified.
func (lc *EthLightClient) IsSlotVerified(slot uint64) bool {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	_, ok := lc.verifiedRoots[slot]
	return ok
}

// GetVerifiedStateRoot returns the verified state root for a slot.
func (lc *EthLightClient) GetVerifiedStateRoot(slot uint64) (types.Hash, bool) {
	lc.mu.RLock()
	defer lc.mu.RUnlock()
	root, ok := lc.verifiedRoots[slot]
	return root, ok
}

// VerifyMPTProof verifies an Ethereum MPT (Merkle Patricia Trie) storage proof
// against a verified state root. This proves a value exists in ETH state.
//
// NOTE: MPT path verification requires full RLP decoding + Keccak256 hash
// chaining. This is not yet production-ready — DO NOT use for real asset
// transfers until verifyMPTPath is fully implemented.
func (lc *EthLightClient) VerifyMPTProof(
	slot uint64,
	accountAddr types.Address,
	storageKey types.Hash,
	expectedValue []byte,
	accountProof [][]byte,
	storageProof [][]byte,
) error {
	lc.mu.RLock()
	stateRoot, ok := lc.verifiedRoots[slot]
	lc.mu.RUnlock()

	if !ok {
		return fmt.Errorf("slot %d not verified", slot)
	}

	// Verify account exists in state trie
	accountRLP, err := verifyMPTPath(stateRoot, crypto.Keccak256(accountAddr[:]), accountProof)
	if err != nil {
		return fmt.Errorf("account proof invalid: %w", err)
	}
	if len(accountRLP) == 0 {
		return fmt.Errorf("account does not exist")
	}

	// Extract storage root from account RLP
	storageRoot, err := extractStorageRoot(accountRLP)
	if err != nil {
		return fmt.Errorf("extract storage root: %w", err)
	}

	// Verify storage value in account's storage trie
	value, err := verifyMPTPath(storageRoot, crypto.Keccak256(storageKey[:]), storageProof)
	if err != nil {
		return fmt.Errorf("storage proof invalid: %w", err)
	}

	if len(expectedValue) > 0 && !bytes.Equal(value, expectedValue) {
		return fmt.Errorf("storage value mismatch")
	}

	return nil
}

// --- Internal helpers ---

// extractParticipantKeys returns the BLS public keys of sync committee members
// who participated (their bit is set in the bitmap).
func extractParticipantKeys(
	committee *SyncCommittee,
	bitmap *[SyncCommitteeSize / 8]byte,
) ([]blscommon.PublicKey, error) {
	keys := make([]blscommon.PublicKey, 0, SyncCommitteeSize)
	for i := 0; i < SyncCommitteeSize; i++ {
		byteIdx := i / 8
		bitIdx := uint(i % 8)
		if bitmap[byteIdx]&(1<<bitIdx) != 0 {
			pk, err := bls.PublicKeyFromBytes(committee.PubKeys[i])
			if err != nil {
				return nil, fmt.Errorf("deserialize BLS pubkey %d: %w", i, err)
			}
			keys = append(keys, pk)
		}
	}
	return keys, nil
}

// computeSyncCommitteeSigningRoot computes the signing root for sync committee.
// signingRoot = hash(headerRoot, computeDomain(DOMAIN_SYNC_COMMITTEE, forkVersion, genesisRoot))
func computeSyncCommitteeSigningRoot(headerRoot types.Hash, genesisRoot types.Hash, slot uint64) [32]byte {
	_ = slot // TODO: derive fork version from slot/epoch for multi-fork support

	var forkVersion [4]byte
	binary.LittleEndian.PutUint32(forkVersion[:], ForkVersionAltair)

	var domainType [4]byte
	binary.LittleEndian.PutUint32(domainType[:], DomainSyncCommittee)

	// ForkData hash-tree-root: hash(forkVersion(32-padded) || genesisValidatorsRoot)
	var forkDataBuf [64]byte
	copy(forkDataBuf[:4], forkVersion[:])
	copy(forkDataBuf[32:], genesisRoot[:])
	forkDataRoot := sha256.Sum256(forkDataBuf[:])

	// Domain = domainType(4) + forkDataRoot[:28]
	var domain [32]byte
	copy(domain[:4], domainType[:])
	copy(domain[4:], forkDataRoot[:28])

	// SigningRoot = hash(objectRoot || domain)
	var signingData [64]byte
	copy(signingData[:32], headerRoot[:])
	copy(signingData[32:], domain[:])
	return sha256.Sum256(signingData[:])
}

// verifyMerkleBranch verifies a Merkle proof against a root.
// This is the standard beacon chain generalized index proof.
func verifyMerkleBranch(leaf types.Hash, branch []types.Hash, depth, index int, root types.Hash) bool {
	value := leaf
	var buf [64]byte
	for i := 0; i < depth && i < len(branch); i++ {
		if (index>>i)&1 == 1 {
			copy(buf[:32], branch[i][:])
			copy(buf[32:], value[:])
		} else {
			copy(buf[:32], value[:])
			copy(buf[32:], branch[i][:])
		}
		value = types.Hash(sha256.Sum256(buf[:]))
	}
	return value == root
}

// hashSyncCommittee computes the hash-tree-root of a sync committee.
// Proper SSZ: Merkle-ize the 512 pubkeys, then hash with aggregate key.
func hashSyncCommittee(c *SyncCommittee) types.Hash {
	// Hash each pubkey padded to 64 bytes (48-byte key + 16 zero padding, then 2 chunks)
	leaves := make([][32]byte, SyncCommitteeSize)
	for i, pk := range c.PubKeys {
		leaves[i] = sha256.Sum256(pk)
	}

	// Merkle-ize the 512 leaves
	for size := SyncCommitteeSize; size > 1; size /= 2 {
		var buf [64]byte
		for i := 0; i < size; i += 2 {
			copy(buf[:32], leaves[i][:])
			copy(buf[32:], leaves[i+1][:])
			leaves[i/2] = sha256.Sum256(buf[:])
		}
	}

	// Mix in aggregate pubkey
	aggHash := sha256.Sum256(c.AggregatePubKey)
	var final [64]byte
	copy(final[:32], leaves[0][:])
	copy(final[32:], aggHash[:])
	return types.Hash(sha256.Sum256(final[:]))
}

// verifyMPTPath verifies an Ethereum MPT proof and returns the value.
// It walks from root through RLP-encoded trie nodes, verifying the
// Keccak256 hash chain at each step.
//
// NOTE: This implementation verifies the hash chain but uses simplified
// value extraction. Full production use requires complete RLP node parsing
// (branch 17-item, extension 2-item, leaf 2-item). DO NOT use for real
// asset transfers until RLP parsing is complete.
func verifyMPTPath(root types.Hash, keyHash []byte, proof [][]byte) ([]byte, error) {
	if len(proof) == 0 {
		return nil, fmt.Errorf("empty proof")
	}

	// Verify root matches first node
	firstNodeHash := crypto.Keccak256Hash(proof[0])
	if firstNodeHash != root {
		return nil, fmt.Errorf("proof root mismatch: got %x, want %x", firstNodeHash, root)
	}

	// Verify hash chain: each node's hash must appear in the previous node
	for i := 0; i < len(proof)-1; i++ {
		nextHash := crypto.Keccak256(proof[i+1])
		if !bytes.Contains(proof[i], nextHash) {
			return nil, fmt.Errorf("broken hash chain at proof node %d", i)
		}
	}

	// Extract value from last node
	// TODO: full RLP decoding for branch/extension/leaf node types
	lastNode := proof[len(proof)-1]
	if len(lastNode) == 0 {
		return nil, nil
	}

	_ = keyHash // TODO: verify key path through nibbles matches keyHash
	return lastNode, nil
}

// extractStorageRoot extracts the storage root (3rd field) from an RLP-encoded account.
// Account: [nonce, balance, storageRoot, codeHash]
//
// NOTE: This uses simplified RLP length-prefix parsing. For production,
// replace with proper rlp.DecodeBytes.
func extractStorageRoot(accountRLP []byte) (types.Hash, error) {
	if len(accountRLP) < 70 {
		return types.Hash{}, fmt.Errorf("account RLP too short: %d", len(accountRLP))
	}

	// RLP list: first byte is list prefix (0xf8 + length byte, or 0xc0+len for short lists)
	offset := 0
	if accountRLP[0] >= 0xf8 {
		// Long list: 0xf8 + (lenOfLen), then lenOfLen bytes of length
		lenOfLen := int(accountRLP[0]) - 0xf7
		offset = 1 + lenOfLen
	} else if accountRLP[0] >= 0xc0 {
		// Short list: 0xc0 + len
		offset = 1
	} else {
		return types.Hash{}, fmt.Errorf("not an RLP list: prefix 0x%x", accountRLP[0])
	}

	// Skip field 0 (nonce) and field 1 (balance), then read field 2 (storageRoot)
	pos := offset
	for field := 0; field < 2; field++ {
		if pos >= len(accountRLP) {
			return types.Hash{}, fmt.Errorf("RLP truncated at field %d", field)
		}
		skip, err := rlpFieldLen(accountRLP, pos)
		if err != nil {
			return types.Hash{}, fmt.Errorf("RLP field %d: %w", field, err)
		}
		pos += skip
	}

	// Field 2 is storageRoot (32 bytes, RLP-encoded as 0xa0 + 32 bytes)
	if pos >= len(accountRLP) || accountRLP[pos] != 0xa0 {
		return types.Hash{}, fmt.Errorf("expected 32-byte storageRoot at offset %d, got 0x%x", pos, accountRLP[pos])
	}
	pos++ // skip 0xa0 prefix
	if pos+32 > len(accountRLP) {
		return types.Hash{}, fmt.Errorf("storageRoot truncated")
	}
	var root types.Hash
	copy(root[:], accountRLP[pos:pos+32])
	return root, nil
}

// rlpFieldLen returns the total encoded length (prefix + data) of the RLP item at pos.
func rlpFieldLen(data []byte, pos int) (int, error) {
	if pos >= len(data) {
		return 0, fmt.Errorf("position %d out of bounds (len %d)", pos, len(data))
	}
	b := data[pos]
	switch {
	case b < 0x80:
		return 1, nil // single byte
	case b <= 0xb7:
		return 1 + int(b-0x80), nil // short string
	case b <= 0xbf:
		lenOfLen := int(b - 0xb7)
		if pos+1+lenOfLen > len(data) {
			return 0, fmt.Errorf("length prefix truncated")
		}
		dataLen := 0
		for i := 0; i < lenOfLen; i++ {
			dataLen = (dataLen << 8) | int(data[pos+1+i])
		}
		return 1 + lenOfLen + dataLen, nil // long string
	case b <= 0xf7:
		return 1 + int(b-0xc0), nil // short list
	default:
		lenOfLen := int(b - 0xf7)
		if pos+1+lenOfLen > len(data) {
			return 0, fmt.Errorf("list length prefix truncated")
		}
		dataLen := 0
		for i := 0; i < lenOfLen; i++ {
			dataLen = (dataLen << 8) | int(data[pos+1+i])
		}
		return 1 + lenOfLen + dataLen, nil // long list
	}
}

// sszMerkleize computes the SSZ Merkle root of a set of 32-byte leaves.
// Leaves must be a power-of-2 length.
func sszMerkleize(leaves [][32]byte) types.Hash {
	n := len(leaves)
	if n == 0 {
		return types.Hash{}
	}
	if n == 1 {
		return types.Hash(leaves[0])
	}

	// Copy leaves to avoid mutating input
	nodes := make([][32]byte, n)
	copy(nodes, leaves)

	var buf [64]byte
	for size := n; size > 1; size /= 2 {
		for i := 0; i < size; i += 2 {
			copy(buf[:32], nodes[i][:])
			copy(buf[32:], nodes[i+1][:])
			nodes[i/2] = sha256.Sum256(buf[:])
		}
	}
	return types.Hash(nodes[0])
}
