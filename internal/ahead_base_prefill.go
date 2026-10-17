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
	"strconv"
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
// tree to hold a block h of the child's own chain with N-K <= h <= N (a
// labelled root, commitment.QMDBRootComputer.AppliedState; K is
// N42_AHEAD_BASE_DEPTH, default 2), reads the accounts every transaction
// sends from and to, the fee recipients and the keys the block-start system
// calls read, and checks afterwards that only the roots of the child's
// ancestors above h changed the tree meanwhile (AppliedSince); otherwise it
// is dropped. A value read at h is the child's base value once every block
// in (h, N] has its post-state layered on the child's reader (P15b, as
// n42-rs stacks its unlanded ancestors): the ahead run (runAhead) layers
// the written ancestors' post-states the pipeline keeps (lagPipeline.older)
// under N's, newest outermost, and drops the prefill when one is missing --
// never a prefill over a partial stack. The ahead run's tree reads -- its
// state, its workers, the block-start calls -- answer the prefilled keys
// without the tree's lock; the rest read through it as before. The claim
// does not wait for a fill still reading: it stops it at the next key and
// takes the keys read so far (N42_AHEAD_BASE_CLAIM_WAIT=1 waits for the
// whole fill). N42_AHEAD_BASE_PREFETCH=0 disables it.

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

// aheadBaseDepth is N42_AHEAD_BASE_DEPTH, K: a prefill reads the tree at a
// block h of the child's chain with N-K <= h <= N (N the child's parent).
// Default 2, clamped to [1, aheadBaseMaxDepth]; each step keeps one more
// written block's post-state alive (lagPipeline.older).
var aheadBaseDepth = parseAheadBaseDepth(os.Getenv("N42_AHEAD_BASE_DEPTH"))

const aheadBaseMaxDepth = 4

func parseAheadBaseDepth(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 2
	}
	if n > aheadBaseMaxDepth {
		n = aheadBaseMaxDepth
	}
	return n
}

// SetAheadBaseDepthForTest sets K and returns the restore.
func SetAheadBaseDepthForTest(k int) func() {
	prev := aheadBaseDepth
	aheadBaseDepth = k
	return func() { aheadBaseDepth = prev }
}

// aheadBaseClaimWaitsFill is N42_AHEAD_BASE_CLAIM_WAIT=1: the claim waits
// for a fill still reading instead of stopping it (the P15 behaviour).
var aheadBaseClaimWaitsFill = os.Getenv("N42_AHEAD_BASE_CLAIM_WAIT") == "1"

// SetAheadBaseClaimWaitForTest sets whether the claim waits for a fill
// still reading, and returns the restore.
func SetAheadBaseClaimWaitForTest(wait bool) func() {
	prev := aheadBaseClaimWaitsFill
	aheadBaseClaimWaitsFill = wait
	return func() { aheadBaseClaimWaitsFill = prev }
}

// aheadBaseMaxWait bounds a prefill's wait for the tree to reach the
// child's grandparent (the claim of the preparation ends it sooner).
var aheadBaseMaxWait = 10 * time.Second

// aheadBase is one child's base prefill.
type aheadBase struct {
	stop chan struct{} // closed by the claim: stop waiting for the tree
	once sync.Once
	done chan struct{}

	// Set before done closes. pf is nil when the prefill was skipped
	// (skip says why).
	pf     *commitment.BasePrefill
	at     uint64        // the labelled height of the tree the fill started on
	atHash types.Hash    // the block the tree held there
	depth  uint64        // N - at, N the child's parent
	waited time.Duration // for the tree to reach a usable state
	took   time.Duration // the fill itself
	skip   string
	// cut: the claim stopped the fill; pf holds the keys read before.
	cut atomic.Bool

	// claimWait: the claim's wait for the prefill to finish (set by the
	// claimer).
	claimWait time.Duration
}

// halt ends a prefill's wait for the tree, stops a fill still reading at
// its next key (unless aheadBaseClaimWaitsFill), and waits until it
// finished.
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
// prefill was read on the grandparent's tree, those read deeper (depth 2 or
// more: older post-states layered), and the lookups the prefills answered
// (process-wide, since start).
var aheadBaseUsed, aheadBaseOnGrandparent, aheadBaseDeep, aheadBaseHits atomic.Uint64

func noteAheadBaseUsed(ab *aheadBase, number uint64, pf *commitment.BasePrefill) {
	aheadBaseUsed.Add(1)
	if ab.at+2 == number {
		aheadBaseOnGrandparent.Add(1)
	}
	if ab.depth >= 2 {
		aheadBaseDeep.Add(1)
	}
	hits, _ := pf.Stats()
	aheadBaseHits.Add(hits)
}

