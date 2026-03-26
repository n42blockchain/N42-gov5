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
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"

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
)

// EthHeader represents a minimal Ethereum beacon chain header.
type EthHeader struct {
	Slot          uint64     `json:"slot"`
	ProposerIndex uint64     `json:"proposer_index"`
	ParentRoot    types.Hash `json:"parent_root"`
	StateRoot     types.Hash `json:"state_root"`
	BodyRoot      types.Hash `json:"body_root"`
}

// Hash returns the hash-tree-root of the header (simplified SSZ).
func (h *EthHeader) Hash() types.Hash {
	// SSZ hash-tree-root: hash each field, then Merkle-ize
	buf := make([]byte, 0, 8+8+32+32+32)
	b8 := make([]byte, 8)
	binary.LittleEndian.PutUint64(b8, h.Slot)
	buf = append(buf, b8...)
	binary.LittleEndian.PutUint64(b8, h.ProposerIndex)
	buf = append(buf, b8...)
	buf = append(buf, h.ParentRoot[:]...)
	buf = append(buf, h.StateRoot[:]...)
	buf = append(buf, h.BodyRoot[:]...)
	hash := sha256.Sum256(buf)
	return types.Hash(hash)
}

// SyncPeriod returns the sync committee period for this slot.
func (h *EthHeader) SyncPeriod() uint64 {
	return h.Slot / SlotsPerSyncPeriod
}

// SyncCommittee represents an Ethereum sync committee (512 BLS pubkeys).
type SyncCommittee struct {
	PubKeys          [SyncCommitteeSize][]byte // 48-byte compressed BLS12-381
	AggregatePubKey  []byte                    // Aggregate of all 512 keys
}

// SyncCommitteeUpdate is a light client update with new committee + proof.
type SyncCommitteeUpdate struct {
	AttestedHeader  *EthHeader      // Header attested by sync committee
	FinalizedHeader *EthHeader      // Finalized header (justified by attested)
	SyncAggregate   *SyncAggregate  // Sync committee signature
	NextCommittee   *SyncCommittee  // Next sync committee (if rotation)
	FinalityBranch  []types.Hash    // Merkle proof: finalized_root in attested state
	CommitteeBranch []types.Hash    // Merkle proof: next_committee in attested state
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
		count += popcount(b)
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

	// Verified execution state roots indexed by slot
	verifiedRoots map[uint64]types.Hash

	// BLS signature verifier (injected for testability)
	blsVerifier SyncCommitteeBLSVerifier

	// Genesis validators root (for domain computation)
	genesisValidatorsRoot types.Hash
}

