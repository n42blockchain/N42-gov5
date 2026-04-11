// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// TrieRootComputer: incremental Ethereum MPT root via CalcTrieRoot.
// Hashes changed keys into HashedAccounts/HashedStorage, builds a
// RetainList of dirty paths and invokes erigon2.7's FlatDBTrieLoader
// so TrieOfAccounts/TrieOfStorage subtrees are reused for unchanged
// paths. SetRwTx wires the MDBX write transaction used by the
// HashCollector callbacks to persist updated branch nodes.

package commitment

import (
	"bytes"
	"fmt"

	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

// TrieRootComputer computes standard Ethereum MPT state roots incrementally
// using erigon2.7's CalcTrieRoot (FlatDBTrieLoader).
//
// Per-block flow:
//  1. Receive dirty accounts/storage from IntraBlockState
//  2. Hash changed keys → update HashedAccounts/HashedStorage
//  3. Build RetainList marking dirty hashed key paths
//  4. CalcTrieRoot reuses TrieOfAccounts/TrieOfStorage for unchanged subtrees
//  5. HashCollector callbacks update TrieOfAccounts/TrieOfStorage
type TrieRootComputer struct {
	tx     kv.RwTx
	hasher hash256
}

type hash256 interface {
	Reset()
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func NewTrieRootComputer() *TrieRootComputer {
	return &TrieRootComputer{
		hasher: sha3.NewLegacyKeccak256(),
	}
}

// SetRwTx sets the read-write transaction for HashedAccounts/Storage updates.
func (t *TrieRootComputer) SetRwTx(tx kv.RwTx) {
	t.tx = tx
}

var emptyCodeHash = crypto.Keccak256Hash(nil)

// ComputeRoot implements RootComputer.
// It incrementally updates HashedAccounts/HashedStorage from dirty keys,
// then calls CalcTrieRoot with a RetainList of changed paths.
func (t *TrieRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	if t.tx == nil {
		return types.Hash{}, fmt.Errorf("TrieRootComputer: tx not set")
	}

	rl := trie.NewRetainList(0)

	// Phase 1: Update HashedAccounts for dirty accounts + build RetainList.
	for addr, acct := range accounts {
		addrHash := t.keccakAddr(addr)

		// Add to RetainList (forces CalcTrieRoot to recompute this path).
		rl.AddKey(addrHash[:])

		if acct == nil {
			// Account deleted.
			if err := t.tx.Delete(modules.HashedAccounts, addrHash[:]); err != nil {
				return types.Hash{}, err
			}
			// Also clean up storage for this account in HashedStorage.
			if err := t.deleteAccountStorage(addrHash); err != nil {
				return types.Hash{}, err
			}
			continue
		}

		// Normalize CodeHash.
		if acct.CodeHash == (types.Hash{}) {
			acct.CodeHash = emptyCodeHash
		}
		enc := acct.MarshalV2()
		if err := t.tx.Put(modules.HashedAccounts, addrHash[:], enc); err != nil {
			return types.Hash{}, err
		}
	}

	// Phase 2: Update HashedStorage for dirty storage slots.
	for addr, slots := range storage {
		addrHash := t.keccakAddr(addr)

		for slot, val := range slots {
			slotHash := t.keccakHash(slot)

			// compositeKey: addrHash(32) + zero_incarnation(8) + slotHash(32) = 72B
			var compositeKey [72]byte
			copy(compositeKey[:32], addrHash[:])
			// [32:40] stays zero — incarnation removed from state model
			copy(compositeKey[40:], slotHash[:])

			// Add to RetainList.
			rl.AddKey(compositeKey[:])

			if val == nil || val.IsZero() {
				if err := t.tx.Delete(modules.HashedStorage, compositeKey[:]); err != nil {
					return types.Hash{}, err
				}
			} else {
				b := val.Bytes32()
				start := 0
				for start < 31 && b[start] == 0 {
					start++
				}
				if err := t.tx.Put(modules.HashedStorage, compositeKey[:], b[start:]); err != nil {
					return types.Hash{}, err
				}
			}
		}
	}

	// Phase 3: CalcTrieRoot. Clear TrieOfAccounts/TrieOfStorage before each
	// run — CalcTrieRoot outputs the complete trie structure, not incremental diffs.
	// The RetainList allows it to skip unchanged subtrees (reads HashedAccounts
	// only for dirty paths), but output must be a full replacement.
	if err := t.tx.ClearBucket(modules.TrieOfAccounts); err != nil {
		return types.Hash{}, err
	}
	if err := t.tx.ClearBucket(modules.TrieOfStorage); err != nil {
		return types.Hash{}, err
	}

	// Collect intermediate hashes in memory (avoid cursor read/write conflict).
	type kvPair struct{ k, v []byte }
	var accTrieUpdates []kvPair
	var storTrieUpdates []kvPair

	accCollector := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if len(keyHex) == 0 {
			return nil
		}
		k := append([]byte{}, keyHex...)
		if hasState == 0 {
			accTrieUpdates = append(accTrieUpdates, kvPair{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		accTrieUpdates = append(accTrieUpdates, kvPair{k, append([]byte{}, v...)})
		return nil
	}
	storCollector := func(accWithInc []byte, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := append(append(make([]byte, 0, len(accWithInc)+len(keyHex)), accWithInc...), keyHex...)
		if len(k) == 0 {
			return nil
		}
		if hasState == 0 {
			storTrieUpdates = append(storTrieUpdates, kvPair{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		storTrieUpdates = append(storTrieUpdates, kvPair{k, append([]byte{}, v...)})
		return nil
	}

	// Use empty RetainList — trie tables were just cleared, so there are
	// no cached intermediate hashes. CalcTrieRoot must do a full rebuild.
	loader := trie.NewFlatDBTrieLoader("trie-root", trie.NewRetainList(0), accCollector, storCollector, false)
	root, err := loader.CalcTrieRoot(t.tx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}

	// Phase 4: Flush collected intermediate hashes to TrieOfAccounts/TrieOfStorage.
	for _, kv := range accTrieUpdates {
		if kv.v == nil {
			_ = t.tx.Delete(modules.TrieOfAccounts, kv.k)
		} else {
			if err := t.tx.Put(modules.TrieOfAccounts, kv.k, kv.v); err != nil {
				return types.Hash{}, err
			}
		}
	}
	for _, kv := range storTrieUpdates {
		if kv.v == nil {
			_ = t.tx.Delete(modules.TrieOfStorage, kv.k)
		} else {
			if err := t.tx.Put(modules.TrieOfStorage, kv.k, kv.v); err != nil {
				return types.Hash{}, err
			}
		}
	}

	return root, nil
}

// InitFromPlainState populates HashedAccounts + HashedStorage from PlainState.
// Called once at genesis or when resuming from empty hashed tables.
func (t *TrieRootComputer) InitFromPlainState() error {
	if t.tx == nil {
		return fmt.Errorf("TrieRootComputer: tx not set")
	}

	// Clear existing.
	for _, bucket := range []string{modules.HashedAccounts, modules.HashedStorage, modules.TrieOfAccounts, modules.TrieOfStorage} {
		if err := t.tx.ClearBucket(bucket); err != nil {
			return err
		}
	}

	// Hash all accounts.
	ac, err := t.tx.Cursor(modules.Account)
	if err != nil {
		return err
	}
	defer ac.Close()

	for k, v, err := ac.First(); k != nil; k, v, err = ac.Next() {
		if err != nil {
			return err
		}
		if len(k) != 20 {
			continue
		}
		var addr types.Address
		copy(addr[:], k)
		addrHash := t.keccakAddr(addr)
		if err := t.tx.Put(modules.HashedAccounts, addrHash[:], v); err != nil {
			return err
		}
	}

	// Hash all storage.
	sc, err := t.tx.Cursor(modules.Storage)
	if err != nil {
		return err
	}
	defer sc.Close()

	for k, v, err := sc.First(); k != nil; k, v, err = sc.Next() {
		if err != nil {
			return err
		}
		if len(k) < 52 || len(v) == 0 {
			continue
		}
		var addr types.Address
		copy(addr[:], k[:20])
		addrHash := t.keccakAddr(addr)

		var slot types.Hash
		copy(slot[:], k[20:52])
		slotHash := t.keccakHash(slot)

		var compositeKey [72]byte
		copy(compositeKey[:32], addrHash[:])
		// [32:40] stays zero — incarnation removed
		copy(compositeKey[40:], slotHash[:])

		if err := t.tx.Put(modules.HashedStorage, compositeKey[:], v); err != nil {
			return err
		}
	}

	return nil
}

func (t *TrieRootComputer) deleteAccountStorage(addrHash types.Hash) error {
	// Delete all storage entries for this account from HashedStorage.
	// HashedStorage is DupSort with key prefix = addrHash[32] + incarnation[8].
	// We scan for any key starting with addrHash and delete.
	c, err := t.tx.Cursor(modules.HashedStorage)
	if err != nil {
		return err
	}
	defer c.Close()

	var toDelete [][]byte
	for k, _, err := c.Seek(addrHash[:]); k != nil && len(k) >= 32; k, _, err = c.Next() {
		if err != nil {
			return err
		}
		if !bytes.Equal(k[:32], addrHash[:]) {
			break
		}
		toDelete = append(toDelete, append([]byte{}, k...))
	}
	for _, k := range toDelete {
		if err := t.tx.Delete(modules.HashedStorage, k); err != nil {
			return err
		}
	}

	// Also clean up TrieOfStorage entries for this account.
	tc, err := t.tx.Cursor(modules.TrieOfStorage)
	if err != nil {
		return err
	}
	defer tc.Close()

	toDelete = toDelete[:0]
	for k, _, err := tc.Seek(addrHash[:]); k != nil && len(k) >= 32; k, _, err = tc.Next() {
		if err != nil {
			return err
		}
		if !bytes.Equal(k[:32], addrHash[:]) {
			break
		}
		toDelete = append(toDelete, append([]byte{}, k...))
	}
	for _, k := range toDelete {
		if err := t.tx.Delete(modules.TrieOfStorage, k); err != nil {
			return err
		}
	}

	return nil
}

func (t *TrieRootComputer) keccakAddr(addr types.Address) types.Hash {
	t.hasher.Reset()
	t.hasher.Write(addr[:])
	var h types.Hash
	t.hasher.Sum(h[:0])
	return h
}

func (t *TrieRootComputer) keccakHash(h types.Hash) types.Hash {
	t.hasher.Reset()
	t.hasher.Write(h[:])
	var out types.Hash
	t.hasher.Sum(out[:0])
	return out
}
