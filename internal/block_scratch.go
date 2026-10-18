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

package internal

import (
	"os"
	"sync"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal/parallel"
)

// Per-block scratch reused across blocks (r31a).
//
// At ~900k transfers a second a follower allocated ~3 KB per transaction,
// and with a 4-5 GB live heap under GOGC=100 the collector's frequency is
// set by those bytes. A block's scratch -- the run's preparation, the
// multi-version store's version lists, the base prefill's key lists -- is
// dead once the block's run is over; kept in small free lists it is
// reused by the next block instead of allocated again. Each list is
// pointer-free or cleared before it is kept, so a kept list costs the
// collector no marking (or only its cleared words) and retains nothing of
// the block that used it.
//
// N42_BLOCK_SCRATCH_POOL=0 restores the old allocation (default on).
var blockScratchPool = os.Getenv("N42_BLOCK_SCRATCH_POOL") != "0"

// SetBlockScratchPoolForTest switches the per-block scratch reuse (here and
// in internal/parallel) and returns the restore.
func SetBlockScratchPoolForTest(on bool) func() {
	prev := blockScratchPool
	blockScratchPool = on
	restoreParallel := parallel.SetScratchPoolForTest(on)
	return func() { blockScratchPool = prev; restoreParallel() }
}

// scratchFree keeps up to max released slices of T for reuse. Released
// slices are cleared (off the caller's goroutine when async) before they
// are kept, so take returns zeroed memory. When full, a released slice
// replaces the smallest kept one if it is larger: the list converges on
// the largest blocks seen (the executor arena's lesson, round 35zzh).
type scratchFree[T any] struct {
	mu   sync.Mutex
	list [][]T
	max  int
	// noZero: kept slices are not cleared and take returns them as they
	// are (only for pointer-free T whose users write before they read).
	noZero bool
}

// take returns a zeroed slice of length n (unless noZero), reused when a
// kept one is large enough.
func (f *scratchFree[T]) take(n int) []T {
	if blockScratchPool {
		f.mu.Lock()
		for i := len(f.list) - 1; i >= 0; i-- {
			if s := f.list[i]; cap(s) >= n {
				f.list = append(f.list[:i], f.list[i+1:]...)
				f.mu.Unlock()
				return s[:n]
			}
		}
		f.mu.Unlock()
	}
	return make([]T, n)
}

// give clears s (to its capacity) and keeps it. The caller must hold no
// reference into s afterwards.
func (f *scratchFree[T]) give(s []T) {
	if !blockScratchPool || cap(s) == 0 {
		return
	}
	if !f.noZero {
		s = s[:cap(s)]
		clear(s)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.list) < f.max {
		f.list = append(f.list, s[:0])
		return
	}
	smallest := 0
	for i, k := range f.list {
		if cap(k) < cap(f.list[smallest]) {
			smallest = i
		}
	}
	if cap(f.list[smallest]) < cap(s) {
		f.list[smallest] = s[:0]
	}
}

// preparedFree keeps released preparations' transaction arrays: one for
// the block being imported, one prepared ahead, one for a leader's build.
var preparedFree = scratchFree[preparedTx]{max: 3}

// preparedSet is a block's preparedTx per transaction, and the messages
// the run cannot rebuild cheaply.
//
// A preparedTx used to carry the transaction's message (288 bytes, with
// pointers): 416 bytes a transaction, fresh every block, ~400 bytes of the
// ~3 KB a follower allocated per transaction. A direct transfer reads only
// its plan and hash, so the message of a planned transfer is not kept
// (when the plan path is on): on the rare decline the worker rebuilds it
// from the transaction, the same message (AsMessageInto and
// NormalizeExecutionMessage are pure functions of the transaction, signer,
// base fee and modes the preparation used). Every other transaction keeps
// its message in msgs, allocated on the first one. The transaction array
// is pointer-free, 128 bytes a transaction, and reused across blocks.
type preparedSet struct {
	txs []preparedTx
	// keepAll: every prepared message is kept (the plan path is off, or
	// the scratch pool is: the old layout's behaviour).
	keepAll  bool
	msgsOnce sync.Once
	msgs     []transaction.Message
}

// newPreparedSet returns an empty preparation for n transactions.
func newPreparedSet(n int) *preparedSet {
	return &preparedSet{
		txs:     preparedFree.take(n),
		keepAll: !blockScratchPool || !(transferFast2 && transferFastPath && transferDirectPath),
	}
}

// keepMsg reports whether a prepared transaction's message is kept.
func (ps *preparedSet) keepMsg(pm *preparedTx) bool {
	return ps.keepAll || !pm.plan.ok
}

// msgSlot returns transaction i's message slot, allocating the messages on
// first use (safe from concurrent key goroutines).
func (ps *preparedSet) msgSlot(i int) *transaction.Message {
	ps.msgsOnce.Do(func() { ps.msgs = make([]transaction.Message, len(ps.txs)) })
	return &ps.msgs[i]
}

// keptMsg returns transaction i's kept message, or nil.
func (ps *preparedSet) keptMsg(i int) *transaction.Message {
	if !ps.txs[i].msgKept {
		return nil
	}
	return &ps.msgs[i]
}

// release hands the transaction array back for reuse. Only once nothing
// reads the preparation any more: its run returned, and its base prefill
// (which reads the senders and recipients) finished.
func (ps *preparedSet) release() {
	if ps == nil || ps.txs == nil {
		return
	}
	txs := ps.txs
	ps.txs, ps.msgs = nil, nil
	go preparedFree.give(txs)
}

// len is the number of transactions prepared for.
func (ps *preparedSet) len() int {
	if ps == nil {
		return 0
	}
	return len(ps.txs)
}
