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

package txspool

import (
	"container/heap"
	"sort"
	"sync"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// --- Account set ---

// accountSet is a set of addresses to check for existence.
type accountSet struct {
	accounts map[types.Address]struct{}
	cache    *[]types.Address
}

func newAccountSet(addrs ...types.Address) *accountSet {
	as := &accountSet{
		accounts: make(map[types.Address]struct{}),
	}
	for _, addr := range addrs {
		as.add(addr)
	}
	return as
}

func (as *accountSet) contains(addr types.Address) bool {
	_, exist := as.accounts[addr]
	return exist
}

func (as *accountSet) empty() bool {
	return len(as.accounts) == 0
}

func (as *accountSet) containsTx(tx *transaction.Transaction) bool {
	from := tx.From()
	if from == nil {
		return false
	}
	return as.contains(*from)
}

func (as *accountSet) add(addr types.Address) {
	as.accounts[addr] = struct{}{}
	as.cache = nil
}

func (as *accountSet) addTx(tx *transaction.Transaction) {
	from := tx.From()
	if from == nil {
		return
	}
	as.add(*from)
}

func (as *accountSet) flatten() []types.Address {
	if as.cache == nil {
		accounts := make([]types.Address, 0, len(as.accounts))
		for account := range as.accounts {
			accounts = append(accounts, account)
		}
		as.cache = &accounts
	}
	return *as.cache
}

func (as *accountSet) merge(other *accountSet) {
	for addr := range other.accounts {
		as.accounts[addr] = struct{}{}
	}
	as.cache = nil
}

// --- Transaction lookup ---

// txLookup is used internally by TxPool to track transactions while allowing
// lookup without mutex contention.
type txLookup struct {
	slots   int
	lock    sync.RWMutex
	locals  map[types.Hash]*transaction.Transaction
	remotes map[types.Hash]*transaction.Transaction
}

func newTxLookup() *txLookup {
	return &txLookup{
		locals:  make(map[types.Hash]*transaction.Transaction),
		remotes: make(map[types.Hash]*transaction.Transaction),
	}
}

// Range calls f on each key and value present in the map.
func (t *txLookup) Range(f func(hash types.Hash, tx *transaction.Transaction, local bool) bool, local bool, remote bool) {
	t.lock.RLock()
	defer t.lock.RUnlock()

	if local {
		for key, value := range t.locals {
			if !f(key, value, true) {
				return
			}
		}
	}
	if remote {
		for key, value := range t.remotes {
			if !f(key, value, false) {
				return
			}
		}
	}
}

func (t *txLookup) Get(hash types.Hash) *transaction.Transaction {
	t.lock.RLock()
	defer t.lock.RUnlock()

	if tx := t.locals[hash]; tx != nil {
		return tx
	}
	return t.remotes[hash]
}

func (t *txLookup) GetLocal(hash types.Hash) *transaction.Transaction {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.locals[hash]
}

func (t *txLookup) GetRemote(hash types.Hash) *transaction.Transaction {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.remotes[hash]
}

func (t *txLookup) Count() int {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return len(t.locals) + len(t.remotes)
}

func (t *txLookup) LocalCount() int {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return len(t.locals)
}

func (t *txLookup) RemoteCount() int {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return len(t.remotes)
}

func (t *txLookup) Slots() int {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.slots
}

func (t *txLookup) Add(tx *transaction.Transaction, local bool) {
	t.lock.Lock()
	defer t.lock.Unlock()

	t.slots += numSlots(tx)
	hash := tx.Hash()
	if local {
		t.locals[hash] = tx
	} else {
		t.remotes[hash] = tx
	}
}

func (t *txLookup) Remove(hash types.Hash) {
	t.lock.Lock()
	defer t.lock.Unlock()

	tx, ok := t.locals[hash]
	if !ok {
		tx, ok = t.remotes[hash]
	}
	if !ok {
		return
	}
	t.slots -= numSlots(tx)
	delete(t.locals, hash)
	delete(t.remotes, hash)
}

// RemoteToLocals migrates transactions belonging to the given locals to the locals set.
func (t *txLookup) RemoteToLocals(locals *accountSet) int {
	t.lock.Lock()
	defer t.lock.Unlock()

	var migrated int
	for hash, tx := range t.remotes {
		if locals.containsTx(tx) {
			t.locals[hash] = tx
			delete(t.remotes, hash)
			migrated++
		}
	}
	return migrated
}

// RemotesBelowTip finds all remote transactions below the given tip threshold.
func (t *txLookup) RemotesBelowTip(threshold uint256.Int) []*transaction.Transaction {
	found := make([]*transaction.Transaction, 0, 128)
	t.Range(func(hash types.Hash, tx *transaction.Transaction, local bool) bool {
		if tx.GasPrice().Cmp(&threshold) < 0 {
			found = append(found, tx)
		}
		return true
	}, false, true)
	return found
}

// --- Sort and heap types ---

// numSlots calculates the number of slots needed for a single transaction.
func numSlots(tx *transaction.Transaction) int {
	txData, err := tx.Marshal()
	if err != nil {
		return 1
	}
	return (len(txData) + txSlotSize - 1) / txSlotSize
}

// TxByNonce implements the sort interface to allow sorting transactions by nonce.
type TxByNonce []*transaction.Transaction

func (s TxByNonce) Len() int           { return len(s) }
func (s TxByNonce) Less(i, j int) bool { return s[i].Nonce() < s[j].Nonce() }
func (s TxByNonce) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

// nonceHeap is a heap.Interface implementation over 64bit unsigned integers for
// retrieving sorted transactions from the possibly gapped future queue.
type nonceHeap []uint64

func (h nonceHeap) Len() int           { return len(h) }
func (h nonceHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h nonceHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *nonceHeap) Push(x interface{}) {
	*h = append(*h, x.(uint64))
}

func (h *nonceHeap) Pop() interface{} {
	old := *h
	n := len(old)
	if n == 0 {
		return nil
	}
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

// --- Nonce-sorted transaction map ---

// txsSortedMap is a nonce->transaction hash map with a heap-based index.
type txsSortedMap struct {
	items map[uint64]*transaction.Transaction
	index *nonceHeap
	cache []*transaction.Transaction
}

func newTxSortedMap() *txsSortedMap {
	return &txsSortedMap{
		items: make(map[uint64]*transaction.Transaction),
		index: new(nonceHeap),
	}
}

func (m *txsSortedMap) Get(nonce uint64) *transaction.Transaction {
	return m.items[nonce]
}

func (m *txsSortedMap) Put(tx *transaction.Transaction) {
	nonce := tx.Nonce()
	if m.items[nonce] == nil {
		heap.Push(m.index, nonce)
	}
	m.items[nonce], m.cache = tx, nil
}

func (m *txsSortedMap) Forward(threshold uint64) []*transaction.Transaction {
	var removed []*transaction.Transaction
	for m.index.Len() > 0 && (*m.index)[0] < threshold {
		nonce := heap.Pop(m.index).(uint64)
		removed = append(removed, m.items[nonce])
		delete(m.items, nonce)
	}
	if m.cache != nil {
		m.cache = m.cache[len(removed):]
	}
	return removed
}

func (m *txsSortedMap) Filter(filter func(*transaction.Transaction) bool) []*transaction.Transaction {
	removed := m.filter(filter)
	if len(removed) > 0 {
		m.reheap()
	}
	return removed
}

func (m *txsSortedMap) reheap() {
	*m.index = make([]uint64, 0, len(m.items))
	for nonce := range m.items {
		*m.index = append(*m.index, nonce)
	}
	heap.Init(m.index)
	m.cache = nil
}

func (m *txsSortedMap) filter(filter func(*transaction.Transaction) bool) []*transaction.Transaction {
	var removed []*transaction.Transaction
	for nonce, tx := range m.items {
		if filter(tx) {
			removed = append(removed, tx)
			delete(m.items, nonce)
		}
	}
	if len(removed) > 0 {
		m.cache = nil
	}
	return removed
}

func (m *txsSortedMap) Cap(threshold int) []*transaction.Transaction {
	if len(m.items) <= threshold {
		return nil
	}
	var drops []*transaction.Transaction

	sort.Sort(*m.index)
	for size := len(m.items); size > threshold; size-- {
		drops = append(drops, m.items[(*m.index)[size-1]])
		delete(m.items, (*m.index)[size-1])
	}
	*m.index = (*m.index)[:threshold]
	heap.Init(m.index)

	if m.cache != nil {
		m.cache = m.cache[:len(m.cache)-len(drops)]
	}
	return drops
}

// Remove deletes a transaction with the given nonce from the map.
func (m *txsSortedMap) Remove(nonce uint64) bool {
	if _, ok := m.items[nonce]; !ok {
		return false
	}
	delete(m.items, nonce)
	m.cache = nil

	// For large heaps, rebuild is more efficient than linear search.
	if m.index.Len() > 256 {
		m.reheap()
	} else {
		for i := 0; i < m.index.Len(); i++ {
			if (*m.index)[i] == nonce {
				heap.Remove(m.index, i)
				break
			}
		}
	}
	return true
}

// Ready retrieves a sequentially increasing list of transactions starting at the
// provided nonce that is ready for processing. The returned transactions will be
// removed from the list.
func (m *txsSortedMap) Ready(start uint64) []*transaction.Transaction {
	if m.index.Len() == 0 || (*m.index)[0] > start {
		return nil
	}
	var ready []*transaction.Transaction
	for next := (*m.index)[0]; m.index.Len() > 0 && (*m.index)[0] == next; next++ {
		ready = append(ready, m.items[next])
		delete(m.items, next)
		heap.Pop(m.index)
	}
	m.cache = nil
	return ready
}

func (m *txsSortedMap) Len() int {
	return len(m.items)
}

func (m *txsSortedMap) flatten() []*transaction.Transaction {
	if m.cache == nil {
		m.cache = make([]*transaction.Transaction, 0, len(m.items))
		for _, tx := range m.items {
			m.cache = append(m.cache, tx)
		}
		sort.Sort(TxByNonce(m.cache))
	}
	return m.cache
}

func (m *txsSortedMap) Flatten() []*transaction.Transaction {
	cache := m.flatten()
	txs := make([]*transaction.Transaction, len(cache))
	copy(txs, cache)
	return txs
}

// LastElement returns the transaction with the highest nonce, or nil if empty.
func (m *txsSortedMap) LastElement() *transaction.Transaction {
	cache := m.flatten()
	if len(cache) == 0 {
		return nil
	}
	return cache[len(cache)-1]
}

// --- Price heap ---

// priceHeap is a heap.Interface implementation over transactions for retrieving
// price-sorted transactions to discard when the pool fills up.
type priceHeap struct {
	baseFee *uint256.Int
	list    []*transaction.Transaction
}

func (h *priceHeap) Len() int      { return len(h.list) }
func (h *priceHeap) Swap(i, j int) { h.list[i], h.list[j] = h.list[j], h.list[i] }

func (h *priceHeap) Less(i, j int) bool {
	switch h.cmp(h.list[i], h.list[j]) {
	case -1:
		return true
	case 1:
		return false
	default:
		return h.list[i].Nonce() > h.list[j].Nonce()
	}
}

func (h *priceHeap) cmp(a, b *transaction.Transaction) int {
	if h.baseFee != nil {
		if c := a.EffectiveGasTipCmp(b, h.baseFee); c != 0 {
			return c
		}
	}
	if c := a.GasPrice().Cmp(b.GasPrice()); c != 0 {
		return c
	}
	return a.GasPrice().Cmp(b.GasPrice())
}

func (h *priceHeap) Push(x interface{}) {
	h.list = append(h.list, x.(*transaction.Transaction))
}

func (h *priceHeap) Pop() interface{} {
	old := h.list
	n := len(old)
	if n == 0 {
		return nil
	}
	x := old[n-1]
	old[n-1] = nil
	h.list = old[0 : n-1]
	return x
}
