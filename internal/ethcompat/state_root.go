// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package ethcompat

import (
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
)

// VerifyStateRoot computes the Ethereum state root by rebuilding an MPT from
// the plain Account and Storage tables. It intentionally avoids depending on
// any higher-level internal packages so genesis/block compatibility helpers can
// use it without introducing package cycles.
func VerifyStateRoot(tx kv.Tx) (types.Hash, error) {
	emptyRoot := types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	emptyCodeHash := crypto.Keccak256Hash(nil)

	accountTrie := hash.NewMPTTrie()

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

	stoK, stoV, stoErr := stoCursor.First()

	var acctCount, stoSlotCount uint64
	for k, v, err := accCursor.First(); k != nil; k, v, err = accCursor.Next() {
		if err != nil {
			return types.Hash{}, err
		}
		if len(k) != types.AddressLength {
			continue
		}

		acctCount++
		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			return types.Hash{}, err
		}
		if acc.CodeHash == (types.Hash{}) {
			acc.CodeHash = emptyCodeHash
		}

		storageTrie := hash.NewMPTTrie()
		hasStorage := false
		for stoK != nil && stoErr == nil {
			if len(stoK) < types.AddressLength {
				stoK, stoV, stoErr = stoCursor.Next()
				continue
			}
			cmp := compareBytes(stoK[:types.AddressLength], k[:types.AddressLength])
			if cmp < 0 {
				stoK, stoV, stoErr = stoCursor.Next()
				continue
			}
			if cmp > 0 {
				break
			}
			if len(stoK) >= types.AddressLength+32 && len(stoV) > 0 {
				stoSlotCount++
				slotHash := crypto.Keccak256(stoK[types.AddressLength : types.AddressLength+32])
				valRLP, _ := rlp.EncodeToBytes(stoV)
				storageTrie.Update(slotHash, valRLP)
				hasStorage = true
			}
			stoK, stoV, stoErr = stoCursor.Next()
		}
		if stoErr != nil {
			return types.Hash{}, stoErr
		}

		if hasStorage {
			acc.Root = storageTrie.Hash()
		} else {
			acc.Root = emptyRoot
		}

		accRLP, err := rlp.EncodeToBytes([]interface{}{
			acc.Nonce,
			acc.Balance.ToBig(),
			acc.Root,
			acc.CodeHash,
		})
		if err != nil {
			return types.Hash{}, err
		}
		accountTrie.Update(crypto.Keccak256(k[:types.AddressLength]), accRLP)
	}

	root := accountTrie.Hash()
	if os.Getenv("DUMP_TRIE") == "1" {
		for i := 0; i <= 8; i++ {
			var addr types.Address
			addr[types.AddressLength-1] = byte(i)
			v, _ := tx.GetOne("Account", addr[:])
			if v != nil {
				var a account.StateAccount
				a.DecodeForStorage(v)
				fmt.Printf("  precompile 0x%02x: nonce=%d bal=%s code=%x\n",
					i, a.Nonce, a.Balance.String(), a.CodeHash[:4])
			}
		}
	}

	if os.Getenv("DUMP_STATE_STATS") == "1" {
		fmt.Fprintf(os.Stderr, "STATE_STATS accts=%d stoSlots=%d root=%s\n",
			acctCount, stoSlotCount, root.Hex())
	}

	return root, nil
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
