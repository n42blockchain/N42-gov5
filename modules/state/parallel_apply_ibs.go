// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// parallel_apply_ibs.go — apply a Block-STM BlockCommit into an IntraBlockState.
//
// This is the commit-side bridge for running the parallel EVM on the
// hashed-canonical path: after ExecuteBlockParallel + FinalizeBlock produce a
// deterministic BlockCommit (the block's net per-key state deltas), this replays
// those deltas into a fresh IntraBlockState so the EXISTING commit machinery —
// ibs.IntermediateRoot() (writeOnly TrieRootComputer → HashedAccounts/HashedStorage)
// + ibs.CommitBlock(HashedCanonicalWriter) (changesets + code) + WriteChangeSets —
// produces byte-identical state + changesets as the serial ApplyTransaction loop.
// Reusing that path (vs writing hashed tables directly) keeps the account/storage
// encoding and changeset format guaranteed-consistent with the serial baseline.

package state

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// ApplyBlockCommitToIBS replays bc's net deltas into ibs. ibs MUST be built on
// the block's pre-state (e.g. NewHashedStateReader(tx)) so changeset pre-values
// are correct. Iterates bc.Writes directly (not via the ApplyTarget interface)
// to control ordering: gather codes first (to link new-contract code to its
// account via SetCode), apply wipes before account/storage (so a recreated
// account starts from cleared storage).
func ApplyBlockCommitToIBS(bc *BlockCommit, ibs *IntraBlockState) error {
	// 1. New-contract code, keyed by codeHash. Only codes WRITTEN this block
	//    appear here; an account whose codeHash is absent from this map has
	//    unchanged code (already persisted) and must NOT be re-SetCode'd.
	codeByHash := make(map[types.Hash][]byte)
	for _, e := range bc.Writes {
		if len(e.Key) == 33 && e.Key[0] == mvKeyTagCode {
			var ch types.Hash
			copy(ch[:], e.Key[1:])
			codeByHash[ch] = e.Value
		}
	}

	// 2. Wipes (SELFDESTRUCT / CREATE-on-existing). Selfdestruct clears the
	//    account's storage at commit time. Rare post-EIP-6780.
	wiped := make(map[types.Address]struct{})
	for _, e := range bc.Writes {
		if len(e.Key) == 21 && e.Key[0] == mvKeyTagWipe {
			var addr types.Address
			copy(addr[:], e.Key[1:])
			ibs.Selfdestruct(addr)
			wiped[addr] = struct{}{}
		}
	}

	// 3. Accounts (balance/nonce/code). nil value = deletion.
	for _, e := range bc.Writes {
		if len(e.Key) != 21 || e.Key[0] != mvKeyTagAccount {
			continue
		}
		var addr types.Address
		copy(addr[:], e.Key[1:])
		if len(e.Value) == 0 {
			ibs.Selfdestruct(addr)
			continue
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(e.Value); err != nil {
			return fmt.Errorf("ApplyBlockCommitToIBS: decode account %x: %w", addr, err)
		}
		if _, w := wiped[addr]; w {
			// Metamorphic recreate: reset the wiped object before re-setting.
			ibs.CreateAccount(addr, true)
		}
		bal := a.Balance
		ibs.SetBalance(addr, &bal)
		ibs.SetNonce(addr, a.Nonce)
		if code, ok := codeByHash[a.CodeHash]; ok {
			ibs.SetCode(addr, code)
		}
	}

	// 4. Storage.
	for _, e := range bc.Writes {
		if len(e.Key) != 53 || e.Key[0] != mvKeyTagStorage {
			continue
		}
		var addr types.Address
		var slot types.Hash
		copy(addr[:], e.Key[1:21])
		copy(slot[:], e.Key[21:])
		var val uint256.Int
		if len(e.Value) > 0 {
			val.SetBytes(e.Value)
		}
		ibs.SetState(addr, &slot, val)
	}

	// 5. Coinbase tip aggregate: zero pre-lazy-coinbase (the tip is already folded
	//    into the coinbase account write above); non-zero once a coinbase-skipping
	//    writer (P1-C) routes the tip through FinalizeBlock's CoinbaseDelta.
	if bc.CoinbaseDelta != nil && !bc.CoinbaseDelta.IsZero() {
		bal := ibs.GetBalance(bc.CoinbaseAddress)
		nb := new(uint256.Int).Add(bal, bc.CoinbaseDelta)
		ibs.SetBalance(bc.CoinbaseAddress, nb)
	}
	return nil
}