// AheadBaseDeepStats returns the ahead runs whose prefill was read two or
// more blocks below their parent (since start).
func AheadBaseDeepStats() uint64 { return aheadBaseDeep.Load() }

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

// aheadBaseChain is a child's ancestry for its prefill: hash[d] is the
// block at height parent-d (hash[0] the parent), as far as it is known and
// at most aheadBaseDepth levels down.
type aheadBaseChain struct {
	top  uint64 // the parent's height
	hash []types.Hash
}

func (bc *BlockChain) aheadBaseChainOf(hdr *block.Header, depth int) aheadBaseChain {
	n := hdr.Number.Uint64()
	c := aheadBaseChain{top: n - 1, hash: []types.Hash{hdr.ParentHash}}
	for d := 1; d <= depth && uint64(d) <= c.top; d++ {
		ph, ok := bc.lagParentHashOf(c.hash[d-1], c.top-uint64(d-1))
		if !ok {
			break
		}
		c.hash = append(c.hash, ph)
	}
	return c
}

// holds reports whether st is the tree in the state of one of the chain's
// blocks; d is its depth below the parent.
func (c aheadBaseChain) holds(st commitment.QMDBAppliedState) (d uint64, ok bool) {
	if !st.Labelled || st.Number > c.top {
		return 0, false
	}
	d = c.top - st.Number
	return d, d < uint64(len(c.hash)) && c.hash[d] == st.Hash
}

// aheadBaseMovedOK reports whether the mutations since gen (states, oldest
// first, from AppliedSince) are all roots of the chain's blocks above at,
// in height order: values read before, between and after them are each the
// state of a block in [at, parent] of the child's chain.
func aheadBaseMovedOK(c aheadBaseChain, at uint64, states []commitment.QMDBAppliedState) bool {
	prev := at
	for _, st := range states {
		if _, ok := c.holds(st); !ok || st.Number != prev+1 {
			return false
		}
		prev = st.Number
	}
	return true
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
		log.Debug("ahead base prefill", "number", n, "keys", ab.pf.Len(), "treeAt", ab.at, "depth", ab.depth,
			"waitMs", ab.waited.Milliseconds(), "fillMs", ab.took.Milliseconds(), "cut", ab.cut.Load(), "skip", ab.skip)
	}()
	if n < 2 {
		ab.skip = "genesis child"
		return
	}
	chain := bc.aheadBaseChainOf(hdr, aheadBaseDepth)

	// Wait for the tree to hold a block of the chain within the depth.
	t0 := time.Now()
	tick := time.NewTicker(250 * time.Microsecond)
	defer tick.Stop()
	var st0 commitment.QMDBAppliedState
	for {
		st0 = rc.AppliedState()
		if d, ok := chain.holds(st0); ok {
			ab.depth = d
			break
		}
		if st0.Labelled && st0.Number+uint64(len(chain.hash)) > chain.top {
			// The tree holds another block at a height the child's layers
			// could cover, or it passed the parent: nothing this child reads
			// comes from there.
			ab.skip = "tree off the parent"
			return
		}
		if time.Since(t0) > aheadBaseMaxWait {
			ab.skip = "tree below the chain"
			return
		}
		select {
		case <-ab.stop:
			ab.skip = "claimed before the tree reached the chain"
			return
		case <-tick.C:
		}
	}
	ab.waited = time.Since(t0)
	ab.at, ab.atHash = st0.Number, st0.Hash

	stop := func() bool {
		if abort() {
			return true
		}
		if aheadBaseClaimWaitsFill {
			return false
		}
		select {
		case <-ab.stop:
			ab.cut.Store(true)
			return true
		default:
			return false
		}
	}
	tFill := time.Now()
	keys := bc.aheadBaseKeys(hdr, txs, prepared, sp)
	pf, err := rc.FillBasePrefill(bc.ChainDB, keys, aheadPrepWorkers(), stop)
	ab.took = time.Since(tFill)
	if err != nil {
		ab.skip = "fill: " + err.Error()
		return
	}
	// Only the roots of the chain's blocks above the start may have run
	// while the fill read: each value is then the state of a block in
	// [at, parent], which the child's layers account for.
	states, ok := rc.AppliedSince(st0.Gen)
	if !ok || !aheadBaseMovedOK(chain, st0.Number, states) {
		if st1 := rc.AppliedState(); st1.Labelled && st1.Number > chain.top {
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