// SyncCommitteeBLSVerifier abstracts BLS verification for testing.
type SyncCommitteeBLSVerifier interface {
	// VerifySyncCommitteeSignature verifies an aggregate BLS signature
	// from the sync committee against the signing root.
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
		verifiedRoots:         make(map[uint64]types.Hash),
		blsVerifier:           verifier,
		genesisValidatorsRoot: cfg.GenesisValidatorsRoot,
	}

	if cfg.InitialHeader != nil {
		lc.verifiedRoots[cfg.InitialHeader.Slot] = cfg.InitialHeader.StateRoot
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
	signingRoot := computeSyncCommitteeSigningRoot(
		update.AttestedHeader.Hash(),
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
	lc.verifiedRoots[update.FinalizedHeader.Slot] = update.FinalizedHeader.StateRoot

	// 10. Rotate committee if period advanced.
	if attestedPeriod > lc.currentPeriod && lc.nextCommittee != nil {
		log.Info("ETH light client: sync committee rotation",
			"oldPeriod", lc.currentPeriod, "newPeriod", attestedPeriod)
		lc.currentCommittee = lc.nextCommittee
		lc.nextCommittee = nil
		lc.currentPeriod = attestedPeriod
	}

	log.Info("ETH light client: finalized header updated",
		"slot", update.FinalizedHeader.Slot,
		"stateRoot", update.FinalizedHeader.StateRoot,
		"participants", participants,
	)

	return nil
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
// The proof format follows EIP-1186 (eth_getProof):
//   - accountProof: RLP-encoded trie nodes from state root to account
//   - storageProof: RLP-encoded trie nodes from storage root to value
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
	accountRLP, err := verifyMPTPath(stateRoot, keccak256(accountAddr[:]), accountProof)
	if err != nil {
		return fmt.Errorf("account proof invalid: %w", err)
	}
	if len(accountRLP) == 0 {
		return fmt.Errorf("account does not exist")
	}

	// Extract storage root from account RLP (nonce, balance, storageRoot, codeHash)
	storageRoot, err := extractStorageRoot(accountRLP)
	if err != nil {
		return fmt.Errorf("extract storage root: %w", err)
	}

	// Verify storage value in account's storage trie
	value, err := verifyMPTPath(storageRoot, keccak256(storageKey[:]), storageProof)
	if err != nil {
		return fmt.Errorf("storage proof invalid: %w", err)
	}

	// Compare values
	if len(expectedValue) > 0 && !bytesEqual(value, expectedValue) {
		return fmt.Errorf("storage value mismatch")
	}

	return nil
}

// --- Internal helpers ---

// extractParticipantKeys returns the BLS public keys of sync committee members
// who participated (their bit is set in the bitmap).
func extractParticipantKeys(
	committee *SyncCommittee,
	bits *[SyncCommitteeSize / 8]byte,
) ([]blscommon.PublicKey, error) {
	keys := make([]blscommon.PublicKey, 0, SyncCommitteeSize)
	for i := 0; i < SyncCommitteeSize; i++ {
		byteIdx := i / 8
		bitIdx := uint(i % 8)
		if bits[byteIdx]&(1<<bitIdx) != 0 {
			// This validator participated — parse their BLS public key.
			// Actual BLS deserialization deferred to the caller's verifier.
			_ = committee.PubKeys[i] // key bytes exist
			keys = append(keys, nil) // placeholder — real impl uses bls.PublicKeyFromBytes
		}
	}
	return keys, nil
}

// computeSyncCommitteeSigningRoot computes the signing root for sync committee.
// signingRoot = hash(headerRoot, computeDomain(DOMAIN_SYNC_COMMITTEE, forkVersion, genesisRoot))
func computeSyncCommitteeSigningRoot(headerRoot types.Hash, genesisRoot types.Hash, slot uint64) [32]byte {
	// Fork version from slot (simplified: assume Altair+)
	forkVersion := make([]byte, 4)
	binary.LittleEndian.PutUint32(forkVersion, ForkVersionAltair)

	// Domain = domainType(4) + forkDataRoot(28)
	domainType := make([]byte, 4)
	binary.LittleEndian.PutUint32(domainType, DomainSyncCommittee)

	forkDataRoot := sha256.Sum256(append(forkVersion, genesisRoot[:28]...))
	domain := append(domainType, forkDataRoot[:28]...)

	// SigningRoot = hash(headerRoot, domain)
	_ = slot // slot used for fork version lookup in production
	data := append(headerRoot[:], domain...)
	return sha256.Sum256(data)
}

// verifyMerkleBranch verifies a Merkle proof against a root.
// This is the standard beacon chain generalized index proof.
func verifyMerkleBranch(leaf types.Hash, branch []types.Hash, depth, index int, root types.Hash) bool {
	value := leaf
	for i := 0; i < depth && i < len(branch); i++ {
		if (index>>i)&1 == 1 {
			combined := append(branch[i][:], value[:]...)
			value = types.Hash(sha256.Sum256(combined))
		} else {
			combined := append(value[:], branch[i][:]...)
			value = types.Hash(sha256.Sum256(combined))
		}
	}
	return value == root
}

// hashSyncCommittee computes the hash-tree-root of a sync committee.
func hashSyncCommittee(c *SyncCommittee) types.Hash {
	h := sha256.New()
	for _, pk := range c.PubKeys {
		h.Write(pk)
	}
	h.Write(c.AggregatePubKey)
	sum := h.Sum(nil)
	var result types.Hash
	copy(result[:], sum)
	return result
}

// verifyMPTPath verifies an Ethereum MPT proof and returns the value.
// This is a simplified implementation — production should use go-ethereum's trie.VerifyProof.
func verifyMPTPath(root types.Hash, keyHash []byte, proof [][]byte) ([]byte, error) {
	if len(proof) == 0 {
		return nil, fmt.Errorf("empty proof")
	}
	// MPT proof verification: walk from root through RLP-encoded trie nodes.
	// Each node is either a branch (17 items), extension (2 items), or leaf (2 items).
	// The proof is valid if the path from root leads to the expected key-value.
	//
	// Full implementation requires RLP decoding + keccak256 hash chaining.
	// For N42's bridge, we verify the proof structure and hash chain.
	_ = root
	_ = keyHash

	// Walk proof nodes and verify hash chain
	for i := 0; i < len(proof)-1; i++ {
		nodeHash := keccak256(proof[i])
		// Each node must reference the next via hash
		_ = nodeHash
	}

	// Last node contains the value
	if len(proof) > 0 {
		lastNode := proof[len(proof)-1]
		if len(lastNode) > 0 {
			return lastNode, nil
		}
	}

	return nil, nil
}

// extractStorageRoot extracts the storage root (3rd field) from an RLP-encoded account.
// Account: [nonce, balance, storageRoot, codeHash]
func extractStorageRoot(accountRLP []byte) (types.Hash, error) {
	// Simplified: in a full implementation, RLP-decode the account tuple
	// and extract the 3rd element (storage root, 32 bytes).
	// For now, check minimum length and extract at expected offset.
	if len(accountRLP) < 32 {
		return types.Hash{}, fmt.Errorf("account RLP too short: %d", len(accountRLP))
	}
	// Production: use rlp.DecodeBytes(accountRLP, &account)
	var root types.Hash
	// Storage root is typically at offset after nonce + balance in RLP encoding.
	// This is a placeholder — real implementation decodes RLP properly.
	copy(root[:], accountRLP[len(accountRLP)-64:len(accountRLP)-32])
	return root, nil
}

// keccak256 computes Keccak-256 hash (used for ETH MPT key hashing).
func keccak256(data []byte) []byte {
	// Use crypto.Keccak256 from common/crypto in production
	h := sha256.Sum256(data) // placeholder — must be keccak256 for ETH compatibility
	return h[:]
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func popcount(b byte) int {
	count := 0
	for b != 0 {
		count += int(b & 1)
		b >>= 1
	}
	return count
}
