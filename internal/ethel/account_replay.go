// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// account_replay.go: trivial Account-table replay primitive used by the
// freezer changeset paths (forward apply + backward unwind + reorg).
//
// Phase B made acctcs OldValue self-contained (full V2 with CodeHash),
// so replay no longer needs to recover CodeHash from PlainContractCode
// or thread an incarnation through the call site. The legacy helpers
// valueIncarnation / revertIncarnation / readIncarnation / writeIncarnation
// were deleted with this file's rewrite — see commit message for context.
//
// Auxiliary tables PlainContractCode and IncarnationMap continue to be
// maintained by the live forward-execution write path
// (PlainStateWriter / BufferedPlainStateWriter); they are no longer
// consulted on the replay/unwind hot path. Phase D will remove them.

package ethel

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// applyAccountValue writes encodedValue to modules.Account for addr, or
// deletes the entry when encodedValue is empty. encodedValue must be
// full-V2 bytes (Phase B: CodeHash inline, no hidden incarnation tag).
func applyAccountValue(tx kv.RwTx, addr types.Address, encodedValue []byte) error {
	if len(encodedValue) == 0 {
		return tx.Delete(modules.Account, addr[:])
	}
	return tx.Put(modules.Account, addr[:], encodedValue)
}
