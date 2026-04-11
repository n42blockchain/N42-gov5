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
