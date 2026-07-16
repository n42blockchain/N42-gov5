// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Sybil resistance for mobile-verifier registration (design §7 item 2).
// Registration is deliberately stake-free — the product goal is that any
// phone can join — so the cost of minting fake identities is pushed up with
// lightweight, non-economic tools instead:
//
//   - Proof-of-work: the registrant grinds a nonce until
//     Keccak256(tag ‖ pubkey ‖ nonce) has N leading zero bits. Verification
//     is one hash; solving is ~2^N hashes (N=20 ≈ one million ≈ well under a
//     second on a modern phone, but expensive enough at fleet scale to make
//     bulk identity minting uneconomical).
//   - Device attestation: a pluggable verifier (Play Integrity, DeviceCheck,
//     or any operator-supplied scheme) checked at the registration edge.
//     The interface keeps the platform-service integration out of this
//     package; nil means "not required".
//
// Both are enforced at the phone-facing admission edge (HTTP register); the
// IDC↔IDC gossip replication path replicates entries that already passed
// some node's edge.

package mobileverify

import (
	"encoding/binary"
	"errors"

	"github.com/n42blockchain/N42/crypto"
)

// powTag domain-separates registration PoW from every other hash in the
// system. Restated in cmd/evmsdk (import would cycle); TestPoWMessageMatchesSDK
// pins the bytes.
const powTag = "n42/mobileverify/pow/v1"

// MaxRegisterPoWBits bounds the difficulty a server will demand — beyond
// this, solving stops being "lightweight" and the knob is misconfigured.
const MaxRegisterPoWBits = 32

// ErrPoWRequired is returned when a registration lacks a valid proof-of-work
// and the admission edge demands one.
var ErrPoWRequired = errors.New("mobileverify: registration proof-of-work required")

// ErrAttestationRequired is returned when a device attestation is demanded
// but missing or invalid.
var ErrAttestationRequired = errors.New("mobileverify: device attestation required")

// DeviceAttestor verifies a platform device attestation for a registering
// key. Implementations wrap Play Integrity / DeviceCheck / operator schemes;
// the attestation blob is opaque to this package.
type DeviceAttestor interface {
	VerifyDevice(pubkey [48]byte, attestation []byte) error
}

// PoWMessage is the exact byte string hashed for registration proof-of-work.
func PoWMessage(pubkey [48]byte, nonce uint64) []byte {
	msg := make([]byte, 0, len(powTag)+48+8)
	msg = append(msg, powTag...)
	msg = append(msg, pubkey[:]...)
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], nonce)
	return append(msg, nb[:]...)
}

// VerifyPoW reports whether nonce solves the registration puzzle for pubkey
// at the given difficulty (leading zero bits of the Keccak256 digest).
// bits <= 0 always verifies (PoW disabled).
func VerifyPoW(pubkey [48]byte, nonce uint64, bits int) bool {
	if bits <= 0 {
		return true
	}
	if bits > MaxRegisterPoWBits {
		bits = MaxRegisterPoWBits
	}
	digest := crypto.Keccak256(PoWMessage(pubkey, nonce))
	return leadingZeroBits(digest) >= bits
}

// SolvePoW grinds nonces until one satisfies the difficulty, bounded by
// maxIters (0 = 1<<(bits+4), comfortably above the ~2^bits expectation).
// Returns the solving nonce and true, or 0 and false if the budget ran out.
func SolvePoW(pubkey [48]byte, bits int, maxIters uint64) (uint64, bool) {
	if bits <= 0 {
		return 0, true
	}
	if bits > MaxRegisterPoWBits {
		bits = MaxRegisterPoWBits
	}
	if maxIters == 0 {
		maxIters = 1 << uint(bits+4)
	}
	for nonce := uint64(0); nonce < maxIters; nonce++ {
		if VerifyPoW(pubkey, nonce, bits) {
			return nonce, true
		}
	}
	return 0, false
}

func leadingZeroBits(b []byte) int {
	n := 0
	for _, c := range b {
		if c == 0 {
			n += 8
			continue
		}
		for mask := byte(0x80); mask != 0; mask >>= 1 {
			if c&mask != 0 {
				return n
			}
			n++
		}
	}
	return n
}
