// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// LtHashAwareRootComputer wraps a JMTRootComputer and adds parallel LtHash
// incremental digest computation. It implements both state.RootComputer
// (for backward compatibility) and the extended ComputeRootWithOriginals method.
type LtHashAwareRootComputer struct {
	jmt    *JMTRootComputer
	lthash *LtHashCommitment
}

// NewLtHashAwareRootComputer creates a root computer that computes both
// JMT state root and LtHash digest in a single pass.
func NewLtHashAwareRootComputer(jmt *JMTRootComputer, lt *LtHashCommitment) *LtHashAwareRootComputer {
	return &LtHashAwareRootComputer{jmt: jmt, lthash: lt}
}

// RootScheme reports the underlying state-root semantics. LtHash runs alongside
// the primary root and does not change the state root scheme itself.
func (r *LtHashAwareRootComputer) RootScheme() state.RootScheme {
	if r == nil || r.jmt == nil {
		return state.RootSchemeUnknown
	}
	return r.jmt.RootScheme()
}

// ComputeRoot delegates to JMT for backward compatibility.
// LtHash is NOT updated through this path (no original data available).
func (r *LtHashAwareRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	return r.jmt.ComputeRoot(accounts, storage)
}

// ComputeRootWithOriginals computes the JMT root AND updates the LtHash digest.
// The originals maps provide the pre-block state needed for incremental LtHash.
//
// Returns: (jmtRoot, ltHashRoot, error)
func (r *LtHashAwareRootComputer) ComputeRootWithOriginals(
	accounts map[types.Address]*account.StateAccount,
	originals map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
	originalStorage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, types.Hash, error) {
	// 1. Compute JMT root (existing behavior).
	jmtRoot, err := r.jmt.ComputeRoot(accounts, storage)
	if err != nil {
		return types.Hash{}, types.Hash{}, err
	}

	// 2. Update LtHash digest incrementally.
	for addr, newAcct := range accounts {
		oldAcct := originals[addr]
		r.lthash.UpdateAccount(addr, oldAcct, newAcct)
	}

	for addr, slots := range storage {
		origSlots := originalStorage[addr]
		for slot, newVal := range slots {
			var oldVal *uint256.Int
			if origSlots != nil {
				oldVal = origSlots[slot]
			}
			r.lthash.UpdateStorage(addr, slot, oldVal, newVal)
		}
	}

	return jmtRoot, r.lthash.Root(), nil
}

// LtHashCommitment returns the underlying LtHash commitment.
func (r *LtHashAwareRootComputer) LtHashCommitment() *LtHashCommitment {
	return r.lthash
}
