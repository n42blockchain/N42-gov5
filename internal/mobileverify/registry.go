// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Registry is the mobile membership accumulator (design §3, tier T3). It
// is NOT a loose in-memory map: registrations settle into an append-only
// committed set anchored by a BMT root (lib/bmt — the same Blake3 binary
// Merkle tree validated across 11.7M replay blocks), and that root is
// what an epoch batch commits on-chain. Anchoring on the accumulator is
// what makes the signer index globally canonical (append position in the
// committed set) instead of a per-node guess — the cross-node
// consistency the earlier map-with-gossip sketch could not guarantee.
//
// Two-layer membership, mempool-and-settled shaped:
//   - pending: PoP-verified registrations gossiped between IDC nodes but
//     not yet in the committed set; they cannot attest yet.
//   - committed: the append-only ordered set under the BMT root. A device
//     gets a stable MobileIndex (its append position) and may attest.
//     CommitEpoch folds the pending set in — sorted by key hash so every
//     node that commits the same batch derives identical indices and the
//     identical root.
//
// Consistency model (honest): within one node the accumulator is fully
// deterministic. Cross-node CANONICAL agreement on the root requires the
// epoch batch to be settled on-chain (the batch transaction lists the
// members, so a node that missed a gossiped registration still adopts the
// exact committed set) — that on-chain write is the anchoring seam
// (design §3, roadmap phase 6b). Until a batch is anchored, roots are
// per-node advisory and converge as gossip does.

package mobileverify

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/lib/bmt"
)

// MobileIndex is a device's stable append position in the committed set —
// the unit the sparse signer mask (mask.go) is built from. Append-only:
// once committed, an index never moves. uint32 covers four billion
// devices; the mask codec is width-agnostic.
type MobileIndex = uint32

// popTag domain-separates proof-of-possession signatures from receipt
// signatures: a PoP message is tag(24B) ‖ pubkey(48B), structurally
// disjoint from the 72-byte receipt signing message, and the explicit
// tag keeps that true even if the receipt format ever grows.
var popTag = []byte("n42/mobileverify/pop/v1\x00")

// PoPMessage is the payload a registrant must sign to prove possession
// of the secret key behind pubkey.
func PoPMessage(pubkey [48]byte) []byte {
	out := make([]byte, 0, len(popTag)+48)
	out = append(out, popTag...)
	out = append(out, pubkey[:]...)
	return out
}

var (
	// ErrBadPoP reports a registration whose proof-of-possession does not
	// verify. Without this check an attacker can register a rogue key and
	// forge cohort aggregates — see docs/mobile-attestation-design.md §3.
	ErrBadPoP = errors.New("mobileverify: invalid proof of possession")
	// ErrUnknownIndex reports a MobileIndex with no committed key.
	ErrUnknownIndex = errors.New("mobileverify: unknown mobile index")
)

// committedEntry pins a device's committed identity.
type committedEntry struct {
	pubkey [48]byte
	pk     common.PublicKey
	pop    [96]byte // retained so a batch commit can be re-announced on-chain
}

// Registry accumulates mobile memberships under a BMT root.
type Registry struct {
	mu sync.RWMutex

	// Committed set (append-only, index-addressed).
	committed []committedEntry
	byKey     map[[48]byte]MobileIndex

	// Pending set (PoP-verified, awaiting the next epoch commit).
	pending map[[48]byte][96]byte

	// Accumulator over committed keys: key = HashKey(pubkey), value =
	// index (BE8). Root() is the on-chain-anchorable commitment.
	tree *bmt.Tree

	// onRegister fires after a NEW pending registration (not a duplicate)
	// with the mapping, so the node can gossip it to the fleet.
	onRegister func(pubkey [48]byte, pop [96]byte)
}

// NewRegistry creates an empty accumulator-backed registry.
func NewRegistry() *Registry {
	return &Registry{
		byKey:   make(map[[48]byte]MobileIndex),
		pending: make(map[[48]byte][96]byte),
		tree:    bmt.New(bmt.NewMemStore()),
	}
}

// SetOnRegister installs the gossip hook, called after each new pending
// registration so the node can announce it to the fleet. Set once at startup.
func (r *Registry) SetOnRegister(fn func(pubkey [48]byte, pop [96]byte)) {
	r.mu.Lock()
	r.onRegister = fn
	r.mu.Unlock()
}

// verifyPoP checks a registration's proof of possession.
func verifyPoP(pubkey [48]byte, pop [96]byte) (common.PublicKey, error) {
	pk, err := bls.PublicKeyFromBytes(pubkey[:])
	if err != nil {
		return nil, fmt.Errorf("%w: pubkey decode: %v", ErrBadPoP, err)
	}
	sig, err := bls.SignatureFromBytes(pop[:])
	if err != nil {
		return nil, fmt.Errorf("%w: signature decode: %v", ErrBadPoP, err)
	}
	if !sig.Verify(pk, PoPMessage(pubkey)) {
		return nil, ErrBadPoP
	}
	return pk, nil
}

