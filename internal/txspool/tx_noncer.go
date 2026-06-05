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
// txNoncer: virtual account nonce cache the pool uses to reason
// about pending chains of transactions without hitting the
// underlying state DB on every lookup. Backed by a sync.Mutex map
// from address to the next pending nonce, refreshed from the state
// reader on demand.

package txspool

import (
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

// txNoncer is a virtual state database to track the pool nonces.
type txNoncer struct {
	fallback ReadState
	nonces   map[types.Address]uint64
	lock     sync.Mutex
}

func newTxNoncer(db ReadState) *txNoncer {
	return &txNoncer{
		fallback: db,
		nonces:   make(map[types.Address]uint64),
	}
}

// get returns the current nonce of an account, falling back to a real state
// database if the account is unknown.
func (txn *txNoncer) get(addr types.Address) uint64 {
	txn.lock.Lock()
	defer txn.lock.Unlock()

	if _, ok := txn.nonces[addr]; !ok {
		txn.nonces[addr] = txn.fallback.GetNonce(addr)
	}
	return txn.nonces[addr]
}

// getReadOnly returns the pending nonce like get, but does NOT insert an entry
// for an untracked address. Read-only RPC paths (eth_getTransactionCount with
// the "pending" tag → TxsPool.Nonce) take arbitrary caller-supplied addresses;
// using get there lets a flood of queries for unknown addresses balloon the
// nonces map (only bounded by setAll's per-reset replacement, i.e. one block).
// For an untracked sender the pending nonce IS the state nonce, so returning
// fallback without caching is correctness-neutral (a later tx insertion caches
// it properly via set/get). Mirrors reth #23008. Same lock discipline as get
// (so fallback calls stay serialized — no concurrency change).
func (txn *txNoncer) getReadOnly(addr types.Address) uint64 {
	txn.lock.Lock()
	defer txn.lock.Unlock()

	if nonce, ok := txn.nonces[addr]; ok {
		return nonce
	}
	return txn.fallback.GetNonce(addr)
}

func (txn *txNoncer) set(addr types.Address, nonce uint64) {
	txn.lock.Lock()
	defer txn.lock.Unlock()
	txn.nonces[addr] = nonce
}

// setIfLower updates the nonce only if the new one is lower than the existing one.
func (txn *txNoncer) setIfLower(addr types.Address, nonce uint64) {
	txn.lock.Lock()
	defer txn.lock.Unlock()

	if _, ok := txn.nonces[addr]; !ok {
		txn.nonces[addr] = txn.fallback.GetNonce(addr)
	}
	if txn.nonces[addr] <= nonce {
		return
	}
	txn.nonces[addr] = nonce
}

func (txn *txNoncer) setAll(all map[types.Address]uint64) {
	txn.lock.Lock()
	defer txn.lock.Unlock()
	txn.nonces = all
}
