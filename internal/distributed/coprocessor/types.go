// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package coprocessor implements a ZK coprocessor framework that allows
// smart contracts to offload heavy computation off-chain and verify
// results on-chain using zero-knowledge proofs.
//
// Architecture:
//
//	1. Client submits a compute task (programHash + input)
//	2. Task enters the pending queue
//	3. External prover (or local SP1) executes the program
//	4. Prover submits ZK proof + public outputs
//	5. On-chain verifier validates the proof
//	6. Smart contract reads the verified result
package coprocessor

import (
	"time"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
)

// TaskStatus represents the lifecycle state of a compute task.
type TaskStatus uint8

const (
	TaskPending  TaskStatus = 0
	TaskProving  TaskStatus = 1
	TaskVerified TaskStatus = 2
	TaskFailed   TaskStatus = 3
	TaskExpired            TaskStatus = 4
	TaskChallenged         TaskStatus = 5
	TaskOptimisticVerified TaskStatus = 6
)

func (s TaskStatus) String() string {
	switch s {
	case TaskPending:
		return "pending"
	case TaskProving:
		return "proving"
	case TaskVerified:
		return "verified"
	case TaskFailed:
		return "failed"
	case TaskExpired:
		return "expired"
	case TaskChallenged:
		return "challenged"
	case TaskOptimisticVerified:
		return "optimistic_verified"
	default:
		return "unknown"
	}
}

// VerificationTier determines the verification method for a compute task.
// TierZK is the default (zero value) for backward compatibility.
type VerificationTier uint8

const (
	TierZK         VerificationTier = 0 // Full ZK proof verification (default)
	TierOptimistic VerificationTier = 1 // Optimistic with challenge window
	TierTEE        VerificationTier = 2 // TEE attestation verification
)

func (t VerificationTier) String() string {
	switch t {
	case TierZK:
		return "zk"
	case TierOptimistic:
		return "optimistic"
	case TierTEE:
		return "tee"
	default:
		return "unknown"
	}
}

// Capability identifies what types of computation a provider supports.
type Capability uint8

const (
	CapZK      Capability = 0
	CapWASM    Capability = 1
	CapAI      Capability = 2
	CapGeneral Capability = 3
)

func (c Capability) String() string {
	switch c {
	case CapZK:
		return "zk"
	case CapWASM:
		return "wasm"
	case CapAI:
		return "ai"
	case CapGeneral:
		return "general"
	default:
		return "unknown"
	}
}

// Task represents a single off-chain computation request.
type Task struct {
	ID            types.Hash    `json:"id"`
	ProgramHash   types.Hash    `json:"programHash"`
	InputHash     types.Hash    `json:"inputHash"`
	Input         []byte        `json:"-"` // not serialized in RPC responses
	Status        TaskStatus    `json:"status"`
	Submitter     types.Address `json:"submitter"`
	ProofData     []byte        `json:"proofData,omitempty"`
	PublicOutputs []byte        `json:"publicOutputs,omitempty"`
	CreatedAt     time.Time     `json:"createdAt"`
	CompletedAt   time.Time     `json:"completedAt,omitempty"`
	Error             string           `json:"error,omitempty"`
	VerificationTier  VerificationTier `json:"verificationTier"`
	Bond              uint64           `json:"bond,omitempty"`
	ChallengeDeadline time.Time        `json:"challengeDeadline,omitempty"`
	Challenger        types.Address    `json:"challenger,omitempty"`
	AssignedProvider  types.Address    `json:"assignedProvider,omitempty"`
	BidPrice          uint64           `json:"bidPrice,omitempty"`
	RewardAmount      uint64           `json:"rewardAmount,omitempty"`
}

// Program represents a registered verifiable program.
type Program struct {
	Hash            types.Hash `json:"hash"`
	VerificationKey []byte     `json:"verificationKey"`
	Name            string     `json:"name"`
	RegisteredAt    time.Time  `json:"registeredAt"`

	// Bytecode is the program's WASM module, optional at registration.
	// When present it is guaranteed to hash (Keccak256) to Hash, so the
	// registry doubles as a verified bytecode source for providers.
	Bytecode []byte `json:"-"`
}

// ComputeTaskID derives a deterministic task ID from program hash, input, and nonce.
func ComputeTaskID(programHash types.Hash, input []byte, nonce uint64) types.Hash {
	data := make([]byte, 0, 32+len(input)+8)
	data = append(data, programHash[:]...)
	data = append(data, input...)
	data = append(data, byte(nonce>>56), byte(nonce>>48), byte(nonce>>40), byte(nonce>>32),
		byte(nonce>>24), byte(nonce>>16), byte(nonce>>8), byte(nonce))
	return crypto.Keccak256Hash(data)
}
