// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Unwind for reth-2.2-style hashed-canonical state.
//
// HashedCanonicalWriter (modules/state) records, per block, the pre-block
// account/storage values into AccountChangeSet/StorageChangeSet (reth-style,
// plain-keyed, no incarnation). UnwindHashedCanonical reverts those blocks by
// replaying each block's changeset in reverse: it reads the pre-values and
// re-applies them via the incremental TrieRootComputer (which rewrites
// HashedAccounts/HashedStorage + TrieOf* and returns the root). The result is the
// state root at block `to`.
//
// Code is NOT removed (modules.Code is hash-keyed/immutable); restoring the
// account's codeHash (carried in the account changeset's MarshalV2 blob) is
// sufficient. An orphaned Code entry is harmless.
//
// Required for: (1) live-mode reorgs (revert to the fork point, re-import the
// canonical branch); (2) a cheap reset (unwind a few blocks vs an 85-min
// re-migrate). Caveat: SELFDESTRUCT-wiped storage (recordStorageWipe /
// collectPreWipeSlots) is not yet captured in the eth-el commit path — rare
// post-Cancun (EIP-6780) but needed for unwinding across such a block.

package commitment

import (
	"bytes"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
)

// UnwindHashedCanonical reverts hashed-canonical state from block `from` down to
// the post-state of block `to` (from > to). Returns the recomputed state root at
// `to`. Consumed changesets for the reverted blocks are deleted.
func UnwindHashedCanonical(tx kv.RwTx, from, to uint64) (types.Hash, error) {
	return UnwindHashedCanonicalWith(tx, from, to, nil)
}

// BlockChangesProvider returns block n's pre-value maps from an alternate
// changeset store (e.g. acctcs/storcs freezer blobs; see the archive data
// layout). found=false means the store does not cover n and the caller falls
// back to the MDBX changesets.
type BlockChangesProvider func(n uint64) (
	map[types.Address]*account.StateAccount,
	map[types.Address]map[types.Hash]*uint256.Int,
	bool, error)

// UnwindHashedCanonicalWith is UnwindHashedCanonical with an optional
// alternate changeset provider consulted FIRST. Blocks the provider does not
// cover unwind via the legacy MDBX changesets.
func UnwindHashedCanonicalWith(tx kv.RwTx, from, to uint64, extra BlockChangesProvider) (types.Hash, error) {
	trc := NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(true)

	var root types.Hash
	for n := from; n > to; n-- {
		var accounts map[types.Address]*account.StateAccount
		var storage map[types.Address]map[types.Hash]*uint256.Int
		var covered bool
		var err error
		if extra != nil {
			accounts, storage, covered, err = extra(n)
			if err != nil {
				return root, err
			}
		}
		if !covered {
			accounts, storage, err = readBlockChangeset(tx, n)
			if err != nil {
				return root, err
			}
		}
		if root, err = trc.ComputeRoot(accounts, storage); err != nil {
			return root, err
		}
		// MDBX rows are deleted regardless (no-op when the block's changesets
		// live in the freezer — those get overwritten by the re-imported
		// canonical block's Append).
		if err := deleteBlockChangeset(tx, n); err != nil {
			return root, err
		}
	}
	return root, nil
}

