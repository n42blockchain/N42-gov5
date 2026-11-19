// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"os"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// N42_AHEAD_START_KEYS_PREFETCH=1 (default off, r94a): a block's preparation
// also starts a second, small base prefill of only the keys its block-start
// system calls read (and the fee recipients, as the base prefill does),
// beside the block's base prefill (ahead_base_prefill.go).
// Run 80 (node0, full blocks): on the 55-63% of blocks whose base prefill a
// cut claim detached while it collected its keys (N42_AHEAD_PRE_DETACH), the
// ahead run read those three keys through the tree itself, 6.3-7.5 ms p50 /
// 17.8 p90, waiting for the tree's reader lock behind its parent's root;
// about 1 ms otherwise. The small prefill reads no transaction, so it is
// done long before the block's tx keys are collected, usually before the
// parent's root takes the tree.
//
// It follows the base prefill's rules exactly -- it is an aheadBase: it waits
// for the tree to hold a block h of the child's own chain within the depth
// (aheadBaseWaitTree), reads the keys there and is dropped when anything but
// the roots of the child's ancestors above h changed the tree meanwhile
// (AppliedSince); the claim then layers the post-states of every block in
// (h, parent] over it (aheadBaseLayers) or drops it. So every value the run
// takes from it is the value its tree read returns today.
//
// The claim takes it only when the block's base prefill is detached (or was
// never started), and only when it is already done: it never waits for it.
// One not taken is told to stop and ends on its own goroutine; it holds no
// reference to the block's transactions or preparation.
var aheadStartKeysOn = parseOptIn(os.Getenv("N42_AHEAD_START_KEYS_PREFETCH"))

// SetAheadStartKeysForTest switches N42_AHEAD_START_KEYS_PREFETCH and returns
// the restore.
func SetAheadStartKeysForTest(on bool) func() {
	prev := aheadStartKeysOn
	aheadStartKeysOn = on
	return func() { aheadStartKeysOn = prev }
}

// N42_AHEAD_START_KEYS_DEPTH2=1 (default off, r95a; acts with
// N42_AHEAD_START_KEYS_PREFETCH): the start-keys prefill may read the tree
// two blocks below the child's parent (h = N-3 for child N), not only one.
// Run 81 (B3): the prefill was taken on 7 of 1,894 full blocks. It starts
// at the block's preparation, 14 ms p50 before root(N-2) starts, while the
// tree is at N-3 and free; at depth 1 it waits for root(N-2) and then
// reads behind the N-2 capture, so it is not done at the claim (57-62% of
// blocks claim before rootDone(N-2)). At depth 2 it reads at once.
//
// The value rules are the base prefill's at K = 2, unchanged: the fill
// reads at a labelled block of the child's chain, is dropped unless every
// mutation during it is a root of the chain above that block
// (aheadBaseMovedOK), and the claim layers the post-states of every block
// in (h, parent] over it -- the pipeline keeps one more written parent's
// post-state for that (keepOlderLocked) -- or drops it when one is missing
// (aheadBaseLayers, earlyPrefillUsable on an early snapshot). So every
// value the run takes from it is still the value its tree read returns.
var aheadStartKeysDepth2On = parseOptIn(os.Getenv("N42_AHEAD_START_KEYS_DEPTH2"))

// SetAheadStartKeysDepth2ForTest switches N42_AHEAD_START_KEYS_DEPTH2 and
// returns the restore.
func SetAheadStartKeysDepth2ForTest(on bool) func() {
	prev := aheadStartKeysDepth2On
	aheadStartKeysDepth2On = on
	return func() { aheadStartKeysDepth2On = prev }
}

// aheadStartKeysDepth is the depth K the start-keys prefill reads at.
func aheadStartKeysDepth() int {
	if aheadStartKeysOn && aheadStartKeysDepth2On && aheadBaseDepth < 2 {
		return 2
	}
	return aheadBaseDepth
}

// aheadPrefillMaxDepth is the deepest K any prefill reads at: the written
// parents' post-states the pipeline keeps are K-1.
func aheadPrefillMaxDepth() int {
	if k := aheadStartKeysDepth(); k > aheadBaseDepth {
		return k
	}
	return aheadBaseDepth
}

// Claims that took a start-keys prefill, and those that found one not done
// or not usable (process-wide, since start).
var aheadStartKeysUsed, aheadStartKeysUnused atomic.Uint64

// AheadStartKeysStats returns the claims that took a start-keys prefill and
// those that did not.
func AheadStartKeysStats() (used, unused uint64) {
	return aheadStartKeysUsed.Load(), aheadStartKeysUnused.Load()
}

// aheadStartKeysExtraForTest adds keys to a start-keys prefill (tests: keys
// a block's transactions read, whose last writer is the parent or an older
// ancestor).
var aheadStartKeysExtraForTest func(hdr *block.Header) []qmdb.Hash

// aheadStartKeysEndForTest sees every start-keys prefill as its goroutine
// ends, before done closes.
var aheadStartKeysEndForTest func(ab *aheadBase)

// startAheadStartKeys starts the start-keys prefill of the block of hdr.
// abort reports that the block will not use it.
func (bc *BlockChain) startAheadStartKeys(hdr *block.Header, sp *StateProcessor, abort func() bool) *aheadBase {
	ab := &aheadBase{stop: make(chan struct{}), done: make(chan struct{}), readersDone: make(chan struct{})}
	// It never reads the block's transactions.
	close(ab.readersDone)
	go bc.runAheadStartKeys(ab, hdr, sp, abort)
	return ab
}

