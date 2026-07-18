// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// TieredVerifier plus the Verifier interface for ZK, Optimistic and
// TEE tiers. Every tier fails CLOSED: the ZK tier requires a proof
// envelope that cryptographically binds the task's program, input and
// outputs (with a pluggable backend for the succinct-proof blob), the
// optimistic tier requires the claimed result to BE the public outputs
// so a challenge has an unambiguous target, and the TEE tier rejects
// everything until a real quote verifier is configured.

package coprocessor

import (
	"bytes"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// VerificationConfig holds per-tier configuration parameters.
type VerificationConfig struct {
	ChallengePeriodSec int    // challenge window for optimistic verification
	MinBondWei         uint64 // minimum bond for optimistic tasks
}

// Verifier defines the interface for proof verification at any tier.
// publicOutputs is the result the prover claims for the task; a verifier
// must refuse a proof that does not bind them.
type Verifier interface {
	Verify(task *Task, proofData, publicOutputs []byte) (bool, error)
}

// OptimisticVerifier accepts results backed by an economic bond,
// opening a challenge window for fraud proof disputes.
type OptimisticVerifier struct{}

func (v *OptimisticVerifier) Verify(task *Task, proofData, publicOutputs []byte) (bool, error) {
	if task.Bond == 0 {
		return false, ErrBondRequired
	}
	if len(proofData) == 0 {
		return false, ErrInvalidProof
	}
	// The optimistic "proof" IS the claimed result: a challenger re-executes
	// the program and disputes the stored outputs. Letting proofData and
	// publicOutputs diverge would leave the challenge without an unambiguous
	// target — the provider could later point at whichever copy survives.
	if !bytes.Equal(proofData, publicOutputs) {
		return false, ErrResultOutputsMismatch
	}
	return true, nil
}

// TEEQuoteVerifier validates a Trusted Execution Environment attestation
// quote (e.g. an Intel SGX/TDX quote checked against the vendor's
// attestation service and an enclave-measurement allowlist) and confirms
// its report data binds the task's input and outputs.
type TEEQuoteVerifier func(task *Task, quote, publicOutputs []byte) (bool, error)

// TEEVerifier validates TEE attestation reports. It fails CLOSED: until a
// real quote verifier is wired in via SetTEEQuoteVerifier, every submission
// is rejected — accepting a quote nobody checks is not hardware-backed
// security, it is a bond-free optimistic tier without the challenge window.
type TEEVerifier struct {
	mu          sync.RWMutex
	quoteVerify TEEQuoteVerifier
}

// SetQuoteVerifier installs the actual attestation validator.
func (v *TEEVerifier) SetQuoteVerifier(fn TEEQuoteVerifier) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.quoteVerify = fn
}

func (v *TEEVerifier) Verify(task *Task, proofData, publicOutputs []byte) (bool, error) {
	// Minimal quote shape: [0:64] signature, [64:] measurement + report data.
	if len(proofData) < 64 {
		return false, ErrInvalidAttestation
	}
	v.mu.RLock()
	fn := v.quoteVerify
	v.mu.RUnlock()
	if fn == nil {
		return false, ErrTEEVerifierUnavailable
	}
	return fn(task, proofData, publicOutputs)
}

// ZK proof envelope v1. The header binds the proof to one specific task
// INSTANCE and result; the tail carries the backend's succinct proof blob:
//
//	[0]       version (zkEnvelopeVersion)
//	[1:33]    task ID      — must equal task.ID (defeats sibling-task free-riding)
//	[33:65]   program hash — must equal task.ProgramHash
//	[65:97]   input hash   — must equal task.InputHash
//	[97:129]  outputs hash — must equal Keccak256(publicOutputs)
//	[129:]    backend proof blob (non-empty)
const (
	zkEnvelopeVersion = 1
	zkEnvelopeSize    = 1 + 32 + 32 + 32 + 32 // 129
)

// BuildZKProofEnvelope assembles the v1 envelope a ZK prover submits. taskID
// binds the proof to one specific task instance, so a valid proof for a sibling
// task with the same program+input cannot be replayed to collect its reward.
func BuildZKProofEnvelope(taskID, programHash, inputHash types.Hash, publicOutputs, proofBlob []byte) []byte {
	out := make([]byte, 0, zkEnvelopeSize+len(proofBlob))
	out = append(out, zkEnvelopeVersion)
	out = append(out, taskID[:]...)
	out = append(out, programHash[:]...)
	out = append(out, inputHash[:]...)
	outputsHash := crypto.Keccak256Hash(publicOutputs)
	out = append(out, outputsHash[:]...)
	out = append(out, proofBlob...)
	return out
}

// ZKBlobVerifier is the succinct-proof backend. It MUST check both that the
// proof blob is valid for the program's circuit AND that the proof's public
// inputs equal the passed inputHash/outputsHash — the envelope header is
// attacker-supplied plaintext, so without this cross-check a valid blob for one
// (input, output) could be wrapped in a header claiming a different output.
type ZKBlobVerifier func(program *Program, blob []byte, inputHash, outputsHash types.Hash) (bool, error)

// ZKVerifier validates the task/result binding of a ZK proof envelope and
// delegates the succinct blob to a pluggable backend. It FAILS CLOSED: with no
// backend it cannot attest the computation (the envelope binding alone only
// prevents cross-task replay, it does not prove the output), so it rejects
// rather than accept. Wire a real proof system through SetBlobVerifier, or
// replace the whole tier via RegisterVerifier / SetZKMLVerifier.
type ZKVerifier struct {
	registry *Registry

	mu         sync.RWMutex
	verifyBlob ZKBlobVerifier
}

