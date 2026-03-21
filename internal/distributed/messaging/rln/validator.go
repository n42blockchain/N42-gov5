// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rln

import (
	"encoding/binary"
	"time"

	"github.com/n42blockchain/N42/log"
)

// DefaultEpochDuration is the default RLN epoch length.
const DefaultEpochDuration = 10 * time.Second

// GossipSubValidator provides RLN proof validation for GossipSub messages.
type GossipSubValidator struct {
	verifier  *Verifier
	registry  *NullifierRegistry
	epochSec  int
	rateLimit int
}

// NewGossipSubValidator creates a new RLN-based GossipSub message validator.
func NewGossipSubValidator(verifier *Verifier, registry *NullifierRegistry, epochSec, rateLimit int) *GossipSubValidator {
	if epochSec <= 0 {
		epochSec = int(DefaultEpochDuration.Seconds())
	}
	if rateLimit <= 0 {
		rateLimit = 1
	}
	return &GossipSubValidator{
		verifier:  verifier,
		registry:  registry,
		epochSec:  epochSec,
		rateLimit: rateLimit,
	}
}

// CurrentEpoch returns the current RLN epoch number.
func (v *GossipSubValidator) CurrentEpoch() uint64 {
	return uint64(time.Now().Unix()) / uint64(v.epochSec)
}

// ValidateProof validates an RLN proof attached to a message.
// Returns ValidationAccept, ValidationReject, or ValidationIgnore.
func (v *GossipSubValidator) ValidateProof(proof *RLNProof) ValidationResult {
	if proof == nil {
		return ValidationReject
	}

	// Check epoch is current or recent
	currentEpoch := v.CurrentEpoch()
	if proof.Epoch > currentEpoch+1 {
		log.Debug("RLN: future epoch", "proof", proof.Epoch, "current", currentEpoch)
		return ValidationReject
	}
	if proof.Epoch+2 < currentEpoch {
		log.Debug("RLN: stale epoch", "proof", proof.Epoch, "current", currentEpoch)
		return ValidationIgnore
	}

	// Verify the RLN proof (Merkle proof validation)
	if err := v.verifier.VerifyProof(proof); err != nil {
		log.Debug("RLN: invalid proof", "err", err)
		return ValidationReject
	}

	// Check nullifier registry for spam
	duplicate, isSpam := v.registry.Check(proof)
	if isSpam {
		log.Warn("RLN: spam detected",
			"epoch", proof.Epoch,
			"nullifier", proof.Nullifier[:8],
		)
		// Attempt to recover identity secret
		isRecovered, secret, err := DetectSpam(duplicate, proof)
		if err == nil && isRecovered {
			log.Warn("RLN: identity secret recovered (slashable)",
				"secret", secret[:8],
			)
		}
		return ValidationReject
	}

	return ValidationAccept
}

// ValidationResult represents the result of message validation.
type ValidationResult int

const (
	ValidationAccept ValidationResult = iota
	ValidationReject
	ValidationIgnore
)

// EpochFromTimestamp converts a timestamp to an epoch number.
func EpochFromTimestamp(t time.Time, epochSec int) uint64 {
	return uint64(t.Unix()) / uint64(epochSec)
}

// MessageHashForRLN computes the message hash used in RLN proof generation.
func MessageHashForRLN(topic string, payload []byte, epoch uint64) [32]byte {
	var epochBuf [8]byte
	binary.BigEndian.PutUint64(epochBuf[:], epoch)
	return PoseidonHash([]byte(topic), payload, epochBuf[:])
}
