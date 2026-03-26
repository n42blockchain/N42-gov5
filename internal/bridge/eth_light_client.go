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
	"github.com/n42blockchain/N42/common/rlp"
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

	// Ethereum fork versions (for BLS domain separation).
	ForkVersionAltair    = 0x01000000
	ForkVersionBellatrix = 0x02000000
	ForkVersionCapella   = 0x03000000
	ForkVersionDeneb     = 0x04000000
	ForkVersionElectra   = 0x05000000

	// Fork activation epochs (mainnet).
	EpochAltair    uint64 = 74240
	EpochBellatrix uint64 = 144896
	EpochCapella   uint64 = 194048
	EpochDeneb     uint64 = 269568
	EpochElectra   uint64 = 364544 // Pectra, May 2025

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
	var leaves [8][32]byte // 5 fields padded to next power-of-2 for SSZ
	binary.LittleEndian.PutUint64(leaves[0][:8], h.Slot)
	binary.LittleEndian.PutUint64(leaves[1][:8], h.ProposerIndex)
	leaves[2] = h.ParentRoot
	leaves[3] = h.StateRoot
	leaves[4] = h.BodyRoot

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
	// Finality branch is REQUIRED — without it, there is no cryptographic link
	// between the attested header (signed by sync committee) and the finalized header.
	if len(update.FinalityBranch) == 0 {
		return fmt.Errorf("finality branch required")
	}
	{
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
	// Skip ring advancement if slot already exists (avoid wasting ring slots on duplicates).
	if _, exists := lc.verifiedRoots[slot]; exists {
		lc.verifiedRoots[slot] = root
		return
	}
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
	var forkVersion [4]byte
	binary.LittleEndian.PutUint32(forkVersion[:], forkVersionForSlot(slot))

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
	if len(branch) != depth {
		return false
	}
	value := leaf
	var buf [64]byte
	for i := 0; i < depth; i++ {
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

// forkVersionForSlot returns the ETH fork version for a given slot.
// Sync committee BLS domains use different fork versions per epoch.
func forkVersionForSlot(slot uint64) uint32 {
	epoch := slot / SlotsPerEpoch
	switch {
	case epoch >= EpochElectra:
		return ForkVersionElectra
	case epoch >= EpochDeneb:
		return ForkVersionDeneb
	case epoch >= EpochCapella:
		return ForkVersionCapella
	case epoch >= EpochBellatrix:
		return ForkVersionBellatrix
	default:
		return ForkVersionAltair
	}
}

// hashSyncCommittee computes the SSZ hash-tree-root of a sync committee.
// Each 48-byte BLS pubkey is SSZ-serialized as 64 bytes (48 + 16 zero padding),
// split into two 32-byte chunks, then hash-tree-rooted per pubkey.
func hashSyncCommittee(c *SyncCommittee) types.Hash {
	leaves := make([][32]byte, SyncCommitteeSize)
	for i, pk := range c.PubKeys {
		// SSZ: pad 48-byte pubkey to 64 bytes, split into 2 chunks, hash pair
		var padded [64]byte
		copy(padded[:], pk) // 48 bytes + 16 zero padding
		var buf [64]byte
		copy(buf[:32], padded[:32])
		copy(buf[32:], padded[32:64])
		leaves[i] = sha256.Sum256(buf[:])
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

// verifyMPTPath verifies an Ethereum MPT proof against a state root.
// Walks from root through RLP-encoded trie nodes (branch/extension/leaf),
// verifying the Keccak256 hash chain and nibble path at each step.
func verifyMPTPath(root types.Hash, keyHash []byte, proof [][]byte) ([]byte, error) {
	if len(proof) == 0 {
		return nil, fmt.Errorf("empty proof")
	}

	// Verify root matches first node
	firstNodeHash := crypto.Keccak256Hash(proof[0])
	if firstNodeHash != root {
		return nil, fmt.Errorf("proof root mismatch: got %x, want %x", firstNodeHash, root)
	}

	// Convert key hash to nibbles (each byte → 2 nibbles)
	nibbles := make([]byte, len(keyHash)*2)
	for i, b := range keyHash {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0f
	}
	nibblePos := 0

	// Walk through proof nodes
	for i, node := range proof {
		// Decode the RLP list (trie node)
		content, _, err := rlp.SplitList(node)
		if err != nil {
			return nil, fmt.Errorf("node %d: not an RLP list: %w", i, err)
		}

		// Count elements to determine node type
		elements, err := rlpListElements(content)
		if err != nil {
			return nil, fmt.Errorf("node %d: parse elements: %w", i, err)
		}

		switch len(elements) {
		case 17:
			// Branch node: 16 children + value
			if i == len(proof)-1 {
				// Last node — value is in elements[16]
				if len(elements[16]) == 0 {
					return nil, nil // key not found
				}
				valBytes, _, err := rlp.SplitString(elements[16])
				if err != nil {
					return nil, fmt.Errorf("branch value: %w", err)
				}
				return valBytes, nil
			}
			// Not last node — follow the nibble path to next child
			if nibblePos >= len(nibbles) {
				return nil, fmt.Errorf("node %d: nibble path exhausted at branch", i)
			}
			childIdx := nibbles[nibblePos]
			nibblePos++
			if childIdx >= 16 {
				return nil, fmt.Errorf("node %d: invalid nibble %d", i, childIdx)
			}
			// Verify child hash matches next proof node
			if i+1 < len(proof) {
				childHash := crypto.Keccak256(proof[i+1])
				if !bytes.Contains(elements[childIdx], childHash) {
					return nil, fmt.Errorf("node %d: branch child %d hash mismatch", i, childIdx)
				}
			}

		case 2:
			// Extension or Leaf node — decode the compact-encoded path
			pathBytes, _, err := rlp.SplitString(elements[0])
			if err != nil {
				return nil, fmt.Errorf("node %d: decode path: %w", i, err)
			}
			if len(pathBytes) == 0 {
				return nil, fmt.Errorf("node %d: empty path", i)
			}

			// Compact encoding: first nibble flags (0=ext-even, 1=ext-odd, 2=leaf-even, 3=leaf-odd)
			firstNibble := pathBytes[0] >> 4
			isLeaf := firstNibble >= 2
			isOdd := firstNibble%2 == 1

			// Extract path nibbles
			var pathNibbles []byte
			if isOdd {
				pathNibbles = append(pathNibbles, pathBytes[0]&0x0f)
			}
			for _, b := range pathBytes[1:] {
				pathNibbles = append(pathNibbles, b>>4, b&0x0f)
			}

			// Verify path nibbles match key nibbles at current position
			if nibblePos+len(pathNibbles) > len(nibbles) {
				return nil, fmt.Errorf("node %d: path exceeds key length", i)
			}
			for j, pn := range pathNibbles {
				if nibbles[nibblePos+j] != pn {
					return nil, fmt.Errorf("node %d: nibble mismatch at position %d", i, nibblePos+j)
				}
			}
			nibblePos += len(pathNibbles)

			if isLeaf {
				// Leaf node — elements[1] is the value
				valBytes, _, err := rlp.SplitString(elements[1])
				if err != nil {
					return nil, fmt.Errorf("leaf value: %w", err)
				}
				return valBytes, nil
			}
			// Extension node — verify child hash
			if i+1 < len(proof) {
				childHash := crypto.Keccak256(proof[i+1])
				if !bytes.Contains(elements[1], childHash) {
					return nil, fmt.Errorf("node %d: extension child hash mismatch", i)
				}
			}

		default:
			return nil, fmt.Errorf("node %d: unexpected element count %d (expected 2 or 17)", i, len(elements))
		}
	}

	return nil, fmt.Errorf("proof walk completed without finding value")
}

// rlpListElements splits an RLP list's content into its raw element byte slices.
func rlpListElements(content []byte) ([][]byte, error) {
	var elements [][]byte
	rest := content
	for len(rest) > 0 {
		// Each call to rlp.Split returns (content, contentRest, restOfList, err)
		_, elemRest, err := rlp.SplitString(rest)
		if err != nil {
			// Try as list
			_, elemRest, err = rlp.SplitList(rest)
			if err != nil {
				return nil, fmt.Errorf("element parse error: %w", err)
			}
		}
		elemLen := len(rest) - len(elemRest)
		elements = append(elements, rest[:elemLen])
		rest = elemRest
	}
	return elements, nil
}

// extractStorageRoot extracts the storage root (3rd field) from an RLP-encoded account.
// Account: [nonce, balance, storageRoot, codeHash]
// Uses common/rlp.Split for canonical RLP decoding.
func extractStorageRoot(accountRLP []byte) (types.Hash, error) {
	// Decode the outer list
	content, _, err := rlp.SplitList(accountRLP)
	if err != nil {
		return types.Hash{}, fmt.Errorf("not an RLP list: %w", err)
	}

	// Skip field 0 (nonce) and field 1 (balance)
	rest := content
	for field := 0; field < 2; field++ {
		_, _, rest, err = rlp.Split(rest)
		if err != nil {
			return types.Hash{}, fmt.Errorf("RLP field %d: %w", field, err)
		}
	}

	// Field 2 is storageRoot (32-byte string)
	storageRootBytes, _, err := rlp.SplitString(rest)
	if err != nil {
		return types.Hash{}, fmt.Errorf("storageRoot: %w", err)
	}
	if len(storageRootBytes) != 32 {
		return types.Hash{}, fmt.Errorf("storageRoot wrong length: %d", len(storageRootBytes))
	}

	var root types.Hash
	copy(root[:], storageRootBytes)
	return root, nil
}

// sszMerkleize computes the SSZ Merkle root of a set of 32-byte leaves.
// Leaves must be a power-of-2 length. Operates in-place (mutates input).
func sszMerkleize(leaves [][32]byte) types.Hash {
	n := len(leaves)
	if n == 0 {
		return types.Hash{}
	}
	if n == 1 {
		return types.Hash(leaves[0])
	}

	var buf [64]byte
	for size := n; size > 1; size /= 2 {
		for i := 0; i < size; i += 2 {
			copy(buf[:32], leaves[i][:])
			copy(buf[32:], leaves[i+1][:])
			leaves[i/2] = sha256.Sum256(buf[:])
		}
	}
	return types.Hash(leaves[0])
}
