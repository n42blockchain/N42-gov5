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
//
// Applying the multi-version state to the IntraBlockState.

package internal

import (
	"fmt"
	"sync"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/envswitch"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/modules/state"
)

// applyMVSToIBS replays the final validated MVS state to the real IntraBlockState.
// Uses a two-pass approach:
//  1. Process FieldBalance entries to establish account existence/deletion.
//  2. Process FieldStorage and FieldCode for accounts that exist in the final state.
//
// This ordering ensures that storage/code changes are only applied to accounts
// that actually exist, preventing phantom account creation for deleted accounts.
func applyMVSToIBS(mvs *parallel.MVS, numTxs int, ibs *state.IntraBlockState) error {
	_, err := applyMVSToIBSTimed(mvs, numTxs, ibs)
	return err
}

// applyMVSSplit is applyMVSToIBS's split: the final-versions walk, the
// account pass (wipes, accounts, credits), the storage and code passes.
type applyMVSSplit struct {
	walk, accounts, storage time.Duration
}

// applyMVSToIBSTimed is applyMVSToIBS, also returning its split.
func applyMVSToIBSTimed(mvs *parallel.MVS, numTxs int, ibs *state.IntraBlockState) (split applyMVSSplit, err error) {
	t0 := time.Now()
	// Collect all MVS entries, separated by type.
	type accountEntry struct {
		addr  types.Address
		value []byte       // nil = deleted, unless delta is set
		delta *uint256.Int // set when the block only credited the account
	}
	type storageEntry struct {
		addr  types.Address
		slot  types.Hash
		value []byte
	}
	type codeEntry struct {
		addr types.Address
		code []byte
	}

	type collected struct {
		accounts []accountEntry
		storages []storageEntry
		codes    []codeEntry
		wipes    []types.Address
		err      error
	}
	collect := func(c *collected, lo, hi int) {
		c.err = mvs.ApplyShards(numTxs, lo, hi, func(key parallel.LocationKey, value []byte, delta *uint256.Int) error {
			switch key.Field {
			case parallel.FieldBalance:
				// FieldBalance stores the full encoded StateAccount (balance +
				// nonce + codeHash + root), or only a balance delta.
				c.accounts = append(c.accounts, accountEntry{addr: key.Address, value: value, delta: delta})
			case parallel.FieldStorage:
				c.storages = append(c.storages, storageEntry{addr: key.Address, slot: key.Slot, value: value})
			case parallel.FieldCode:
				if value != nil {
					c.codes = append(c.codes, codeEntry{addr: key.Address, code: value})
				}
			case parallel.FieldStorageWipe:
				if value != nil {
					c.wipes = append(c.wipes, key.Address)
				}
			}
			// Other fields (CodeHash, CodeSize, Suicide, Exist, Incarnation) are
			// auxiliary — the IBS handles them internally via SetBalance/SetCode/etc.
			return nil
		})
	}
	// The final versions are read across goroutines over disjoint shard
	// ranges (P10: the serial walk was ~11 ms of a 163k block's collect)
	// and concatenated in shard order, the order of the serial walk.
	parts := make([]collected, applyMVSWorkers(numTxs))
	if len(parts) == 1 {
		collect(&parts[0], 0, parallel.MVSShards)
	} else {
		var wg sync.WaitGroup
		per := (parallel.MVSShards + len(parts) - 1) / len(parts)
		for w := range parts {
			lo, hi := w*per, (w+1)*per
			if hi > parallel.MVSShards {
				hi = parallel.MVSShards
			}
			wg.Add(1)
			go func(c *collected, lo, hi int) {
				defer wg.Done()
				collect(c, lo, hi)
			}(&parts[w], lo, hi)
		}
		wg.Wait()
	}
	var accounts []accountEntry
	var storages []storageEntry
	var codes []codeEntry
	var wipes []types.Address
	if len(parts) == 1 {
		accounts, storages, codes, wipes = parts[0].accounts, parts[0].storages, parts[0].codes, parts[0].wipes
	} else {
		na, ns := 0, 0
		for i := range parts {
			na += len(parts[i].accounts)
			ns += len(parts[i].storages)
		}
		accounts = make([]accountEntry, 0, na)
		storages = make([]storageEntry, 0, ns)
		for i := range parts {
			accounts = append(accounts, parts[i].accounts...)
			storages = append(storages, parts[i].storages...)
			codes = append(codes, parts[i].codes...)
			wipes = append(wipes, parts[i].wipes...)
		}
	}
	for i := range parts {
		if parts[i].err != nil {
			return split, parts[i].err
		}
	}
	tWalk := time.Now()
	split.walk = tWalk.Sub(t0)

	full := 0
	for i := range accounts {
		if accounts[i].delta == nil {
			full++
		}
	}
	ibs.Reserve(full+len(storages)+len(codes), len(accounts)-full)

	// Track which accounts are deleted in the final state.
	deletedAccounts := make(map[types.Address]bool)
	for _, ae := range accounts {
		if ae.value == nil && ae.delta == nil {
			deletedAccounts[ae.addr] = true
		}
	}

	// Pass 0: storage wipes for addresses that are ALIVE in the final state
	// (recreate-after-SELFDESTRUCT / CREATE-on-existing). A plain SELFDESTRUCT
	// ends deleted and has its storage cleared by Selfdestruct in Pass 1, so
	// skip those here. CreateAccount(addr, true) preserves the balance and marks
	// the address in storageWipes, so CommitBlock clears the stale base slots
	// before the post-wipe slots from Pass 2 are written. Must run before Pass 1
	// (account) and Pass 2 (storage) so those values land on the recreated
	// object rather than being discarded by the wipe.
	for _, addr := range wipes {
		if deletedAccounts[addr] {
			continue
		}
		ibs.CreateAccount(addr, true)
	}

	// Pass 1: Apply account-level changes. A large block's entries go
	// through ApplyAccounts (P13c), which prepares the written accounts'
	// objects across goroutines and leaves the same serial sequence of
	// map inserts and journal records the loop below makes; a decode
	// error falls back to the loop, which reports it where it always did.
	pass1 := accounts
	if w := applyMVSWorkers(numTxs); applyBulkEnabled && w > 1 && len(accounts) >= applyBulkMinAccounts {
		if bulk, ok := decodeAccountApplies(len(accounts), w, func(i int) (types.Address, []byte, *uint256.Int) {
			return accounts[i].addr, accounts[i].value, accounts[i].delta
		}); ok {
			if collectShardWorkers > 1 {
				ibs.ApplyAccountsSharded(bulk, collectShardWorkers)
			} else {
				ibs.ApplyAccounts(bulk, w)
			}
			pass1 = nil
		}
	}
	for _, ae := range pass1 {
		if ae.delta != nil {
			// The block only credited the account: add onto the base value,
			// which is the same deferred increment the serial path takes.
			ibs.AddBalance(ae.addr, ae.delta)
			continue
		}
		if ae.value == nil {
			// Account was deleted (selfdestructed).
			// Selfdestruct handles accounts that exist in base state;
			// for accounts created and deleted within the same block
			// (not in base state), Selfdestruct is a safe no-op.
			ibs.Selfdestruct(ae.addr)
		} else {
			acc, err := parallel.DecodeAccount(ae.value)
			if err != nil {
				return split, fmt.Errorf("decode account %v: %w", ae.addr, err)
			}
			ibs.SetBalance(ae.addr, &acc.Balance)
			ibs.SetNonce(ae.addr, acc.Nonce)
			// incarnation removed from StateAccount
		}
	}

	tAccounts := time.Now()
	split.accounts = tAccounts.Sub(tWalk)

	// Pass 2: Apply storage changes (skip deleted accounts).
	for _, se := range storages {
		if deletedAccounts[se.addr] {
			continue // storage of deleted accounts is discarded
		}
		var val uint256.Int
		if len(se.value) > 0 {
			val.SetBytes(se.value)
		}
		slot := se.slot
		ibs.SetState(se.addr, &slot, val)
	}

	// Pass 3: Apply code changes (skip deleted accounts).
	for _, ce := range codes {
		if deletedAccounts[ce.addr] {
			continue
		}
		ibs.SetCode(ce.addr, ce.code)
	}
	split.storage = time.Since(tAccounts)

	return split, nil
}

