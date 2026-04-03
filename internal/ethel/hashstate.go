// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// SetupStateRootComputerPublic is the exported version of setupStateRootComputer.
func SetupStateRootComputerPublic(tx kv.RwTx, ibs *state.IntraBlockState) *commitment.TrieRootComputer {
	return setupStateRootComputer(tx, ibs)
}

// setupStateRootComputer creates and attaches a TrieRootComputer to the
// IntraBlockState so that state root is computed incrementally after
// each block's CommitBlock.
func setupStateRootComputer(tx kv.RwTx, ibs *state.IntraBlockState) *commitment.TrieRootComputer {
	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	ibs.SetRootComputer(trc)
	return trc
}

// CalcStateRoot runs CalcTrieRoot on already-populated HashedAccounts/Storage.
// Assumes HashedAccounts/Storage are up-to-date (via HashOnlyComputer or InitHashState).
func CalcStateRoot(tx kv.RwTx) (types.Hash, error) {
	// Clear trie tables — CalcTrieRoot outputs fresh trie.
	if err := tx.ClearBucket(kv.TrieOfAccounts); err != nil {
		return types.Hash{}, err
	}
	if err := tx.ClearBucket(kv.TrieOfStorage); err != nil {
		return types.Hash{}, err
	}

	loader := trie.NewFlatDBTrieLoader("ethel-verify", trie.NewRetainList(0), nil, nil, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}
	return root, nil
}

// FullStateRootVerify does a complete re-hash and MPT root computation.
// It clears HashedAccounts/HashedStorage/TrieOfAccounts/TrieOfStorage,
// re-hashes everything from Account/Storage, then runs CalcTrieRoot.
// This is expensive but guaranteed correct for verification.
func FullStateRootVerify(tx kv.RwTx) (types.Hash, error) {
	// Clear hashed/trie tables.
	for _, tbl := range []string{kv.HashedAccounts, kv.HashedStorage, kv.TrieOfAccounts, kv.TrieOfStorage} {
		if err := tx.ClearBucket(tbl); err != nil {
			return types.Hash{}, fmt.Errorf("clear %s: %w", tbl, err)
		}
	}

	// Re-hash all accounts.
	if err := hashAllAccounts(tx); err != nil {
		return types.Hash{}, err
	}
	// Re-hash all storage.
	if err := hashAllStorage(tx); err != nil {
		return types.Hash{}, err
	}

	// CalcTrieRoot with empty RetainList (full rebuild).
	loader := trie.NewFlatDBTrieLoader("ethel-verify", trie.NewRetainList(0), nil, nil, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}
	return root, nil
}

func hashAllAccounts(tx kv.RwTx) error {
	cursor, err := tx.Cursor("Account")
	if err != nil {
		return err
	}
	defer cursor.Close()

	emptyCodeHash := crypto.Keccak256Hash(nil)

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return err
		}
		if len(k) != 20 {
			continue
		}
		hashedKey := crypto.Keccak256(k)

		// CalcTrieRoot's IsEmptyCodeHash() checks for keccak256(empty),
		// not zero hash. Normalize zero → emptyCodeHash so EOAs are
		// correctly recognized as "no code".
		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			return err
		}
		if acc.CodeHash == (types.Hash{}) {
			acc.CodeHash = emptyCodeHash
		}

		if err := tx.Put(kv.HashedAccounts, hashedKey, acc.MarshalV2()); err != nil {
			return err
		}
	}
	return nil
}

func hashAllStorage(tx kv.RwTx) error {
	cursor, err := tx.Cursor("Storage")
	if err != nil {
		return err
	}
	defer cursor.Close()

	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return err
		}
		if len(k) < 54 {
			continue
		}
		addrHash := crypto.Keccak256(k[:20])
		incarnation := binary.BigEndian.Uint16(k[20:22])
		slotHash := crypto.Keccak256(k[22:54])

		var compositeKey [72]byte
		copy(compositeKey[:32], addrHash)
		binary.BigEndian.PutUint64(compositeKey[32:40], uint64(incarnation))
		copy(compositeKey[40:72], slotHash)

		if err := tx.Put(kv.HashedStorage, compositeKey[:], v); err != nil {
			return err
		}
	}
	return nil
}

// InitHashState does a full conversion of Account/Storage tables into
// HashedAccounts/HashedStorage. This must be done once before the first
// incremental state root verification.
func InitHashState(tx kv.RwTx) error {
	// Check if HashedAccounts already has data.
	c, err := tx.Cursor(kv.HashedAccounts)
	if err != nil {
		return err
	}
	k, _, err := c.First()
	c.Close()
	if err != nil {
		return err
	}
	if k != nil {
		// Already populated.
		return nil
	}

	log.Info("Initializing HashedAccounts/HashedStorage from PlainState (one-time)...")

	// Hash all accounts.
	accCursor, err := tx.Cursor("Account")
	if err != nil {
		return fmt.Errorf("open Account: %w", err)
	}
	defer accCursor.Close()

	emptyCodeHash := crypto.Keccak256Hash(nil)
	accCount := 0
	for k, v, err := accCursor.First(); k != nil; k, v, err = accCursor.Next() {
		if err != nil {
			return err
		}
		if len(k) != 20 {
			continue
		}
		hashedKey := crypto.Keccak256(k)

		// Normalize zero CodeHash → emptyCodeHash for CalcTrieRoot.
		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			return fmt.Errorf("decode account: %w", err)
		}
		if acc.CodeHash == (types.Hash{}) {
			acc.CodeHash = emptyCodeHash
		}
		if err := tx.Put(kv.HashedAccounts, hashedKey, acc.MarshalV2()); err != nil {
			return fmt.Errorf("put HashedAccounts: %w", err)
		}
		accCount++
	}

	// Hash all storage.
	stoCursor, err := tx.Cursor("Storage")
	if err != nil {
		return fmt.Errorf("open Storage: %w", err)
	}
	defer stoCursor.Close()

	stoCount := 0
	for k, v, err := stoCursor.First(); k != nil; k, v, err = stoCursor.Next() {
		if err != nil {
			return err
		}
		if len(k) < 54 {
			continue
		}
		addrHash := crypto.Keccak256(k[:20])
		incarnation := k[20:22] // 2-byte
		slotHash := crypto.Keccak256(k[22:54])

		// HashedStorage DupSort: full key = addrHash(32) + inc(8) + slotHash(32) = 72
		var compositeKey [72]byte
		copy(compositeKey[:32], addrHash)
		// Zero-pad 2-byte incarnation to 8-byte BE
		binary.BigEndian.PutUint64(compositeKey[32:40], uint64(binary.BigEndian.Uint16(incarnation)))
		copy(compositeKey[40:72], slotHash)

		if err := tx.Put(kv.HashedStorage, compositeKey[:], v); err != nil {
			return fmt.Errorf("put HashedStorage: %w", err)
		}
		stoCount++
	}

	log.Info("HashState initialization complete", "accounts", accCount, "storage", stoCount)
	return nil
}
