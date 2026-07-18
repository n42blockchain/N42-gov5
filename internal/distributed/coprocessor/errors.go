// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Sentinel errors for the coprocessor package. Covers task lifecycle
// (ErrTaskNotFound, ErrTaskNotPending), tiered verification
// (ErrUnsupportedTier, ErrBondRequired, ErrInvalidAttestation),
// challenges and provider registration failures.

package coprocessor

import "errors"

var (
	ErrProgramNotRegistered = errors.New("coprocessor: program not registered")
	ErrTaskNotFound         = errors.New("coprocessor: task not found")
	ErrTaskNotPending       = errors.New("coprocessor: task not in pending/proving state")
	ErrInvalidProof         = errors.New("coprocessor: invalid proof data")
	ErrMaxPendingReached    = errors.New("coprocessor: max pending tasks reached")

	// Tiered verification errors
	ErrUnsupportedTier        = errors.New("coprocessor: unsupported verification tier")
	ErrBondRequired           = errors.New("coprocessor: optimistic verification requires bond")
	ErrInvalidAttestation     = errors.New("coprocessor: invalid TEE attestation")
	ErrProofBindingMismatch   = errors.New("coprocessor: proof envelope does not bind this task's program/input/outputs")
	ErrTEEVerifierUnavailable = errors.New("coprocessor: no TEE quote verifier configured (fail-closed)")
	ErrResultOutputsMismatch  = errors.New("coprocessor: optimistic claimed result does not match public outputs")

	// Challenge errors
	ErrTaskNotChallengeable  = errors.New("coprocessor: task not in optimistic-verified state")
	ErrChallengeWindowExpired = errors.New("coprocessor: challenge window has expired")
	ErrDuplicateChallenge    = errors.New("coprocessor: duplicate challenge from same challenger")
	ErrChallengeNotFound     = errors.New("coprocessor: challenge not found")
	ErrChallengeResolved     = errors.New("coprocessor: challenge already resolved")

	// Provider errors
	ErrProviderNotFound     = errors.New("coprocessor: provider not found")
	ErrProviderExists       = errors.New("coprocessor: provider already registered")
	ErrProviderSuspended    = errors.New("coprocessor: provider is suspended")
	ErrInsufficientStake    = errors.New("coprocessor: insufficient stake")

	// Marketplace errors
	ErrTaskNotOpen           = errors.New("coprocessor: task not open for bidding")
	ErrBidTooHigh            = errors.New("coprocessor: bid exceeds task reward")
	ErrTaskAlreadyAssigned   = errors.New("coprocessor: task already assigned")
	ErrNoEligibleBids        = errors.New("coprocessor: no eligible bids")

	// WASM errors
	ErrWASMDisabled         = errors.New("coprocessor: WASM execution disabled")
	ErrWASMModuleNotFound   = errors.New("coprocessor: WASM module not found")
	ErrWASMOutOfGas         = errors.New("coprocessor: WASM execution out of gas")
	ErrWASMMemoryExceeded   = errors.New("coprocessor: WASM memory limit exceeded")
	ErrWASMTimeout          = errors.New("coprocessor: WASM execution timeout")
)