// applyBulkEnabled gates applyMVSToIBS's bulk account pass (P13c);
// N42_APPLY_BULK=0 restores the per-entry loop.
var applyBulkEnabled = envswitch.Not0("N42_APPLY_BULK", envswitch.Perf, "Default on: bulk account pass of applyMVSToIBS; 0 restores the per-entry loop.")

// applyBulkMinAccounts is the account-entry count from which the bulk
// pass is taken.
const applyBulkMinAccounts = 4096

// decodeAccountApplies turns n account entries (entry(i): address, encoded
// final account or nil for deleted, credit delta) into ApplyAccounts'
// form across workers goroutines. ok is false when an encoding does not
// decode.
func decodeAccountApplies(n, workers int, entry func(int) (types.Address, []byte, *uint256.Int)) ([]state.AccountApply, bool) {
	out := make([]state.AccountApply, n)
	bad := make([]bool, workers)
	per := (n + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > n {
			hi = n
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			var acc account.StateAccount
			for i := lo; i < hi; i++ {
				addr, value, delta := entry(i)
				o := &out[i]
				o.Addr = addr
				switch {
				case delta != nil:
					o.Delta = delta
				case value == nil:
					o.Delete = true
				default:
					acc = account.StateAccount{}
					if err := acc.DecodeForStorageV2(value); err != nil {
						bad[w] = true
						return
					}
					o.Balance = acc.Balance
					o.Nonce = acc.Nonce
				}
			}
		}(w, lo, hi)
	}
	wg.Wait()
	for _, b := range bad {
		if b {
			return nil, false
		}
	}
	return out, true
}
