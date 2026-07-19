// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Reporter authentication for the cross-node cohort announcements. The
// announcements' `reporter` field arrives over an unsigned pubsub transport;
// left unauthenticated (the original design) any peer could inject an
// announcement claiming an arbitrary reporter, letting a single anonymous
// actor manufacture the "distinct announcements" ReconcileIndices counts and
// so censor a device (see reconcile.go). Layer 1 of the fix: every
// announcement is signed by the reporter's validator BLS key and the receiver
// rejects it unless the signature verifies against that validator's registered
// public key. This binds an announcement to a real, attributable validator —
// an outsider can no longer spoof any reporter, and one validator can no
// longer sybil many. (Layers 2C/2B raise the ban threshold to f+1 and add
// commit-reveal proof-of-possession so even a collusion of validators cannot
// silently censor.)

package mobileverify

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// CohortSigLen is the wire length of the reporter's BLS12-381 signature.
const CohortSigLen = 96

// Domain tags separate the two announcement kinds so an index-announcement
// signature can never be replayed as a cert-announcement signature.
var (
	cohortIndexAuthDomain  = []byte("n42-cohort-index-auth-v1")
	cohortRevealAuthDomain = []byte("n42-cohort-reveal-auth-v1")
	cohortCertAuthDomain   = []byte("n42-cohort-cert-auth-v1")
)

// CohortSigner signs an announcement's canonical bytes with the local node's
// validator BLS key, returning the 96-byte signature. Nil disables signing
// (single-node / test configurations that never gossip).
type CohortSigner func(msg []byte) []byte

// CohortVerifier verifies that sig is reporter's valid BLS signature over msg
// AND that reporter is an authorized cohort validator. Nil disables
// verification (single-node / test). Returns false to reject.
type CohortVerifier func(reporter types.Address, msg, sig []byte) bool

// cohortIndexAuthMessage builds the domain-separated bytes a reporter signs for
// an index announcement. Binding block hash+number+reporter+a hash of the
// (index,commitment) body prevents replaying a signature onto a different
// block, reporter, or index set.
func cohortIndexAuthMessage(blockHash types.Hash, blockNumber uint64, reporter types.Address, body []byte) []byte {
	return cohortAuthMessage(cohortIndexAuthDomain, blockHash, blockNumber, reporter, body)
}

// cohortCertAuthMessage builds the domain-separated bytes a reporter signs for
// a cert announcement (body = the fixed cert fields + mask).
func cohortCertAuthMessage(blockHash types.Hash, blockNumber uint64, reporter types.Address, body []byte) []byte {
	return cohortAuthMessage(cohortCertAuthDomain, blockHash, blockNumber, reporter, body)
}

func cohortAuthMessage(domain []byte, blockHash types.Hash, blockNumber uint64, reporter types.Address, body []byte) []byte {
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], blockNumber)
	bodyHash := crypto.Keccak256(body)
	msg := make([]byte, 0, len(domain)+32+8+20+len(bodyHash))
	msg = append(msg, domain...)
	msg = append(msg, blockHash[:]...)
	msg = append(msg, nb[:]...)
	msg = append(msg, reporter[:]...)
	msg = append(msg, bodyHash...)
	return crypto.Keccak256(msg)
}
