// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Core types for the storage deal lifecycle (slice 1 of
// docs/distributed-storage-design-2026-07-06.md §9): manifest, deal spec and
// state, the StorageProvider abstraction, and the challenge/proof records.
// Slice 1 uses replication (r full copies) for redundancy; erasure coding
// (RS(k,n)) is a documented later slice. The economic + challenge + repair
// machinery here is redundancy-scheme agnostic.

package deal

import (
	"context"
	"errors"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// DefaultChunkSize is the storage-proof chunk granularity (256 KiB).
const DefaultChunkSize = 256 << 10

// Manifest commits to a stored object: the chunk-merkle root plus geometry.
// The deal ID is derived from the manifest so it is content-addressed.
type Manifest struct {
	Root       types.Hash `json:"root"`
	Size       uint64     `json:"size"`
	ChunkSize  int        `json:"chunkSize"`
	ChunkCount int        `json:"chunkCount"`
}

// DealSpec parameterizes a storage deal at submission.
type DealSpec struct {
	Data []byte // object bytes (slice 1: in-memory; CAS-streamed later)

	Redundancy int    // r full replicas on distinct providers/groups
	Disperse   bool   // replicas must span distinct provider groups
	ChunkSize  int    // 0 → DefaultChunkSize
	EpochPrice uint64 // wei per byte per epoch per replica
	Epochs     uint64 // number of storage epochs paid for

	// Timeouts.
	ChallengeTimeout time.Duration // provider must answer a challenge within
}

// DealStatus is the lifecycle state.
type DealStatus uint8

const (
	DealActive    DealStatus = 0
	DealCompleted DealStatus = 1 // paid epochs exhausted, finalized
	DealFailed    DealStatus = 2 // could not maintain redundancy
)

func (s DealStatus) String() string {
	switch s {
	case DealActive:
		return "active"
	case DealCompleted:
		return "completed"
	case DealFailed:
		return "failed"
	default:
		return "unknown"
	}
}

// Replica is one provider's copy of the deal.
type Replica struct {
	Provider types.Address
	Group    string
	Healthy  bool // last challenge passed
}

// StorageProvider is a node that physically holds deal chunks and answers
// storage-proof challenges. Slice 1 ships MemProvider (in-memory, for tests
// and single-process); a transport-backed provider (messaging) implements the
// same interface later, exactly as the compute Executor does.
type StorageProvider interface {
	// Store persists the deal's chunks. Must verify them against the manifest
	// root before accepting (a provider never stores unverifiable data).
	Store(ctx context.Context, dealID types.Hash, manifest Manifest, chunks [][]byte) error
	// Prove returns chunk i and its merkle proof. A provider that does not
	// hold the data cannot produce a valid proof.
	Prove(ctx context.Context, dealID types.Hash, chunkIdx int) ([]byte, MerkleProof, error)
	// Retrieve returns all chunks (repair source).
	Retrieve(ctx context.Context, dealID types.Hash) ([][]byte, error)
	// Drop releases storage when the deal ends.
	Drop(dealID types.Hash)
}

// Errors.
var (
	ErrNilSpec         = errors.New("deal: nil spec")
	ErrBadRedundancy   = errors.New("deal: redundancy must be >= 1")
	ErrZeroEpochs      = errors.New("deal: epochs must be >= 1")
	ErrZeroPrice       = errors.New("deal: epoch price must be > 0")
	ErrInsufficient    = errors.New("deal: not enough eligible providers for the requested redundancy")
	ErrDealUnknown     = errors.New("deal: unknown deal")
	ErrProviderUnknown = errors.New("deal: storage provider not registered")
	ErrDealNotActive   = errors.New("deal: not active")
	ErrChunkOutOfRange = errors.New("deal: chunk index out of range")
	ErrNotStored       = errors.New("deal: provider does not hold this deal")
)

// Validate checks a spec's internal consistency.
func (s *DealSpec) Validate() error {
	if s == nil {
		return ErrNilSpec
	}
	if s.Redundancy < 1 {
		return ErrBadRedundancy
	}
	if s.Epochs < 1 {
		return ErrZeroEpochs
	}
	if s.EpochPrice == 0 {
		return ErrZeroPrice
	}
	return nil
}