func (bc *BlockChain) runAheadStartKeys(ab *aheadBase, hdr *block.Header, sp *StateProcessor, abort func() bool) {
	defer close(ab.done)
	if fn := aheadStartKeysEndForTest; fn != nil {
		defer fn(ab)
	}
	defer func() {
		if r := recover(); r != nil {
			ab.pf, ab.skip = nil, "panic"
			log.Warn("ahead start-keys prefill panicked", "number", hdr.Number.Uint64(), "err", r)
		}
	}()
	chain, st0, ok := bc.aheadBaseWaitTreeDepth(ab, hdr, aheadStartKeysDepth())
	if !ok {
		return
	}
	stop := func() bool {
		if abort() {
			return true
		}
		select {
		case <-ab.stop:
			ab.cut.Store(true)
			return true
		default:
			return false
		}
	}
	ab.phase.Store(abPhaseFill)
	tFill := time.Now()
	// The base prefill's own extra keys: the fee recipients, then what the
	// block-start calls read.
	keys := []qmdb.Hash{qmdb.Hash(commitment.AccountKeyHash(hdr.Coinbase))}
	for _, a := range deferredFeeRecipients(sp.config, hdr) {
		keys = append(keys, qmdb.Hash(commitment.AccountKeyHash(a)))
	}
	keys = append(keys, bc.blockStartKeys(hdr, sp)...)
	if fn := aheadStartKeysExtraForTest; fn != nil {
		keys = append(keys, fn(hdr)...)
	}
	ab.keys = time.Since(tFill)
	if stop() {
		ab.took = time.Since(tFill)
		ab.skip = "stopped before the fill"
		return
	}
	rc := bc.qmdbRootComputer
	pf, err := rc.FillBasePrefill(bc.ChainDB, [][]qmdb.Hash{keys}, 1, stop)
	ab.took = time.Since(tFill)
	if err != nil {
		ab.skip = "fill: " + err.Error()
		return
	}
	if ab.cut.Load() {
		// Stopped: no run takes it.
		ab.skip = "stopped during the fill"
		return
	}
	states, ok := rc.AppliedSince(st0.Gen)
	if !ok || !aheadBaseMovedOK(chain, st0.Number, states) {
		ab.skip = "tree changed under the fill"
		return
	}
	ab.pf = pf
}

// settleStartKeys decides at the claim (pr computed, its base prefill halted
// or detached) whether the run reads through pr's start-keys prefill: only
// with no usable base prefill to read through and only when it is done
// already. One not taken is stopped.
func (pr *aheadPrep) settleStartKeys() {
	sk := pr.startKeys
	if sk == nil {
		return
	}
	switch {
	case !pr.ok:
		pr.startKeysWhy = "prep not ok"
	case !pr.baseDetached && pr.base != nil:
		pr.startKeysWhy = "base usable"
	default:
		select {
		case <-sk.done:
			pr.startKeysAt, pr.startKeysDepth = sk.at, sk.depth
			if sk.pf != nil {
				pr.startKeysUsed = true
				pr.startKeysWhy = "used"
				aheadStartKeysUsed.Add(1)
				return
			}
			pr.startKeysWhy = "skip: " + sk.skip
		default:
			if sk.phase.Load() == abPhaseFill {
				pr.startKeysWhy = "not done: filling"
			} else {
				pr.startKeysWhy = "not done: waiting for the tree"
			}
		}
	}
	aheadStartKeysUnused.Add(1)
	sk.once.Do(func() { close(sk.stop) })
}

// startKeysLogFields returns the start-keys stamps for the "parallel block"
// line: why the claim took the prefill or not (a taken one the claim's
// layering dropped afterwards reads "dropped: " and its reason), and the
// tree height and depth it read at (0 when it never read).
func (pr *aheadPrep) startKeysLogFields() []any {
	return pr.startKeysLog().fields()
}

// startKeysStamp is the start-keys part of a preparation, kept by a run
// that drops its preparation (parallel_processor.go clears prep once the
// affinity pass is adopted, before it logs). sk stays referenced so a
// prefill the claim's layering dropped afterwards still reads "dropped: ".
type startKeysStamp struct {
	sk        *aheadBase
	used      bool
	why       string
	at, depth uint64
}

// startKeysLog snapshots pr's start-keys stamps (zero for a nil pr).
func (pr *aheadPrep) startKeysLog() startKeysStamp {
	if pr == nil {
		return startKeysStamp{}
	}
	return startKeysStamp{sk: pr.startKeys, used: pr.startKeysUsed, why: pr.startKeysWhy, at: pr.startKeysAt, depth: pr.startKeysDepth}
}

// fields returns the stamps as startKeysLogFields documents them.
func (s startKeysStamp) fields() []any {
	if s.sk == nil {
		return []any{"startKeysWhy", "", "startKeysAt", uint64(0), "startKeysDepth", uint64(0)}
	}
	why := s.why
	if s.used && s.sk.pf == nil {
		why = "dropped: " + s.sk.skip
	}
	return []any{"startKeysWhy", why, "startKeysAt", s.at, "startKeysDepth", s.depth}
}
