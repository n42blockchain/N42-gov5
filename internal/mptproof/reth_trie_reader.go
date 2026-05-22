package mptproof

import (
	"bytes"
	"context"
	"fmt"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

const (
	// reth's per-contract trie tables.
	rethAccountsTrieTable = "AccountsTrie"
	rethStoragesTrieTable = "StoragesTrie"
)

// RethTrieReader reads reth's AccountsTrie + StoragesTrie tables —
// reth's own production source for eth_getProof. Per-contract
// storage tries live in StoragesTrie keyed by the contract's
// addrHash (32 B) with a StoredNibblesSubKey (65 B) subkey, so
// heavy-contract proofs scope to that contract's sub-trie cursor
// scan (avoids the unified-trie collapse problem documented in
// docs/ethel/g1d-storage-v1-runbook.md Step 4).
type RethTrieReader struct {
	src *RethHashedLeafSource
}

// NewRethTrieReader is the per-contract trie companion to
// RethBackedReader (the leaf source). One env, two views.
func NewRethTrieReader(src *RethHashedLeafSource) *RethTrieReader {
	return &RethTrieReader{src: src}
}

// AccountBranchAt returns the AccountsTrie branch at the given
// nibble path. Reth stores AccountsTrie keys as raw nibble bytes
// (one nibble per byte, NO padding, NO length suffix). Empty
// prefix is the zero-length byte slice.
func (r *RethTrieReader) AccountBranchAt(nibblePath []byte) (*mpttrie.BranchNode, bool, error) {
	tx, err := r.src.db.BeginRo(context.Background())
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	// Reth's AccountsTrie uses the raw nibble path as-is (length
	// encoded by the key's own byte length).
	v, err := tx.GetOne(rethAccountsTrieTable, nibblePath)
	if err != nil {
		return nil, false, err
	}
	if v == nil {
		return nil, false, nil
	}
	bn, err := DecodeRethBranchNodeCompact(v)
	if err != nil {
		return nil, false, fmt.Errorf("AccountsTrie at %x: %w", nibblePath, err)
	}
	return bn, true, nil
}

// StorageBranchAt returns the StoragesTrie branch for the given
// (addrHash, nibblePath). addrHash is the 32-byte keccak of the
// 20-byte contract address; the trie below it is the contract's
// own per-contract storage trie.
func (r *RethTrieReader) StorageBranchAt(addrHash []byte, nibblePath []byte) (*mpttrie.BranchNode, bool, error) {
	if len(addrHash) != 32 {
		return nil, false, fmt.Errorf("StorageBranchAt: addrHash must be 32 bytes, got %d", len(addrHash))
	}
	tx, err := r.src.db.BeginRo(context.Background())
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	c, err := tx.CursorDupSort(rethStoragesTrieTable)
	if err != nil {
		return nil, false, err
	}
	defer c.Close()

	subKey := EncodeStoredNibbles(nibblePath)
	// SeekBothRange positions on (addrHash, subKey). The dup value's
	// first 65 bytes are the SubKey echo; the rest is the
	// BranchNodeCompact. Equality on the subkey is required —
	// reth's `dup_walk_with_key` pattern.
	v, err := c.SeekBothRange(addrHash, subKey)
	if err != nil {
		return nil, false, err
	}
	if v == nil || len(v) < 65 {
		return nil, false, nil
	}
	// Reth encodes StorageTrieEntry as (StoredNibblesSubKey 65 B
	// echo) || BranchNodeCompact. Strip the echo and compare.
	gotSub := v[:65]
	if !bytes.Equal(gotSub, subKey) {
		// Cursor found a later subkey; no exact match.
		return nil, false, nil
	}
	bn, err := DecodeRethBranchNodeCompact(v[65:])
	if err != nil {
		return nil, false, fmt.Errorf("StoragesTrie at addr %x path %x: %w",
			addrHash, nibblePath, err)
	}
	return bn, true, nil
}

// AccountTrieRoot returns the root of the AccountsTrie — i.e. the
// canonical state root recorded by reth. Reth stores it as the
// branch at the empty-prefix key; if no row exists, the trie is
// empty.
func (r *RethTrieReader) AccountTrieRoot() ([32]byte, error) {
	bn, ok, err := r.AccountBranchAt(nil)
	if err != nil {
		return [32]byte{}, err
	}
	if !ok {
		return [32]byte{}, fmt.Errorf("AccountsTrie has no root branch")
	}
	if !bn.HasRoot {
		return [32]byte{}, fmt.Errorf("AccountsTrie root branch lacks rootHash field")
	}
	return bn.RootHash, nil
}
