// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"errors"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

// MobileIndex is a registrant's stable position in the registry — the
// unit the sparse signer mask (mask.go) is built from. uint32 covers
// the first four billion devices; the mask codec is width-agnostic.
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
	// ErrUnknownIndex reports a MobileIndex with no registered key.
	ErrUnknownIndex = errors.New("mobileverify: unknown mobile index")
)

// Registry maps BLS pubkeys to stable MobileIndex values. Registration
// is idempotent (same pubkey → same index) and requires a verified
// proof of possession. In-memory for phase 1; persistence and cross-IDC
// replication are later phases (design §3, §9).
type Registry struct {
	mu      sync.RWMutex
	byKey   map[[48]byte]MobileIndex
	pubkeys []common.PublicKey // index-addressed; parsed once at registration
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{byKey: make(map[[48]byte]MobileIndex)}
}

// Register verifies the PoP and returns the key's stable index,
// allocating the next index for a first-seen key. Re-registration of a
// known key re-verifies the PoP (cheap, and keeps the endpoint
// stateless) and returns the existing index.
func (r *Registry) Register(pubkey [48]byte, pop [96]byte) (MobileIndex, error) {
	pk, err := bls.PublicKeyFromBytes(pubkey[:])
	if err != nil {
		return 0, fmt.Errorf("%w: pubkey decode: %v", ErrBadPoP, err)
	}
	sig, err := bls.SignatureFromBytes(pop[:])
	if err != nil {
		return 0, fmt.Errorf("%w: signature decode: %v", ErrBadPoP, err)
	}
	if !sig.Verify(pk, PoPMessage(pubkey)) {
		return 0, ErrBadPoP
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if idx, ok := r.byKey[pubkey]; ok {
		return idx, nil
	}
	idx := MobileIndex(len(r.pubkeys))
	r.byKey[pubkey] = idx
	r.pubkeys = append(r.pubkeys, pk)
	return idx, nil
}

// Lookup returns the index for a registered pubkey.
func (r *Registry) Lookup(pubkey [48]byte) (MobileIndex, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	idx, ok := r.byKey[pubkey]
	return idx, ok
}

// PublicKey returns the parsed key at an index.
func (r *Registry) PublicKey(idx MobileIndex) (common.PublicKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if int(idx) >= len(r.pubkeys) {
		return nil, ErrUnknownIndex
	}
	return r.pubkeys[idx], nil
}

// PublicKeys resolves a sorted index set (a decoded signer mask) to its
// keys, for aggregate verification.
func (r *Registry) PublicKeys(indices []MobileIndex) ([]common.PublicKey, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]common.PublicKey, len(indices))
	for i, idx := range indices {
		if int(idx) >= len(r.pubkeys) {
			return nil, fmt.Errorf("%w: %d", ErrUnknownIndex, idx)
		}
		out[i] = r.pubkeys[idx]
	}
	return out, nil
}

// Count returns the number of registered devices.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.pubkeys)
}
