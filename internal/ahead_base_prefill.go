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
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// The base prefill of a queued child's ahead run (P15).
//
// Child N+1 runs ahead on parent N's post-state layered over the live QMDB
// tree, and every base read it makes while N's ComputeRoot holds the tree
// waits for that root (S110: the block-start system call, sysStartMs median
// 22 ms on blocks whose stateless preparation hit). The tree in state N-1
// and in state N agree on every key N did not change, and N's post-state
// layer answers every key N did change (commitment/qmdb_base_prefill.go),
// so the child's base values may come from either state. Once the child's
// preparation (ahead_prep.go) is computed, its base prefill waits for the
// tree to hold N-1 or N of the child's own chain (a labelled root,
// commitment.QMDBRootComputer.AppliedState), reads the accounts every
// transaction sends from and to, the fee recipients and the keys the
// block-start system calls read, and checks afterwards that nothing but N's
// own root changed the tree meanwhile; otherwise it is dropped. The ahead
// run's tree reads -- its state, its workers, the block-start calls --
// answer the prefilled keys without the tree's lock; the rest read through
// it as before. N42_AHEAD_BASE_PREFETCH=0 disables it.

// aheadBasePrefetchOn is N42_AHEAD_BASE_PREFETCH (default on; "0", "false"
// or "off" disables).
var aheadBasePrefetchOn = parseLagPipeline(os.Getenv("N42_AHEAD_BASE_PREFETCH"))

// SetAheadBasePrefetchForTest switches the base prefill and returns the
// restore.
func SetAheadBasePrefetchForTest(on bool) func() {
	prev := aheadBasePrefetchOn
	aheadBasePrefetchOn = on
	return func() { aheadBasePrefetchOn = prev }
}

// aheadBaseMaxWait bounds a prefill's wait for the tree to reach the
// child's grandparent (the claim of the preparation ends it sooner).
const aheadBaseMaxWait = 10 * time.Second

// aheadBase is one child's base prefill.
type aheadBase struct {
	stop chan struct{} // closed by the claim: stop waiting for the tree
	once sync.Once
	done chan struct{}

	// Set before done closes. pf is nil when the prefill was skipped
	// (skip says why).
	pf     *commitment.BasePrefill
	at     uint64        // the labelled height of the tree the fill started on
	waited time.Duration // for the tree to reach the grandparent
	took   time.Duration // the fill itself
	skip   string

	// claimWait: the claim's wait for the prefill to finish (set by the
	// claimer).
	claimWait time.Duration
}

// halt ends a prefill's wait for the tree and waits until it finished.
func (ab *aheadBase) halt() {
	if ab == nil {
		return
	}
	t0 := time.Now()
	ab.once.Do(func() { close(ab.stop) })
	<-ab.done
	ab.claimWait = time.Since(t0)
}

// prefill returns the usable prefill of a claimed preparation, or nil.
func (pr *aheadPrep) prefill() *aheadBase {
	if pr == nil {
		return nil
	}
	return pr.base
}

// Ahead runs whose tree reads had a base prefill, those of them whose
// prefill was read on the grandparent's tree, and the lookups the prefills
// answered (process-wide, since start).
var aheadBaseUsed, aheadBaseOnGrandparent, aheadBaseHits atomic.Uint64

func noteAheadBaseUsed(ab *aheadBase, number uint64, pf *commitment.BasePrefill) {
	aheadBaseUsed.Add(1)
	if ab.at+2 == number {
		aheadBaseOnGrandparent.Add(1)
	}
	hits, _ := pf.Stats()
	aheadBaseHits.Add(hits)
}

// AheadBasePrefillStats returns the ahead runs that read through a base
// prefill, those whose prefill came from the grandparent's tree, and the
// lookups the prefills answered (since start).
func AheadBasePrefillStats() (used, onGrandparent, hits uint64) {
	return aheadBaseUsed.Load(), aheadBaseOnGrandparent.Load(), aheadBaseHits.Load()
}

// aheadBaseUsable: the chain's ahead runs read base values from the QMDB
// tree, so a prefill can answer them.
func (bc *BlockChain) aheadBaseUsable() bool {
	return aheadBasePrefetchOn && bc.qmdbEnabled && bc.qmdbRootComputer != nil && bc.ChainDB != nil &&
		commitment.QMDBStateReadMode() != commitment.QMDBReadOff
}

