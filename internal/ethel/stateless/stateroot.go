package stateless

import (
	"bytes"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// StateRootUpdater performs two-level stateless state-root recomputation for
// N42's per-account MPT: an account trie whose leaves embed each account's
// storage-subtree root (RLP[nonce,balance,storageRoot,codeHash]).
//
// Usage:
//
//	u, _ := NewStateRootUpdater(preStateRoot, accountProofNodes)
//	u.AddStorageProof(addrHash, storageRoot, storageProofNodes) // per dirty contract
//	u.SetStorage(addrHash, slotHash, newValueBytes)             // SSTORE
//	u.SetAccount(addrHash, &AccountUpdate{...})                 // balance/nonce/code
//	u.DeleteAccount(addrHash)                                   // SELFDESTRUCT (rare post-6780)
//	root, _ := u.Root()                                         // compare to header.stateRoot
//
// The recompute order inside Root(): first re-root every dirty storage subtree
// (folding new slot values in), patch the owning account leaf's storageRoot,
// then re-hash the account trie. Account-only changes (balance/nonce) skip the
// storage step. The storage proof for a contract must be supplied before its
// slots are touched.
type StateRootUpdater struct {
	acct      *partialTrie            // account trie
	storage   map[string]*storageCtx  // addrHash(hex string of 32B) -> storage subtree
	acctDirty map[string]*AccountEdit // pending account-field edits, keyed by addrHash
	deleted   map[string]bool         // accounts to delete
}

type storageCtx struct {
	trie     *partialTrie
	origRoot []byte // storage root as proven (for sanity / no-op detection)
	dirty    bool
}

// AccountEdit carries the non-storage account fields to write. A nil field means
// "keep existing" only when the account already exists in the proof; for a fresh
// account all fields are taken as given (zero defaults).
type AccountEdit struct {
	Nonce    uint64
	Balance  uint256.Int
	CodeHash []byte // 32B; nil → keep existing / empty
}

// NewStateRootUpdater loads the account-trie multiproof rooted at preStateRoot.
func NewStateRootUpdater(preStateRoot []byte, accountProofNodes [][]byte) (*StateRootUpdater, error) {
	at, err := newPartialTrie(preStateRoot, accountProofNodes)
	if err != nil {
		return nil, fmt.Errorf("account trie: %w", err)
	}
	return &StateRootUpdater{
		acct:      at,
		storage:   map[string]*storageCtx{},
		acctDirty: map[string]*AccountEdit{},
		deleted:   map[string]bool{},
	}, nil
}

// AddStorageProof registers a contract's storage-subtree multiproof, rooted at
// storageRoot (which must equal the storageRoot embedded in that account's leaf).
func (u *StateRootUpdater) AddStorageProof(addrHash types.Hash, storageRoot []byte, proofNodes [][]byte) error {
	st, err := newPartialTrie(storageRoot, proofNodes)
	if err != nil {
		return fmt.Errorf("storage trie %x: %w", addrHash[:6], err)
	}
	u.storage[string(addrHash[:])] = &storageCtx{trie: st, origRoot: append([]byte(nil), storageRoot...)}
	return nil
}

// SetStorage applies an SSTORE: slot -> value (value==0 / empty deletes the slot).
// The contract's storage proof must already be registered via AddStorageProof.
func (u *StateRootUpdater) SetStorage(addrHash, slotHash types.Hash, value []byte) error {
	sc, ok := u.storage[string(addrHash[:])]
	if !ok {
		return fmt.Errorf("SetStorage %x: no storage proof registered", addrHash[:6])
	}
	// Storage leaf value is RLP(trimmed-big-endian) of the slot word.
	var enc []byte
	if v := trimLeftZeros(value); len(v) > 0 {
		enc = rlpStr(v)
	}
	if err := sc.trie.update(keybytesToHex(slotHash[:]), enc); err != nil {
		return fmt.Errorf("SetStorage %x/%x: %w", addrHash[:6], slotHash[:6], err)
	}
	sc.dirty = true
	return nil
}

// SetAccount records non-storage field edits for an account.
func (u *StateRootUpdater) SetAccount(addrHash types.Hash, e *AccountEdit) {
	u.acctDirty[string(addrHash[:])] = e
	delete(u.deleted, string(addrHash[:]))
}

// DeleteAccount marks an account for removal from the account trie.
func (u *StateRootUpdater) DeleteAccount(addrHash types.Hash) {
	u.deleted[string(addrHash[:])] = true
	delete(u.acctDirty, string(addrHash[:]))
}

// Root recomputes and returns the post-state account-trie root.
func (u *StateRootUpdater) Root() ([]byte, error) {
	// 1. Re-root every dirty storage subtree and fold the new root into its
	//    owning account leaf (read current leaf, patch storageRoot, write back).
	for addr, sc := range u.storage {
		if !sc.dirty {
			continue
		}
		newStorRoot := sc.trie.hash()
		var ah types.Hash
		copy(ah[:], addr)
		if u.deleted[addr] {
			continue // account being deleted; storage root irrelevant
		}
		if err := u.patchAccountStorageRoot(ah, newStorRoot); err != nil {
			return nil, err
		}
	}

	// 2. Apply account-field edits (may also need the just-patched storageRoot,
	//    so read-modify-write the leaf).
	for addr, e := range u.acctDirty {
		var ah types.Hash
		copy(ah[:], addr)
		if err := u.applyAccountEdit(ah, e); err != nil {
			return nil, err
		}
	}

	// 3. Deletions.
	for addr := range u.deleted {
		var ah types.Hash
		copy(ah[:], addr)
		if err := u.acct.update(keybytesToHex(ah[:]), nil); err != nil {
			return nil, fmt.Errorf("delete account %x: %w", ah[:6], err)
		}
	}

	return u.acct.hash(), nil
}

// patchAccountStorageRoot reads the account leaf, replaces its storageRoot with
// newRoot, and writes it back. The account MUST already exist in the proof
// (a contract with storage always does).
func (u *StateRootUpdater) patchAccountStorageRoot(addrHash types.Hash, newRoot []byte) error {
	leaf, found, err := u.acct.get(keybytesToHex(addrHash[:]))
	if err != nil {
		return fmt.Errorf("read account %x: %w", addrHash[:6], err)
	}
	if !found {
		return fmt.Errorf("patch storageRoot: account %x not in proof", addrHash[:6])
	}
	al, err := decodeAccountLeaf(leaf)
	if err != nil {
		return fmt.Errorf("decode account %x: %w", addrHash[:6], err)
	}
	if bytes.Equal(al.storageRoot, newRoot) {
		return nil // no change
	}
	al.storageRoot = append([]byte(nil), newRoot...)
	return u.acct.update(keybytesToHex(addrHash[:]), al.encode())
}

// applyAccountEdit read-modify-writes the account leaf with new nonce/balance/
// codeHash, preserving the current storageRoot (which storage step may have just
// patched). For a fresh account (not in proof) it starts from empty defaults.
func (u *StateRootUpdater) applyAccountEdit(addrHash types.Hash, e *AccountEdit) error {
	leaf, found, err := u.acct.get(keybytesToHex(addrHash[:]))
	if err != nil {
		return fmt.Errorf("read account %x: %w", addrHash[:6], err)
	}
	var al *accountLeaf
	if found {
		al, err = decodeAccountLeaf(leaf)
		if err != nil {
			return fmt.Errorf("decode account %x: %w", addrHash[:6], err)
		}
	} else {
		al = &accountLeaf{
			storageRoot: append([]byte(nil), emptyRootHash...),
			codeHash:    append([]byte(nil), emptyCodeHashBytes...),
		}
	}
	al.nonce = e.Nonce
	al.balance.Set(&e.Balance)
	if len(e.CodeHash) == 32 {
		al.codeHash = append([]byte(nil), e.CodeHash...)
	}
	return u.acct.update(keybytesToHex(addrHash[:]), al.encode())
}

// emptyCodeHashBytes = keccak(nil), the canonical empty-code hash.
var emptyCodeHashBytes = keccak(nil)

func trimLeftZeros(b []byte) []byte {
	i := 0
	for i < len(b) && b[i] == 0 {
		i++
	}
	return b[i:]
}