// SetBlobVerifier installs the succinct-proof backend (see ZKBlobVerifier for
// the contract it must uphold).
func (v *ZKVerifier) SetBlobVerifier(fn ZKBlobVerifier) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.verifyBlob = fn
}

func (v *ZKVerifier) Verify(task *Task, proofData, publicOutputs []byte) (bool, error) {
	if len(proofData) <= zkEnvelopeSize {
		return false, ErrInvalidProof
	}
	if proofData[0] != zkEnvelopeVersion {
		return false, fmt.Errorf("%w: unsupported envelope version %d", ErrInvalidProof, proofData[0])
	}
	program, ok := v.registry.Get(task.ProgramHash)
	if !ok {
		return false, ErrProgramNotRegistered
	}
	var envTask, envProgram, envInput, envOutputs types.Hash
	copy(envTask[:], proofData[1:33])
	copy(envProgram[:], proofData[33:65])
	copy(envInput[:], proofData[65:97])
	copy(envOutputs[:], proofData[97:129])
	if envTask != task.ID || envProgram != task.ProgramHash || envInput != task.InputHash {
		return false, fmt.Errorf("%w: task/program/input header mismatch", ErrProofBindingMismatch)
	}
	if envOutputs != crypto.Keccak256Hash(publicOutputs) {
		return false, fmt.Errorf("%w: outputs hash mismatch", ErrProofBindingMismatch)
	}
	v.mu.RLock()
	fn := v.verifyBlob
	v.mu.RUnlock()
	if fn == nil {
		return false, ErrZKVerifierUnavailable
	}
	return fn(program, proofData[zkEnvelopeSize:], envInput, envOutputs)
}

// TieredVerifier routes proof verification to the appropriate verifier
// based on the task's VerificationTier setting.
type TieredVerifier struct {
	mu        sync.RWMutex
	verifiers map[VerificationTier]Verifier
}

// NewTieredVerifier creates a verifier with all standard tiers registered.
func NewTieredVerifier(registry *Registry) *TieredVerifier {
	return &TieredVerifier{
		verifiers: map[VerificationTier]Verifier{
			TierZK:         &ZKVerifier{registry: registry},
			TierOptimistic: &OptimisticVerifier{},
			TierTEE:        &TEEVerifier{},
		},
	}
}

// Verify dispatches proof verification to the tier-appropriate verifier.
func (tv *TieredVerifier) Verify(task *Task, proofData, publicOutputs []byte) (bool, error) {
	tv.mu.RLock()
	v, ok := tv.verifiers[task.VerificationTier]
	tv.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf("%w: %d", ErrUnsupportedTier, task.VerificationTier)
	}
	return v.Verify(task, proofData, publicOutputs)
}

// RegisterVerifier adds or replaces a verifier for the given tier.
func (tv *TieredVerifier) RegisterVerifier(tier VerificationTier, v Verifier) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.verifiers[tier] = v
}

// SetTEEQuoteVerifier installs the attestation validator on the standard TEE
// tier. Returns an error (rather than silently no-op'ing) if the TEE tier has
// been replaced with a custom Verifier — a silent failure would leave the
// operator believing a real validator was wired when it was not.
func (tv *TieredVerifier) SetTEEQuoteVerifier(fn TEEQuoteVerifier) error {
	tv.mu.RLock()
	v, ok := tv.verifiers[TierTEE].(*TEEVerifier)
	tv.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: TEE tier is not the standard TEEVerifier", ErrVerifierTierReplaced)
	}
	v.SetQuoteVerifier(fn)
	return nil
}

// SetZKBlobVerifier installs the succinct-proof backend on the standard ZK
// tier. Returns an error (rather than silently no-op'ing) if the ZK tier has
// been replaced with a custom Verifier.
func (tv *TieredVerifier) SetZKBlobVerifier(fn ZKBlobVerifier) error {
	tv.mu.RLock()
	v, ok := tv.verifiers[TierZK].(*ZKVerifier)
	tv.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: ZK tier is not the standard ZKVerifier", ErrVerifierTierReplaced)
	}
	v.SetBlobVerifier(fn)
	return nil
}

// ZKMLVerifierFunc is a function type that adapts ZKML proof verification
// to the coprocessor's Verifier interface. The function receives the raw
// proof data and should return whether the proof is valid.
type ZKMLVerifierFunc func(proofData []byte) (bool, error)

// ZKMLVerifierAdapter wraps a ZKMLVerifierFunc as a coprocessor Verifier.
type ZKMLVerifierAdapter struct {
	verifyFunc ZKMLVerifierFunc
}

func (v *ZKMLVerifierAdapter) Verify(task *Task, proofData, publicOutputs []byte) (bool, error) {
	if v.verifyFunc == nil {
		return false, fmt.Errorf("ZKML verifier not configured")
	}
	return v.verifyFunc(proofData)
}

// SetZKMLVerifier registers a ZKML verification function on the ZK tier.
// This connects the ZKML prover/verifier with the coprocessor's verification pipeline.
func (tv *TieredVerifier) SetZKMLVerifier(fn ZKMLVerifierFunc) {
	tv.RegisterVerifier(TierZK, &ZKMLVerifierAdapter{verifyFunc: fn})
}
