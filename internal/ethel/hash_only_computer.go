// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// HashOnlyComputer implements state.RootComputer but only updates
// HashedAccounts/HashedStorage without computing CalcTrieRoot.
// Used for non-verify blocks to keep hashed tables in sync cheaply.
type HashOnlyComputer struct {
	tx kv.RwTx
}

func NewHashOnlyComputer(tx kv.RwTx) *HashOnlyComputer {
	return &HashOnlyComputer{tx: tx}
}

var hoEmptyCodeHash = crypto.Keccak256Hash(nil)

func (h *HashOnlyComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	// Phase 1: Update HashedAccounts.
	for addr, acct := range accounts {
		addrHash := crypto.Keccak256(addr[:])

		if acct == nil {
			if err := h.tx.Delete(modules.HashedAccounts, addrHash); err != nil {
				return types.Hash{}, err
			}
			// Delete storage for this account.
			h.deleteAccountStorage(addrHash)
			continue
		}

		// CalcTrieRoot uses IsEmptyCodeHash() to decide whether to include CodeHash
		// in the trie. Zero hash != emptyCodeHash, so we must normalize it.
		if acct.CodeHash == (types.Hash{}) {
			acct.CodeHash = hoEmptyCodeHash
		}
		if err := h.tx.Put(modules.HashedAccounts, addrHash, acct.MarshalV2()); err != nil {
			return types.Hash{}, err
		}
	}

	// Phase 2: Update HashedStorage.
	for addr, slots := range storage {
		addrHash := crypto.Keccak256(addr[:])
		// incarnation removed from StateAccount — always use 0
		var incarnation uint64

		for slot, val := range slots {
			slotHash := crypto.Keccak256(slot[:])
			var compositeKey [72]byte
			copy(compositeKey[:32], addrHash)
			binary.BigEndian.PutUint64(compositeKey[32:40], incarnation)
			copy(compositeKey[40:], slotHash)

			if val == nil || val.IsZero() {
				h.tx.Delete(modules.HashedStorage, compositeKey[:])
			} else {
				b := val.Bytes32()
				start := 0
				for start < 31 && b[start] == 0 {
					start++
				}
				h.tx.Put(modules.HashedStorage, compositeKey[:], b[start:])
			}
		}
	}

	// Return empty hash — no trie computation.
	return types.Hash{}, nil
}

func (h *HashOnlyComputer) deleteAccountStorage(addrHash []byte) {
	c, err := h.tx.Cursor(modules.HashedStorage)
	if err != nil {
		return
	}
	defer c.Close()

	// HashedStorage DupSort key prefix = addrHash(32) + incarnation(8) = 40 bytes
	prefix := make([]byte, 40)
	copy(prefix[:32], addrHash)

	for k, _, err := c.Seek(prefix); k != nil && len(k) >= 40; k, _, err = c.Next() {
		if err != nil {
			break
		}
		// Check prefix match (first 32 bytes = addrHash).
		match := true
		for i := 0; i < 32; i++ {
			if k[i] != addrHash[i] {
				match = false
				break
			}
		}
		if !match {
			break
		}
		h.tx.Delete(modules.HashedStorage, k)
	}
}
