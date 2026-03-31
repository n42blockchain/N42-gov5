// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"sort"

	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	libcommit "github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/modules/state"
)

// MPTRootComputer implements state.RootComputer using Erigon's HexPatriciaHashed
// trie. It produces standard Ethereum MPT state roots identical to geth.
//
// The HexPatriciaHashed trie internally uses Keccak256 for key hashing and
// RLP for account encoding — no external encoding adaptation needed.
type MPTRootComputer struct {
	trie     *libcommit.HexPatriciaHashed
	branches map[string][]byte // in-memory branch node store
}

// NewMPTRootComputer creates a root computer backed by HexPatriciaHashed.
// Branch node data is stored in memory across blocks. Account/storage
// callbacks return "not found" — all data comes through Updates.
func NewMPTRootComputer() *MPTRootComputer {
	m := &MPTRootComputer{
		branches: make(map[string][]byte),
	}

	// Branch callback: load previously-folded branch nodes from in-memory store.
	branchFn := func(prefix []byte) ([]byte, error) {
		if data, ok := m.branches[string(prefix)]; ok {
			return data, nil
		}
		return nil, nil
	}
	// Account/storage callbacks: not needed — all data comes through Updates.
	accountFn := func(plainKey []byte, cell *libcommit.Cell) error { return nil }
	storageFn := func(plainKey []byte, cell *libcommit.Cell) error { return nil }

	m.trie = libcommit.NewHexPatriciaHashed(length.Addr, branchFn, accountFn, storageFn)

	// Immediately persist branch data during fold() so unfold() within
	// the same ProcessUpdates call can find it.
	// EncodeBranch produces [touchMap:2B][afterMap:2B][cells...].
	// unfoldBranchNode expects [afterMap:2B][cells...] (touchMap stripped).
	m.trie.SetPutBranchFn(func(updateKey []byte, branchData []byte) {
		if len(branchData) < 4 {
			return
		}
		// Skip touchMap (first 2 bytes), store from afterMap onwards.
		stripped := branchData[2:]
		cp := make([]byte, len(stripped))
		copy(cp, stripped)
		m.branches[string(updateKey)] = cp
	})

	return m
}

// ResetTrie resets HPH internal state between batches while preserving
// the in-memory branch data. This is required because ProcessUpdates
// expects a clean grid at the start of each batch.
func (m *MPTRootComputer) ResetTrie() {
	m.trie.Reset()
}

// RootScheme reports that this produces standard Ethereum MPT roots.
func (*MPTRootComputer) RootScheme() state.RootScheme {
	return state.RootSchemeEthereumMPT
}

// ComputeRoot applies dirty accounts and storage to the MPT and returns
// the new root hash. The root is compatible with geth's state root.
func (m *MPTRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	// Collect all updates: accounts + storage slots.
	type entry struct {
		plainKey  []byte
		hashedKey []byte
		update    libcommit.Update
	}
	entries := make([]entry, 0, len(accounts)+len(storage)*4)

	for addr, acct := range accounts {
		pk := addr.Bytes() // 20 bytes
		hk := keccakToNibbles(pk)

		if acct == nil || isAccountEmpty(acct) {
			entries = append(entries, entry{
				plainKey:  pk,
				hashedKey: hk,
				update:    libcommit.Update{Flags: libcommit.DeleteUpdate},
			})
			continue
		}

		var u libcommit.Update
		u.Flags = libcommit.BalanceUpdate | libcommit.NonceUpdate | libcommit.CodeUpdate
		u.Balance.Set(&acct.Balance)
		u.Nonce = acct.Nonce
		copy(u.CodeHashOrStorage[:], acct.CodeHash[:])
		entries = append(entries, entry{plainKey: pk, hashedKey: hk, update: u})
	}

	for addr, slots := range storage {
		for slot, val := range slots {
			// Storage plain key: address(20) + slot(32) = 52 bytes
			var pk [52]byte
			copy(pk[:20], addr[:])
			copy(pk[20:], slot[:])
			// Storage hashed key: keccak(addr) + keccak(slot) = 128 nibbles
			// HPH uses first 64 nibbles to locate the account, then next 64
			// nibbles to navigate the storage trie (depth 64-128).
			hk := storageKeccakToNibbles(addr[:], slot[:])

			if val == nil || val.IsZero() {
				entries = append(entries, entry{
					plainKey:  pk[:],
					hashedKey: hk,
					update:    libcommit.Update{Flags: libcommit.DeleteUpdate},
				})
			} else {
				var u libcommit.Update
				u.Flags = libcommit.StorageUpdate
				b := val.Bytes32()
				copy(u.CodeHashOrStorage[:], b[:])
				u.ValLength = 32
				// Trim leading zeros for compact storage.
				for u.ValLength > 0 && u.CodeHashOrStorage[32-u.ValLength] == 0 {
					u.ValLength--
				}
				entries = append(entries, entry{plainKey: pk[:], hashedKey: hk, update: u})
			}
		}
	}

	if len(entries) == 0 {
		root, err := m.trie.RootHash()
		if err != nil {
			return types.Hash{}, err
		}
		var h types.Hash
		copy(h[:], root)
		return h, nil
	}

	// HexPatriciaHashed requires keys sorted by hashedKey (nibble order).
	sort.Slice(entries, func(i, j int) bool {
		return compareBytes(entries[i].hashedKey, entries[j].hashedKey) < 0
	})

	plainKeys := make([][]byte, len(entries))
	hashedKeys := make([][]byte, len(entries))
	updates := make([]libcommit.Update, len(entries))
	for i, e := range entries {
		plainKeys[i] = e.plainKey
		hashedKeys[i] = e.hashedKey
		updates[i] = e.update
	}

	root, branchUpdates, err := m.trie.ProcessUpdates(plainKeys, hashedKeys, updates)
	if err != nil {
		return types.Hash{}, err
	}
	// Branch updates from ProcessUpdates are already handled by putBranchFn
	// during fold(). Only handle deletions (empty branch data) here.
	for k, v := range branchUpdates {
		if len(v) == 0 {
			delete(m.branches, k)
		}
		// Non-empty entries are already stored by putBranchFn.
	}

	var h types.Hash
	copy(h[:], root)
	return h, nil
}

// keccakToNibbles computes Keccak256(data) and expands to hex nibbles.
// Each byte of the hash becomes two nibble bytes (0x0-0xF).
func keccakToNibbles(data []byte) []byte {
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(data)
	hash := hasher.Sum(nil)
	nibbles := make([]byte, len(hash)*2)
	for i, b := range hash {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0f
	}
	return nibbles
}

// storageKeccakToNibbles produces 128 nibbles: keccak(addr) || keccak(slot).
// HPH uses the first 64 nibbles for the account trie path and the next 64
// for the storage trie path (grid rows 0-63 for account, 64-127 for storage).
func storageKeccakToNibbles(addr, slot []byte) []byte {
	hasher := sha3.NewLegacyKeccak256()
	hasher.Write(addr)
	addrHash := hasher.Sum(nil)

	hasher.Reset()
	hasher.Write(slot)
	slotHash := hasher.Sum(nil)

	nibbles := make([]byte, 128)
	for i, b := range addrHash {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0f
	}
	for i, b := range slotHash {
		nibbles[64+i*2] = b >> 4
		nibbles[64+i*2+1] = b & 0x0f
	}
	return nibbles
}

func compareBytes(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return len(a) - len(b)
}
