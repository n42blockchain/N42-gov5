// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.2.7 — minimal-stub mirror of ../erigon/execution/protocol/misc.
// Only the functions Caplin's fork-choice / on_block path actually
// call are defined here. Real implementations land alongside the
// blob_storage / sentinel wiring (Phase 7.5+).

//go:build n42el

// Package name "misc" matches the upstream erigon execution/protocol/misc
// import name so caplin code that uses bare `misc.ValidateBlobs(...)`
// compiles without alias-renames.
package misc

import (
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
)

// ValidateBlobs mirrors erigon's misc.ValidateBlobs signature. STUB:
// returns nil (accept all blob hashes). The real validation runs
// inside api.EngineAPIv4.NewPayloadV4 (Phase 7.1.1.b wiring), which
// is the only path eth-el actually executes payloads through.
//
// Follower-mode N42 doesn't need this layer; Caplin invokes it as a
// defence-in-depth check before forwarding to the EL. Replace when
// Phase 7.5 makes Caplin a self-sufficient proposer.
func ValidateBlobs(
	_blobGasUsed, _maxBlobsGas, _maxBlobsPerBlock uint64,
	_expectedBlobHashes []common.Hash,
	_transactions *[]deptypes.Transaction,
) error {
	return nil
}
