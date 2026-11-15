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
// senders are settled, its preparation (ahead_prep.go) starts the base
// prefill with its computation (N42_AHEAD_BASE_EARLY, default on) or after
// it; the prefill waits for the
// tree to hold a block h of the child's own chain with N-K <= h <= N (a
// labelled root, commitment.QMDBRootComputer.AppliedState; K is
// N42_AHEAD_BASE_DEPTH, default 1), reads the accounts every transaction
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
// waits for a fill still reading; N42_AHEAD_BASE_CLAIM_WAIT=0 stops it at
// the next key and takes the keys read so far. N42_AHEAD_BASE_PREFETCH=0
// disables it. S113 (35zzzcm): depth 2 with the cut claim bought no cycle
// time (cut fills answered no lookups) and its leg stalled for over a
// minute, so both are opt-in.

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
// Default 1, clamped to [1, aheadBaseMaxDepth]; each step keeps one more
// written block's post-state alive (lagPipeline.older).
var aheadBaseDepth = parseAheadBaseDepth(os.Getenv("N42_AHEAD_BASE_DEPTH"))

const aheadBaseMaxDepth = 4

func parseAheadBaseDepth(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 1
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

// aheadBaseClaimWaitsFill is N42_AHEAD_BASE_CLAIM_WAIT (default on, the P15
// behaviour): the claim waits for a fill still reading; "0" stops it.
var aheadBaseClaimWaitsFill = os.Getenv("N42_AHEAD_BASE_CLAIM_WAIT") != "0"

// SetAheadBaseClaimWaitForTest sets whether the claim waits for a fill
// still reading, and returns the restore.
func SetAheadBaseClaimWaitForTest(wait bool) func() {
	prev := aheadBaseClaimWaitsFill
	aheadBaseClaimWaitsFill = wait
	return func() { aheadBaseClaimWaitsFill = prev }
}

// aheadBaseBeforeFillForTest runs before a prefill reads its keys (tests:
// hold the fill until the claim).
var aheadBaseBeforeFillForTest func(ab *aheadBase)

// N42_AHEAD_KEYS_CUT=1 (default off): a claim that cuts its base prefill
// (N42_AHEAD_PRE_EARLY, or N42_AHEAD_BASE_CLAIM_WAIT=0) also stops one still
// collecting its keys, instead of waiting for the collection and stopping
// the fill at its first key. Run 67 (B2, full blocks): the key collection
// took 21-24 ms mean (two key hashes per transaction, plus the block-start
// calls' tree reads), the fill 31-35 ms in all, and an early-snapshot
// launch's claim waited 5.2 ms mean (15 ms p90) for it -- aheadPreFill,
// nearly all of aheadPre. A prefill stopped while collecting reads nothing
// (skip "cut while collecting the keys"): the run reads every key through
// the tree, as without a prefill.
var aheadKeysCutOn = parseOptIn(os.Getenv("N42_AHEAD_KEYS_CUT"))

// aheadKeysCuts counts prefills a claim stopped while they collected their
// keys (process-wide).
var aheadKeysCuts atomic.Uint64

// SetAheadKeysCutForTest switches N42_AHEAD_KEYS_CUT and returns the restore.
func SetAheadKeysCutForTest(on bool) func() {
	prev := aheadKeysCutOn
	aheadKeysCutOn = on
	return func() { aheadKeysCutOn = prev }
}

// N42_AHEAD_PRE_DETACH=1 (default off): a cut claim (N42_AHEAD_PRE_EARLY)
// that finds its base prefill still collecting its keys does not wait for
// the collection to end: it detaches the prefill and runs without one,
// which is what such a claim got anyway (the prefill read nothing, skip "cut
// while collecting the keys"). Run 72 (B1, node0): on 48% of early launches
// the claim waited 11 / 27 (13) ms p50 / p90 (mean) for that collection,
// because its block-start reads (blockStartKeys, on the collecting
// goroutine, checked for the stop only before they start) queue on the
// tree's reader lock behind the root that labelled the tree for the prefill
// (ComputeRoot labels the tree as it takes the lock, then applies for
// 15-25 ms): the collection took 29 ms p50 against 3.5 ms when the prefill
// was used.
//
// What the claim still waits for: the key goroutines, the only part of the
// prefill that reads the block's transactions (and, for a prefill started
// after the computation, the preparation's prepared set) -- the run writes
// senders and prepared entries once it starts, so nothing may read them
// then. They check the stop every aheadKeysHaltEvery transactions. The rest
// -- the block-start reads on the prefill's own read transaction -- finishes
// in the background: the detached prefill releases its key lists itself,
// exactly once, builds no prefill and closes done; nothing else references
// it (the claim removed the preparation from the map, and prefill() reports
// none). A prefill that already started reading (its keys complete) is not
// detached: the claim cuts it as before and keeps what it read.
var aheadPreDetachOn = parseOptIn(os.Getenv("N42_AHEAD_PRE_DETACH"))

// SetAheadPreDetachForTest switches N42_AHEAD_PRE_DETACH and returns the
// restore.
func SetAheadPreDetachForTest(on bool) func() {
	prev := aheadPreDetachOn
	aheadPreDetachOn = on
	return func() { aheadPreDetachOn = prev }
}

// aheadPreDetaches counts claims that detached a prefill collecting its keys
// (process-wide, since start).
var aheadPreDetaches atomic.Uint64

// Phases of an aheadBase (phase).
const (
	abPhaseTree     int32 = iota // waiting for the tree
	abPhaseKeys                  // collecting the keys
	abPhaseFill                  // reading the tree
	abPhaseDetached              // a claim detached it while it collected the keys
)

// aheadBaseSysKeysForTest runs on a prefill's collecting goroutine right
// before its block-start reads (tests: hold them as the tree's lock does).
var aheadBaseSysKeysForTest func(ab *aheadBase)

// aheadBaseEndForTest sees every prefill as its goroutine ends, before done
// closes (tests: key lists released exactly once).
var aheadBaseEndForTest func(ab *aheadBase)

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
	keys   time.Duration // of took, collecting the keys
	skip   string
	// cut: the claim stopped the fill; pf holds the keys read before.
	cut atomic.Bool
	// cutAtClaim: the claim stops a fill still reading even when
	// aheadBaseClaimWaitsFill (N42_AHEAD_PRE_EARLY, a late child's claim);
	// set before the claim closes stop.
	cutAtClaim atomic.Bool

	// claimWait: the claim's wait for the prefill to finish (set by the
	// claimer).
	claimWait time.Duration

	// phase (abPhase*): a claim may detach the prefill only while it
	// collects its keys (N42_AHEAD_PRE_DETACH); the fill moves on to reading
	// only if no claim did.
	phase atomic.Int32
	// readersDone closes once the key goroutines (the only readers of the
	// block's transactions and prepared set) are done.
	readersDone chan struct{}
	// keysReleased counts the releases of the key lists (at most one).
	keysReleased atomic.Int32
	// detachWait: a detaching claim's wait in nanoseconds (diagnostics).
	detachWait atomic.Int64
}