// startAheadBase starts b's base prefill (its preparation computed, its
// senders settled).
// abort reports that the block will not use it: the fill stops at the next
// key.
func (bc *BlockChain) startAheadBase(hdr *block.Header, txs []*transaction.Transaction, prepared []preparedTx, sp *StateProcessor, abort func() bool) *aheadBase {
	ab := &aheadBase{stop: make(chan struct{}), done: make(chan struct{})}
	go bc.runAheadBase(ab, hdr, txs, prepared, sp, abort)
	return ab
}

// lagParentHashOf returns the parent hash of block (hash, number): a queued
// block's, a pending or stored header's.
func (bc *BlockChain) lagParentHashOf(hash types.Hash, number uint64) (types.Hash, bool) {
	le := &bc.lagExec
	le.mu.Lock()
	q := le.queued[hash]
	le.mu.Unlock()
	if q != nil {
		return q.blk.ParentHash(), true
	}
	if h := bc.pendingHeader(hash, number); h != nil {
		return h.ParentHash, true
	}
	if bc.ChainDB == nil {
		return types.Hash{}, false
	}
	if ih := bc.GetHeader(hash, uint256.NewInt(number)); ih != nil {
		if h, ok := ih.(*block.Header); ok && h.Number != nil && h.Number.Uint64() == number {
			return h.ParentHash, true
		}
	}
	return types.Hash{}, false
}

// aheadBaseTreeOK reports whether st is a tree state the child (number n,
// on parent) may take base values from: its parent's post-state or its
// grandparent's (grand, when known).
func aheadBaseTreeOK(st commitment.QMDBAppliedState, n uint64, parent, grand types.Hash, haveGrand bool) bool {
	if !st.Labelled {
		return false
	}
	return (st.Number == n-1 && st.Hash == parent) || (haveGrand && st.Number == n-2 && st.Hash == grand)
}

func (bc *BlockChain) runAheadBase(ab *aheadBase, hdr *block.Header, txs []*transaction.Transaction, prepared []preparedTx, sp *StateProcessor, abort func() bool) {
	defer close(ab.done)
	defer func() {
		if r := recover(); r != nil {
			ab.pf, ab.skip = nil, "panic"
			log.Warn("ahead base prefill panicked", "number", hdr.Number.Uint64(), "err", r)
		}
	}()
	rc := bc.qmdbRootComputer
	n := hdr.Number.Uint64()
	defer func() {
		log.Debug("ahead base prefill", "number", n, "keys", ab.pf.Len(), "treeAt", ab.at,
			"waitMs", ab.waited.Milliseconds(), "fillMs", ab.took.Milliseconds(), "skip", ab.skip)
	}()
	if n < 2 {
		ab.skip = "genesis child"
		return
	}
	parent := hdr.ParentHash
	grand, haveGrand := bc.lagParentHashOf(parent, n-1)

	// Wait for the tree to hold the grandparent (or the parent).
	t0 := time.Now()
	tick := time.NewTicker(250 * time.Microsecond)
	defer tick.Stop()
	var st0 commitment.QMDBAppliedState
	for {
		st0 = rc.AppliedState()
		if aheadBaseTreeOK(st0, n, parent, grand, haveGrand) {
			break
		}
		if st0.Labelled && st0.Number >= n-1 {
			// The parent's height holds another block, or the tree passed
			// it: nothing this child reads comes from there.
			ab.skip = "tree off the parent"
			return
		}
		if time.Since(t0) > aheadBaseMaxWait {
			ab.skip = "tree below the grandparent"
			return
		}
		select {
		case <-ab.stop:
			ab.skip = "claimed before the tree reached the grandparent"
			return
		case <-tick.C:
		}
	}
	ab.waited = time.Since(t0)
	ab.at = st0.Number

	tFill := time.Now()
	keys := bc.aheadBaseKeys(hdr, txs, prepared, sp)
	pf, err := rc.FillBasePrefill(bc.ChainDB, keys, aheadPrepWorkers(), abort)
	ab.took = time.Since(tFill)
	if err != nil {
		ab.skip = "fill: " + err.Error()
		return
	}
	// Only the parent's own root may have run while the fill read: the
	// values then come from the grandparent's state or the parent's, never
	// from a state the child's layers do not account for.
	st1 := rc.AppliedState()
	switch {
	case st1.Gen == st0.Gen:
	case st1.Gen == st0.Gen+1 && st1.Labelled && st1.Number == n-1 && st1.Hash == parent:
	default:
		if st1.Labelled && st1.Number > n-1 {
			log.Warn("ahead base prefill: the tree passed the child's parent while it read",
				"number", n, "treeAt", st1.Number)
		}
		ab.skip = "tree changed under the fill"
		return
	}
	ab.pf = pf
}

