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
	chain, st0, ok := bc.aheadBaseWaitTree(ab, hdr)
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
	if pr.ok && (pr.baseDetached || pr.base == nil) {
		select {
		case <-sk.done:
			if sk.pf != nil {
				pr.startKeysUsed = true
				aheadStartKeysUsed.Add(1)
				return
			}
		default:
		}
	}
	aheadStartKeysUnused.Add(1)
	sk.once.Do(func() { close(sk.stop) })
}