// detach detaches a prefill still collecting its keys from its claim
// (N42_AHEAD_PRE_DETACH): it stops the collection and waits only until
// nothing reads the block's transactions any more. False when the prefill
// is past that phase (or before it): the caller halts it as before.
func (ab *aheadBase) detach() (time.Duration, bool) {
	if ab == nil || !ab.phase.CompareAndSwap(abPhaseKeys, abPhaseDetached) {
		return 0, false
	}
	t0 := time.Now()
	ab.cutAtClaim.Store(true)
	ab.once.Do(func() { close(ab.stop) })
	select {
	case <-ab.readersDone:
	case <-ab.done:
	}
	aheadPreDetaches.Add(1)
	d := time.Since(t0)
	ab.detachWait.Store(int64(d))
	return d, true
}

// releaseKeys hands the prefill's key lists back (once).
func (ab *aheadBase) releaseKeys(keys [][]qmdb.Hash) {
	ab.keysReleased.Add(1)
	releaseAheadBaseKeys(keys)
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
	if pr == nil || pr.baseDetached {
		// A detached prefill still runs: nothing of it may be read.
		return nil
	}
	return pr.base
}

// detachedPrefill reports whether the claim detached pr's prefill, and its
// wait for the prefill's transaction readers.
func (pr *aheadPrep) detachedPrefill() (time.Duration, bool) {
	if pr == nil || !pr.baseDetached {
		return 0, false
	}
	return pr.baseDetachWait, true
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
func (bc *BlockChain) startAheadBase(hdr *block.Header, txs []*transaction.Transaction, prepared *preparedSet, sp *StateProcessor, abort func() bool) *aheadBase {
	ab := &aheadBase{stop: make(chan struct{}), done: make(chan struct{}), readersDone: make(chan struct{})}
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

func (bc *BlockChain) runAheadBase(ab *aheadBase, hdr *block.Header, txs []*transaction.Transaction, prepared *preparedSet, sp *StateProcessor, abort func() bool) {
	defer close(ab.done)
	if fn := aheadBaseEndForTest; fn != nil {
		defer fn(ab)
	}
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
		if aheadBaseClaimWaitsFill && !ab.cutAtClaim.Load() {
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
	// From here a claim may detach the prefill (N42_AHEAD_PRE_DETACH) until
	// its keys are complete.
	ab.phase.Store(abPhaseKeys)
	if fn := aheadBaseBeforeFillForTest; fn != nil {
		fn(ab)
	}
	tFill := time.Now()
	var keysHalt func() bool
	if aheadKeysCutOn || aheadPreDetachOn {
		keysHalt = stop
	}
	var beforeSys func()
	if fn := aheadBaseSysKeysForTest; fn != nil {
		beforeSys = func() { fn(ab) }
	}
	keys, whole := bc.aheadBaseKeysHaltDone(hdr, txs, prepared, sp, keysHalt, ab.readersDone, beforeSys)
	ab.keys = time.Since(tFill)
	if whole && !ab.phase.CompareAndSwap(abPhaseKeys, abPhaseFill) {
		// Detached after the keys were complete: its claim runs without it.
		whole = false
	}
	if !whole {
		// N42_AHEAD_KEYS_CUT / N42_AHEAD_PRE_DETACH: the claim (or the
		// block's drop) came while the keys were collected; nothing was read.
		ab.releaseKeys(keys)
		ab.took = time.Since(tFill)
		ab.skip = "cut while collecting the keys"
		if ab.phase.Load() == abPhaseDetached {
			ab.skip = "detached while collecting the keys"
		}
		if ab.cut.Load() {
			aheadKeysCuts.Add(1)
		}
		return
	}
	pf, err := rc.FillBasePrefill(bc.ChainDB, keys, aheadPrepWorkers(), stop)
	// The fill copied what it read and its workers are done: nothing reads
	// the key lists any more.
	ab.releaseKeys(keys)
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

// aheadKeysFree keeps the prefills' per-goroutine key lists (pointer-free,
// written before they are read): two blocks' worth.
var aheadKeysFree = scratchFree[qmdb.Hash]{max: 64, noZero: true}

// releaseAheadBaseKeys hands aheadBaseKeys' lists back once nothing reads
// them.
func releaseAheadBaseKeys(keys [][]qmdb.Hash) {
	for i, ks := range keys {
		aheadKeysFree.give(ks)
		keys[i] = nil
	}
}

// aheadBaseKeys returns the tree keys a child's ahead run reads for certain:
// every transaction's sender and recipient account, the fee recipients and
// what the block-start system calls read. One slice per goroutine. A
// transaction's sender and recipient come from its prepared message (the
// preparation's, read-only here), else from the transaction.
func (bc *BlockChain) aheadBaseKeys(hdr *block.Header, txs []*transaction.Transaction, prepared *preparedSet, sp *StateProcessor) [][]qmdb.Hash {
	keys, _ := bc.aheadBaseKeysHalt(hdr, txs, prepared, sp, nil)
	return keys
}

// aheadKeysHaltEvery is how many transactions a key goroutine collects
// between two checks of halt.
const aheadKeysHaltEvery = 1024

// aheadBaseKeysHalt is aheadBaseKeys stopping early once halt (nil: never)
// reports true, checked every aheadKeysHaltEvery transactions per goroutine
// and before the block-start calls' tree reads; whole is false when it
// stopped (keys is then partial, for release only).
func (bc *BlockChain) aheadBaseKeysHalt(hdr *block.Header, txs []*transaction.Transaction, prepared *preparedSet, sp *StateProcessor, halt func() bool) (_ [][]qmdb.Hash, whole bool) {
	return bc.aheadBaseKeysHaltDone(hdr, txs, prepared, sp, halt, nil, nil)
}

// aheadBaseKeysHaltDone is aheadBaseKeysHalt closing readersDone (when not
// nil) as soon as no goroutine of it reads txs or prepared any more -- before
// the block-start calls' tree reads end. beforeSys (tests) runs right before
// those reads.
func (bc *BlockChain) aheadBaseKeysHaltDone(hdr *block.Header, txs []*transaction.Transaction, prepared *preparedSet, sp *StateProcessor, halt func() bool, readersDone chan struct{}, beforeSys func()) (_ [][]qmdb.Hash, whole bool) {
	workers := aheadPrepWorkers()
	keys := make([][]qmdb.Hash, workers+1)
	per := (len(txs) + workers - 1) / workers
	var halted atomic.Bool
	check := func() bool {
		if halt == nil {
			return false
		}
		if halted.Load() {
			return true
		}
		if halt() {
			halted.Store(true)
			return true
		}
		return false
	}
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
			ks := aheadKeysFree.take(2 * (hi - lo))[:0]
			for i := lo; i < hi; i++ {
				if (i-lo)%aheadKeysHaltEvery == 0 && check() {
					break
				}
				if i < prepared.len() && prepared.txs[i].ok {
					// A planned transfer's sender and recipient are its
					// message's; any other prepared transaction kept its
					// message.
					if pm := &prepared.txs[i]; pm.plan.ok {
						ks = append(ks, qmdb.Hash(commitment.AccountKeyHash(pm.plan.from)), qmdb.Hash(commitment.AccountKeyHash(pm.plan.to)))
						continue
					}
					if m := prepared.keptMsg(i); m != nil {
						ks = append(ks, qmdb.Hash(commitment.AccountKeyHash(m.From())))
						if to := m.To(); to != nil {
							ks = append(ks, qmdb.Hash(commitment.AccountKeyHash(*to)))
						}
						continue
					}
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
	if readersDone != nil {
		go func() {
			wg.Wait()
			close(readersDone)
		}()
	}
	extra := []qmdb.Hash{qmdb.Hash(commitment.AccountKeyHash(hdr.Coinbase))}
	for _, a := range deferredFeeRecipients(sp.config, hdr) {
		extra = append(extra, qmdb.Hash(commitment.AccountKeyHash(a)))
	}
	if !check() {
		if beforeSys != nil {
			beforeSys()
		}
		extra = append(extra, bc.blockStartKeys(hdr, sp)...)
	}
	wg.Wait()
	keys[workers] = extra
	return keys, !check()
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
