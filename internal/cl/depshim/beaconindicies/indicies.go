// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.2.7 — minimal-stub mirror of
// ../erigon/cl/persistence/beacon_indicies. Only the functions
// Caplin's fork-choice path actually call are declared here. Real
// MDBX-backed indexers land in Phase 7.5 alongside the snapshot
// persistence layer.

//go:build n42el

// Package name uses the underscore form to match the upstream
// cl/persistence/beacon_indicies package name so caplin code that
// references `beacon_indicies.WriteExecutionPayloadEnvelopeIndicies`
// compiles without an alias.
package beacon_indicies

import (
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/lib/kv"
)

// WriteExecutionPayloadEnvelopeIndicies is the Gloas-only
// (EIP-7732) hook that persists a beacon block root ↔ execution
// payload mapping. STUB: no-op for pre-Gloas mainnet. Once Caplin's
// Gloas stage loop lands and proposers locally produce envelopes,
// this must persist the actual indices to allow re-org and historical
// envelope lookup.
func WriteExecutionPayloadEnvelopeIndicies(
	_ kv.RwTx, _ common.Hash, _ *cltypes.ExecutionPayloadEnvelope,
) error {
	return nil
}

// ReadCanonicalBlockRoot returns the canonical beacon block root at the
// given slot. STUB: returns zero-hash, which downstream code treats as
// "unknown". Real impl needed for fork-choice walks and proposer
// attestation production; lands in Phase 7.5 when the persistence
// layer is wired.
func ReadCanonicalBlockRoot(_ kv.Tx, _ uint64) (common.Hash, error) {
	return common.Hash{}, nil
}