// readBlockChangeset reads block n's account + storage changesets and returns the
// pre-block values as ComputeRoot deltas (nil account = was created in n → delete;
// zero slot = was new/zero in n → delete).
func readBlockChangeset(tx kv.Tx, n uint64) (
	map[types.Address]*account.StateAccount,
	map[types.Address]map[types.Hash]*uint256.Int,
	error,
) {
	accounts := make(map[types.Address]*account.StateAccount)
	if err := changeset.ForRange(tx, modules.AccountChangeSet, n, n+1, func(_ uint64, k, v []byte) error {
		var addr types.Address
		copy(addr[:], k)
		if len(v) == 0 {
			accounts[addr] = nil // created in block n → unwind deletes it
			return nil
		}
		acc := new(account.StateAccount)
		if err := acc.DecodeForStorageV2(v); err != nil {
			return err
		}
		accounts[addr] = acc
		return nil
	}); err != nil {
		return nil, nil, err
	}

	storage := make(map[types.Address]map[types.Hash]*uint256.Int)
	if err := changeset.ForRange(tx, modules.StorageChangeSet, n, n+1, func(_ uint64, k, v []byte) error {
		if len(k) < length.Addr+length.Hash {
			return nil
		}
		var addr types.Address
		var slot types.Hash
		copy(addr[:], k[:length.Addr])
		copy(slot[:], k[length.Addr:length.Addr+length.Hash])
		val := new(uint256.Int)
		if len(v) > 0 {
			val.SetBytes(v) // pre-block value; empty → 0 → ComputeRoot deletes the slot
		}
		if storage[addr] == nil {
			storage[addr] = make(map[types.Hash]*uint256.Int)
		}
		storage[addr][slot] = val
		return nil
	}); err != nil {
		return nil, nil, err
	}
	return accounts, storage, nil
}

// deleteBlockChangeset removes the consumed account + storage changesets for
// block n. AccountChangeSet is keyed by blockNum (delete clears all dups);
// StorageChangeSet is keyed by blockNum+addr, so all keys with the blockNum
// prefix are deleted.
func deleteBlockChangeset(tx kv.RwTx, n uint64) error {
	bn := modules.EncodeBlockNumber(n)
	if err := tx.Delete(modules.AccountChangeSet, bn); err != nil {
		return err
	}
	c, err := tx.Cursor(modules.StorageChangeSet)
	if err != nil {
		return err
	}
	var keys [][]byte
	seen := make(map[string]struct{})
	for k, _, err := c.Seek(bn); k != nil && bytes.HasPrefix(k, bn); k, _, err = c.Next() {
		if err != nil {
			c.Close()
			return err
		}
		if _, ok := seen[string(k)]; !ok {
			seen[string(k)] = struct{}{}
			keys = append(keys, append([]byte(nil), k...))
		}
	}
	c.Close()
	for _, k := range keys {
		if err := tx.Delete(modules.StorageChangeSet, k); err != nil {
			return err
		}
	}
	return nil
}

// UnwindPlainStateBlock reverts block n's PLAIN state (modules.Account /
// modules.Storage) using the block's changeset pre-values, then deletes the
// consumed changeset rows. This is the world-state half of a HotStuff
// same-height sibling switch (the QMDB tree half is
// QMDBRootComputer.RevertBlock): the re-executed sibling must read the
// PARENT's PlainState, not the orphaned block's leftovers — an un-reverted key
// the sibling doesn't re-touch would silently keep the loser's value.
//
// nil account pre-value = the account was created in n → delete; zero slot
// pre-value = the slot was empty before n → delete. Code stays (hash-keyed,
// immutable; orphaned entries are harmless).
func UnwindPlainStateBlock(tx kv.RwTx, n uint64) error {
	accounts, storage, err := readBlockChangeset(tx, n)
	if err != nil {
		return err
	}
	for addr, acc := range accounts {
		if acc == nil {
			if err := tx.Delete(modules.Account, addr[:]); err != nil {
				return err
			}
			continue
		}
		if err := tx.Put(modules.Account, addr[:], acc.MarshalV2()); err != nil {
			return err
		}
	}
	for addr, slots := range storage {
		for slot, val := range slots {
			ck := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot.Bytes())
			if val == nil || val.IsZero() {
				if err := tx.Delete(modules.Storage, ck); err != nil {
					return err
				}
				continue
			}
			v := make([]byte, val.ByteLen())
			val.WriteToSlice(v)
			if err := tx.Put(modules.Storage, ck, v); err != nil {
				return err
			}
		}
	}
	return deleteBlockChangeset(tx, n)
}