// aheadBaseKeys returns the tree keys a child's ahead run reads for certain:
// every transaction's sender and recipient account, the fee recipients and
// what the block-start system calls read. One slice per goroutine. A
// transaction's sender and recipient come from its prepared message (the
// preparation's, read-only here), else from the transaction.
func (bc *BlockChain) aheadBaseKeys(hdr *block.Header, txs []*transaction.Transaction, prepared []preparedTx, sp *StateProcessor) [][]qmdb.Hash {
	workers := aheadPrepWorkers()
	keys := make([][]qmdb.Hash, workers+1)
	per := (len(txs) + workers - 1) / workers
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		lo, hi := w*per, (w+1)*per
		if hi > len(txs) {
			hi = len(txs)
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(w, lo, hi int) {
			defer wg.Done()
			ks := make([]qmdb.Hash, 0, 2*(hi-lo))
			for i := lo; i < hi; i++ {
				if i < len(prepared) && prepared[i].ok {
					m := &prepared[i].msg
					ks = append(ks, qmdb.Hash(commitment.AccountKeyHash(m.From())))
					if to := m.To(); to != nil {
						ks = append(ks, qmdb.Hash(commitment.AccountKeyHash(*to)))
					}
					continue
				}
				tx := txs[i]
				if from := tx.From(); from != nil {
					ks = append(ks, qmdb.Hash(commitment.AccountKeyHash(*from)))
				}
				if to := tx.To(); to != nil {
					ks = append(ks, qmdb.Hash(commitment.AccountKeyHash(*to)))
				}
			}
			keys[w] = ks
		}(w, lo, hi)
	}
	extra := []qmdb.Hash{qmdb.Hash(commitment.AccountKeyHash(hdr.Coinbase))}
	for _, a := range deferredFeeRecipients(sp.config, hdr) {
		extra = append(extra, qmdb.Hash(commitment.AccountKeyHash(a)))
	}
	extra = append(extra, bc.blockStartKeys(hdr, sp)...)
	wg.Wait()
	keys[workers] = extra
	return keys
}

// blockStartKeys runs the block-start system calls on a throwaway state over
// the tree and returns the keys they read (the fork rules decide which).
func (bc *BlockChain) blockStartKeys(hdr *block.Header, sp *StateProcessor) (keys []qmdb.Hash) {
	defer func() {
		if recover() != nil {
			keys = nil
		}
	}()
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	src := commitment.NewLookupSourceRefreshing(bc.qmdbRootComputer, tx, bc.ChainDB)
	rec := &keyRecordingReader{StateReader: commitment.NewQMDBStateReader(src, state.NewPlainStateReader(tx), commitment.QMDBStateReadMode())}
	ibs := state.New(rec)
	_ = ProcessExecutionBlockStart(hdr.ParentBeaconRoot, sp.config, ibs, hdr, sp.engine)
	return rec.keys
}

// keyRecordingReader records the tree keys of the account and storage reads
// it passes through.
type keyRecordingReader struct {
	state.StateReader
	keys []qmdb.Hash
}

func (r *keyRecordingReader) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	r.keys = append(r.keys, qmdb.Hash(commitment.AccountKeyHash(a)))
	return r.StateReader.ReadAccountData(a)
}

func (r *keyRecordingReader) ReadAccountStorage(a types.Address, key *types.Hash) ([]byte, error) {
	r.keys = append(r.keys, qmdb.Hash(commitment.StorageKeyHash(a, *key)))
	return r.StateReader.ReadAccountStorage(a, key)
}
