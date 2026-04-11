// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Engine types unit for the engine_types package.
// Defines the PayloadAttributes, Withdrawal, BlobsBundle, and
// ForkChoiceState types.
// Engine API payload and fork-choice types.

//go:build n42el

// Package engine_types is a Phase 4 stub of erigon's
// execution/engineapi/engine_types. The cl/ tree references this package
// for the PayloadAttributes / BlobsBundle / ForkChoiceState / PayloadStatus
// types it ships across the EL seam.
//
// In Phase 4 we only need the type names to exist so cl/beacon/beaconevents
// and cl/phase1/execution_client can compile. The methods on these types
// are all stubs — they will be replaced with the real types (or with thin
// adapters around N42's existing engine API types in internal/api) when
// Phase 5 wires up the eladapter.
package engine_types

import (
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/hexutil"
)

// PayloadAttributes describes the parameters required by an EL payload
// builder when assembling a new block. Field shape mirrors the EL JSON-RPC
// engine API definition.
type PayloadAttributes struct {
	Timestamp             hexutil.Uint64       `json:"timestamp"`
	PrevRandao            common.Hash          `json:"prevRandao"`
	SuggestedFeeRecipient common.Address       `json:"suggestedFeeRecipient"`
	Withdrawals           []*Withdrawal        `json:"withdrawals,omitempty"`
	ParentBeaconBlockRoot *common.Hash         `json:"parentBeaconBlockRoot,omitempty"`
}

// Withdrawal mirrors EIP-4895 withdrawal entries shipped over the engine
// API.
type Withdrawal struct {
	Index          hexutil.Uint64 `json:"index"`
	ValidatorIndex hexutil.Uint64 `json:"validatorIndex"`
	Address        common.Address `json:"address"`
	Amount         hexutil.Uint64 `json:"amount"`
}

// BlobsBundle bundles raw blob data, KZG commitments, and proofs that the
// EL returns alongside an assembled payload.
type BlobsBundle struct {
	Commitments []hexutil.Bytes `json:"commitments"`
	Proofs      []hexutil.Bytes `json:"proofs"`
	Blobs       []hexutil.Bytes `json:"blobs"`
}

// ForkChoiceState is the (head, safe, finalized) tuple sent on every
// engine_forkchoiceUpdated call.
type ForkChoiceState struct {
	HeadHash      common.Hash `json:"headBlockHash"`
	SafeHash      common.Hash `json:"safeBlockHash"`
	FinalizedHash common.Hash `json:"finalizedBlockHash"`
}

// EngineStatus is the status string returned by the EL on
// engine_newPayload.
type EngineStatus string

const (
	ValidStatus            EngineStatus = "VALID"
	InvalidStatus          EngineStatus = "INVALID"
	SyncingStatus          EngineStatus = "SYNCING"
	AcceptedStatus         EngineStatus = "ACCEPTED"
	InvalidBlockHashStatus EngineStatus = "INVALID_BLOCK_HASH"
)

// PayloadStatus mirrors the engine API PayloadStatusV1 result.
type PayloadStatus struct {
	Status          EngineStatus `json:"status"`
	LatestValidHash *common.Hash     `json:"latestValidHash"`
	ValidationError *string          `json:"validationError"`
}
