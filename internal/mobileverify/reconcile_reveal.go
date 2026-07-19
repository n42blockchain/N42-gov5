// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Layer 2B of the cohort-reporter fix: commit-reveal proof-of-possession.
//
// Layers 1 (authenticate reporters) and 2C (f+1 ban threshold) raise the
// censorship bar to a collusion of f validators, but a collusion could still
// replay a victim's PUBLIC commitment (Keccak256 of the device signature) and,
// combined with the victim's own honest announcement, reach f+1 and ban the
// device. This layer removes that: a reporter's commitment is bound to the
// reporter — Keccak256(deviceSig || reporterAddr) — so it can only be produced
// by a node that actually HOLDS the raw device signature, which the raw sig's
// hash (broadcast in the commit phase) does not reveal. At reconcile time each
// reporter reveals the raw signatures; a witness counts toward a ban only if
// its revealed signature (a) reproduces the reporter-bound commitment under
// THAT reporter's address and (b) is the device's genuine registered signature.
// A replayer that copied an honest commitment (bound to the honest reporter)
// cannot make it verify under its own address, and cannot fabricate a fresh
// bound commitment without the raw signature it never received — so it is
// caught and reported as misbehaving, never counted. The device's own
// signature is unchanged (still reporter-agnostic over block+root), so nothing
// in the mobile streaming protocol is touched.

package mobileverify

import (
	"sort"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
)

// ReporterBoundCommitment is a reporter's proof-of-possession commitment to a
// device signature: Keccak256(deviceSig || reporterAddr). Only a node that
// holds the raw deviceSig can compute it for its own address.
func ReporterBoundCommitment(deviceSig [96]byte, reporter types.Address) types.Hash {
	buf := make([]byte, 0, 96+20)
	buf = append(buf, deviceSig[:]...)
	buf = append(buf, reporter[:]...)
	return crypto.Keccak256Hash(buf)
}

// RevealedSig is one device's raw signature plus the receipts root it attests
// — both are needed to reconstruct the message the device signed and verify
// the signature.
type RevealedSig struct {
	Sig  [96]byte
	Root types.Hash
}

// ReporterReveal is one reporter's phase-2 reveal: the raw device signatures
// (with their roots) behind the commitments it announced in phase 1. The
// reporter is authenticated at the transport layer (cohort_auth.go); the raw
// signatures are verified here.
type ReporterReveal struct {
	Reporter types.Address
	Sigs     map[MobileIndex]RevealedSig
}

// ReconcileWithReveals is the Layer 2B replacement for ReconcileIndices. It
// bans a device index only when >= threshold DISTINCT reporters each backed a
// (reporter-bound) commitment to it with a valid reveal — a genuine multi-node
// submission that no replayer can forge. Reporters that committed to a device
// but whose reveal is missing, does not reproduce their bound commitment, or is
// not that device's registered signature are returned in misbehaved (they
// either replayed a foreign commitment or fabricated one) and never counted.
func ReconcileWithReveals(
	threshold int,
	reg *Registry,
	blockHash types.Hash,
	blockNumber uint64,
	commits map[types.Address][]IndexCommitment,
	reveals map[types.Address]ReporterReveal,
) (banned []MobileIndex, misbehaved []types.Address) {
	if threshold < 2 {
		threshold = 2
	}
	witnesses := make(map[MobileIndex]map[types.Address]struct{})
	misbehavedSet := make(map[types.Address]struct{})

	for reporter, ics := range commits {
		reveal, hasReveal := reveals[reporter]
		for _, ic := range ics {
			var rs RevealedSig
			ok := false
			if hasReveal {
				rs, ok = reveal.Sigs[ic.Index]
			}
			if !ok {
				// Committed but never revealed the raw signature: cannot be
				// counted, and is suspicious (a genuine holder always can).
				misbehavedSet[reporter] = struct{}{}
				continue
			}
			// The reveal must reproduce THIS reporter's bound commitment — a
			// copied (foreign-reporter-bound) commitment fails here.
			if ReporterBoundCommitment(rs.Sig, reporter) != ic.Commitment {
				misbehavedSet[reporter] = struct{}{}
				continue
			}
			// The reveal must be the device's genuine registered signature —
			// a fabricated (self-consistent but bogus) reveal fails here.
			if !verifyDeviceSignature(reg, ic.Index, rs.Sig, blockHash, blockNumber, rs.Root) {
				misbehavedSet[reporter] = struct{}{}
				continue
			}
			if witnesses[ic.Index] == nil {
				witnesses[ic.Index] = make(map[types.Address]struct{}, threshold)
			}
			witnesses[ic.Index][reporter] = struct{}{}
		}
	}

	for idx, reps := range witnesses {
		if len(reps) >= threshold {
			banned = append(banned, idx)
		}
	}
	sort.Slice(banned, func(i, j int) bool { return banned[i] < banned[j] })
	for r := range misbehavedSet {
		misbehaved = append(misbehaved, r)
	}
	return banned, misbehaved
}

// verifyDeviceSignature reports whether sig is index's registered device's
// genuine BLS signature over the block's canonical signing message at root.
func verifyDeviceSignature(reg *Registry, index MobileIndex, sig [96]byte, blockHash types.Hash, blockNumber uint64, root types.Hash) bool {
	if reg == nil {
		return false
	}
	pk, err := reg.PublicKey(index)
	if err != nil {
		return false
	}
	s, err := bls.SignatureFromBytes(sig[:])
	if err != nil {
		return false
	}
	return s.Verify(pk, SigningMessage(blockHash, blockNumber, root))
}
