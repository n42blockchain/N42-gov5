// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"bytes"
	"fmt"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/trie"
)

// VerifyAccountsAgainstReth compares a sample of accounts from the N42 MDBX
// against the Reth MDBX database. Returns the number of matched accounts
// and any mismatches found.
//
// Reth stores accounts in "PlainAccountState" table with key=address(20B),
// value=Reth Compact encoding. N42 stores in "Account" table with key=address(20B),
// value=Erigon V2 encoding. We compare decoded field values.
func VerifyAccountsAgainstReth(n42Tx kv.Tx, rethTx kv.Tx, sampleSize int) (matched int, mismatches []string, err error) {
	// Iterate N42 accounts and compare against Reth.
	cursor, err := n42Tx.Cursor("Account")
	if err != nil {
		return 0, nil, fmt.Errorf("open Account cursor: %w", err)
	}
	defer cursor.Close()

	checked := 0
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return matched, mismatches, err
		}
		if checked >= sampleSize {
			break
		}
		if len(k) != 20 {
			continue // skip non-address keys
		}

		addr := types.BytesToAddress(k)

		// Decode N42 account (Erigon V2).
		var n42Acc account.StateAccount
		if err := n42Acc.DecodeForStorage(v); err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: n42 decode error: %v", addr.Hex(), err))
			checked++
			continue
		}

		// Read from Reth.
		rethVal, err := rethTx.GetOne("PlainAccountState", k)
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: reth read error: %v", addr.Hex(), err))
			checked++
			continue
		}
		if rethVal == nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: exists in n42 but not in reth", addr.Hex()))
			checked++
			continue
		}

		// Decode Reth account (Compact format).
		rethNonce, rethBalance, rethCodeHash, err := decodeRethCompactAccount(rethVal)
		if err != nil {
			mismatches = append(mismatches, fmt.Sprintf("%s: reth decode error: %v", addr.Hex(), err))
			checked++
			continue
		}

		// Compare fields.
		mismatch := false
		if n42Acc.Nonce != rethNonce {
			mismatches = append(mismatches, fmt.Sprintf("%s: nonce mismatch n42=%d reth=%d", addr.Hex(), n42Acc.Nonce, rethNonce))
			mismatch = true
		}
		if !mismatch && n42Acc.Balance.Cmp(rethBalance) != 0 {
			mismatches = append(mismatches, fmt.Sprintf("%s: balance mismatch n42=%v reth=%v", addr.Hex(), &n42Acc.Balance, rethBalance))
			mismatch = true
		}
		emptyHash := types.Hash{}
		if !mismatch && n42Acc.CodeHash != emptyHash && rethCodeHash != nil && !bytes.Equal(n42Acc.CodeHash.Bytes(), rethCodeHash) {
			mismatches = append(mismatches, fmt.Sprintf("%s: codeHash mismatch", addr.Hex()))
			mismatch = true
		}
		if !mismatch {
			matched++
		}
		checked++
	}

	return matched, mismatches, nil
}

// decodeRethCompactAccount decodes Reth's Compact account encoding.
// Format: bitfield(1B) + [nonce(varint)] + [balance(len+bytes)] + [bytecodeHash(32B)]
//
// Bitfield:
//   bit 0: has nonce
//   bit 1: has balance
//   bit 2: has bytecode_hash
func decodeRethCompactAccount(data []byte) (nonce uint64, balance *uint256.Int, codeHash []byte, err error) {
	// Reth Compact account is quite different from Erigon V2.
	// For a basic comparison, we try a simplified decoder.
	// This handles the common cases but may not cover all edge cases.

	if len(data) == 0 {
		return 0, new(uint256.Int), nil, nil
	}

	// Reth stores: nonce(u64 LE, compacted) + balance(U256 LE, compacted) + bytecode_hash(Option<B256>)
	// The Compact format uses a length prefix for variable-length fields.
	// For now, return a placeholder — full Reth Compact decoding requires
	// understanding the exact varint format.

	// TODO: Implement full Reth Compact account decoder.
	// For state root verification, we should use HPH instead of per-account comparison.
	return 0, new(uint256.Int), nil, fmt.Errorf("reth compact decoder not yet implemented")
}

// HashStateAndCalcRoot populates HashedAccounts/HashedStorage from PlainState,
// then computes the MPT state root using CalcTrieRoot.
// This is a full (non-incremental) computation — expensive but definitive.
func HashStateAndCalcRoot(rwTx kv.RwTx) (types.Hash, error) {
	// Phase 1: Hash all accounts from "Account" → "HashedAccounts"
	accCursor, err := rwTx.Cursor("Account")
	if err != nil {
		return types.Hash{}, fmt.Errorf("open Account cursor: %w", err)
	}
	defer accCursor.Close()

	accCount := 0
	for k, v, err := accCursor.First(); k != nil; k, v, err = accCursor.Next() {
		if err != nil {
			return types.Hash{}, err
		}
		if len(k) != 20 {
			continue
		}
		// keccak256(address)
		hashedKey := crypto.Keccak256(k)

		// Decode to normalize CodeHash.
		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			return types.Hash{}, fmt.Errorf("decode account: %w", err)
		}
		emptyHash := types.Hash{}
		if acc.CodeHash == emptyHash {
			acc.CodeHash = crypto.Keccak256Hash(nil)
		}
		enc := acc.MarshalV2()

		if err := rwTx.Put(kv.HashedAccounts, hashedKey, enc); err != nil {
			return types.Hash{}, fmt.Errorf("put HashedAccounts: %w", err)
		}
		accCount++
	}

	// Phase 2: Hash all storage from "Storage" → "HashedStorage"
	stoCursor, err := rwTx.Cursor("Storage")
	if err != nil {
		return types.Hash{}, fmt.Errorf("open Storage cursor: %w", err)
	}
	defer stoCursor.Close()

	stoCount := 0
	for k, v, err := stoCursor.First(); k != nil; k, v, err = stoCursor.Next() {
		if err != nil {
			return types.Hash{}, err
		}
		// Storage key = address(20) + incarnation(2) + storageKey(32) = 54 bytes
		if len(k) < 54 {
			continue
		}
		addrHash := crypto.Keccak256(k[:20])
		incarnation := k[20:22] // 2-byte incarnation
		slotHash := crypto.Keccak256(k[22:54])

		// HashedStorage uses DupSort with AutoDupSortKeysConversion:
		// Full key (72B) = addressHash(32) + incarnation(8) + slotHash(32)
		// MDBX auto-converts to: key(40) = addressHash(32) + incarnation(8), dup(32) = slotHash(32)
		// value = storage value
		incBuf := make([]byte, 8)
		copy(incBuf[6:8], incarnation) // 2-byte incarnation → 8-byte BE

		hashedKey := make([]byte, 72)
		copy(hashedKey[0:32], addrHash)
		copy(hashedKey[32:40], incBuf)
		copy(hashedKey[40:72], slotHash)

		if err := rwTx.Put(kv.HashedStorage, hashedKey, v); err != nil {
			return types.Hash{}, fmt.Errorf("put HashedStorage: %w", err)
		}
		stoCount++
	}

	fmt.Printf("HashState: %d accounts, %d storage slots\n", accCount, stoCount)

	// Phase 3: CalcTrieRoot using FlatDBTrieLoader.
	loader := trie.NewFlatDBTrieLoader("ethel-verify", trie.NewRetainList(0), nil, nil, false)
	root, err := loader.CalcTrieRoot(rwTx, nil)
	if err != nil {
		return types.Hash{}, fmt.Errorf("CalcTrieRoot: %w", err)
	}

	return root, nil
}
