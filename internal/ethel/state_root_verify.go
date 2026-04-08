// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// state_root_verify.go — Ethereum-standard state root verification
// using the minimal MPT implementation (no CalcTrieRoot dependency).

package ethel

import (
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
)

// VerifyStateRoot computes the Ethereum state root by building a fresh MPT
// from the plain Account and Storage tables. Does not use CalcTrieRoot or
// HashedAccounts/HashedStorage at all — fully independent verification.
func VerifyStateRoot(tx kv.RwTx) (types.Hash, error) {
	emptyRoot := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	emptyCodeHash := crypto.Keccak256Hash(nil)

	// Build the main account trie.
	accountTrie := newSimpleTrie()

	accCursor, err := tx.Cursor("Account")
	if err != nil {
		return types.Hash{}, err
	}
	defer accCursor.Close()

	stoCursor, err := tx.Cursor("Storage")
	if err != nil {
		return types.Hash{}, err
	}
	defer stoCursor.Close()

	// Pre-position storage cursor.
	stoK, stoV, stoErr := stoCursor.First()

	for k, v, err := accCursor.First(); k != nil; k, v, err = accCursor.Next() {
		if err != nil {
			return types.Hash{}, err
		}
		if len(k) != 20 {
			continue
		}

		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			return types.Hash{}, err
		}
		if acc.CodeHash == (types.Hash{}) {
			acc.CodeHash = emptyCodeHash
		}

		// Build storage trie for this account.
		storageTrie := newSimpleTrie()
		hasStorage := false
		for stoK != nil && stoErr == nil {
			if len(stoK) < 20 {
				stoK, stoV, stoErr = stoCursor.Next()
				continue
			}
			cmp := compareBytes(stoK[:20], k[:20])
			if cmp < 0 {
				stoK, stoV, stoErr = stoCursor.Next()
				continue
			}
			if cmp > 0 {
				break
			}
			// Same address — add to storage trie.
			if len(stoK) >= 52 && len(stoV) > 0 {
				slotHash := crypto.Keccak256(stoK[20:52])
				// Storage trie value = RLP(trimmed_value)
				valRLP, _ := rlp.EncodeToBytes(stoV)
				storageTrie.update(slotHash, valRLP)
				hasStorage = true
			}
			stoK, stoV, stoErr = stoCursor.Next()
		}

		var storageRoot types.Hash
		if hasStorage {
			storageRoot = storageTrie.hash()
		} else {
			storageRoot = emptyRoot
		}
		acc.Root = storageRoot

		// RLP encode the account for the main trie.
		accRLP := encodeAccountRLP(&acc)

		addrHash := crypto.Keccak256(k[:20])
		accountTrie.update(addrHash, accRLP)
	}

	root := accountTrie.hash()

	// Debug: dump precompile addresses if present.
	if os.Getenv("DUMP_TRIE") == "1" {
		for i := 0; i <= 8; i++ {
			var addr types.Address
			addr[19] = byte(i)
			v, _ := tx.GetOne("Account", addr[:])
			if v != nil {
				var a account.StateAccount
				a.DecodeForStorage(v)
				fmt.Printf("  precompile 0x%02x: nonce=%d bal=%s code=%x\n",
					i, a.Nonce, a.Balance.String(), a.CodeHash[:4])
			}
		}
	}

	return root, nil
}

func encodeAccountRLP(acc *account.StateAccount) []byte {
	data, _ := rlp.EncodeToBytes([]interface{}{
		acc.Nonce,
		acc.Balance.ToBig(),
		acc.Root,
		acc.CodeHash,
	})
	return data
}

func compareBytes(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}