// Register verifies the PoP and admits the device to the PENDING set,
// returning whether it is already committed and, if so, its index.
// Idempotent: a committed or already-pending key is a no-op. New pending
// registrations fire the gossip hook. A device attests only once
// committed (CommitEpoch), so callers should treat committed=false as
// "registered, effective next epoch".
func (r *Registry) Register(pubkey [48]byte, pop [96]byte) (index MobileIndex, committed bool, err error) {
	if _, err := verifyPoP(pubkey, pop); err != nil {
		return 0, false, err
	}
	r.mu.Lock()
	if idx, ok := r.byKey[pubkey]; ok {
		r.mu.Unlock()
		return idx, true, nil // already committed
	}
	if _, ok := r.pending[pubkey]; ok {
		r.mu.Unlock()
		return 0, false, nil // already pending
	}
	r.pending[pubkey] = pop
	hook := r.onRegister
	r.mu.Unlock()

	if hook != nil {
		hook(pubkey, pop)
	}
	return 0, false, nil
}

// AdoptPending admits a registration learned from another IDC node's
// gossip announcement into the pending set (replication, design §3).
// Verifies the PoP — a replicated registration is no more trusted than a
// local one. Does not re-gossip (the sender already did). Returns whether
// it was newly added.
func (r *Registry) AdoptPending(pubkey [48]byte, pop [96]byte) (bool, error) {
	if _, err := verifyPoP(pubkey, pop); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byKey[pubkey]; ok {
		return false, nil
	}
	if _, ok := r.pending[pubkey]; ok {
		return false, nil
	}
	r.pending[pubkey] = pop
	return true, nil
}

// CommitEpoch folds the pending set into the committed set and advances
// the accumulator root. Pending keys are sorted by their BMT key hash so
// every node that commits the same pending set derives the identical
// index assignment and identical root. Returns the new root and the
// number of devices committed in this batch. The returned root is what an
// epoch batch anchors on-chain (design §3).
func (r *Registry) CommitEpoch() (root types.Hash, committedNow int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pending) == 0 {
		return r.tree.Root(), 0
	}
	// Deterministic order: by BMT key hash of the pubkey.
	type pk struct {
		pubkey  [48]byte
		keyHash bmt.Hash
		pop     [96]byte
	}
	batch := make([]pk, 0, len(r.pending))
	for pub, pop := range r.pending {
		batch = append(batch, pk{pubkey: pub, keyHash: bmt.HashKey(pub[:]), pop: pop})
	}
	sort.Slice(batch, func(i, j int) bool {
		return string(batch[i].keyHash[:]) < string(batch[j].keyHash[:])
	})
	for _, e := range batch {
		parsed, err := bls.PublicKeyFromBytes(e.pubkey[:])
		if err != nil {
			continue // PoP-verified at admission; unreachable
		}
		idx := MobileIndex(len(r.committed))
		r.committed = append(r.committed, committedEntry{pubkey: e.pubkey, pk: parsed, pop: e.pop})
		r.byKey[e.pubkey] = idx
		var v [8]byte
		binary.BigEndian.PutUint64(v[:], uint64(idx))
		_ = r.tree.Put(e.keyHash, v[:])
		committedNow++
	}
	r.pending = make(map[[48]byte][96]byte)
	return r.tree.Root(), committedNow
}

// Root returns the current committed accumulator root — the value an
// epoch batch anchors on-chain (design §3). EmptyHash when nothing is
// committed yet.
func (r *Registry) Root() types.Hash {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.tree.Root()
}

// MembershipProof returns a BMT inclusion proof for a committed device,
// verifiable against Root() by a light client without the full member
// list. Errors if the key is not committed.
func (r *Registry) MembershipProof(pubkey [48]byte) (*bmt.Proof, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.byKey[pubkey]; !ok {
		return nil, ErrUnknownIndex
	}
	return r.tree.GetProof(bmt.HashKey(pubkey[:]))
}

// Lookup returns the committed index for a pubkey. Pending (uncommitted)
// keys report false — they cannot attest until the next epoch.
func (r *Registry) Lookup(pubkey [48]byte) (MobileIndex, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	idx, ok := r.byKey[pubkey]
	return idx, ok
}

// PublicKey returns the committed key at an index.
func (r *Registry) PublicKey(idx MobileIndex) (common.PublicKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if int(idx) >= len(r.committed) {
		return nil, ErrUnknownIndex
	}
	return r.committed[idx].pk, nil
}

// PublicKeys resolves a sorted index set (a decoded signer mask) to its
// committed keys, for aggregate verification. An out-of-range index (a
// cert built against a later committed set than this node has synced)
// fails the whole resolution.
func (r *Registry) PublicKeys(indices []MobileIndex) ([]common.PublicKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]common.PublicKey, len(indices))
	for i, idx := range indices {
		if int(idx) >= len(r.committed) {
			return nil, fmt.Errorf("%w: %d", ErrUnknownIndex, idx)
		}
		out[i] = r.committed[idx].pk
	}
	return out, nil
}

// Count returns the number of committed devices.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.committed)
}

// PendingCount returns the number of registrations awaiting the next commit.
func (r *Registry) PendingCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.pending)
}

// IndexBound returns one past the highest committed index — the exclusive
// upper bound a signer mask's indices must fall under. Append-only
// commits mean this equals Count(); it stays a distinct method so mask
// decoding reads intent, not a coincidence.
func (r *Registry) IndexBound() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.committed)
}
