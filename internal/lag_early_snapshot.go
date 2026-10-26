// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/parallel"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
)

// N42_LAG_EARLY_SNAPSHOT=1: the child starts at its parent's exec end
// (docs/GOV5_5M_GAP_20261026.md section 5.1).
//
// Without it a child's ahead run starts at its parent's root post-state
// hook, after the parent's collect (the fold of its Block-STM writes into
// its state, the receipts, the fee credits), block end and Finalize:
// ~45 ms of the ~116 ms loop on the fleet (S136b) between "the parent's
// transactions executed" and "the child may read the parent's state".
//
// With it, the ahead run of block N (the spec) publishes an early snapshot
// of N the moment its Block-STM run ends: a reader layer over N's final
// MVS versions (the store is kept alive until nothing reads it) and the
// writes of N's block-start system calls, captured before the run. N's
// queued child then starts its own ahead run (the next run) on that layer
// over N's parent's layers, while N's collect, its import (block end,
// Finalize, root) and N-1's write proceed. When N's import publishes the
// complete post-state at its root hook, the early snapshot switches to it
// (every later read is answered there) and the next run becomes the spec.
//
// What N writes after its transactions' fold -- the fee credits, the
// block-end system calls, the rewards -- is not in the early snapshot.
// Reads of a hold set of addresses (the fee recipients and every address
// such a step wrote in an earlier block) wait for the complete post-state.
// N's state records the addresses written after the fold
// (IntraBlockState.StartWriteRecord); at the hook, a write outside the
// hold set means the child may have read a stale value: its run is
// discarded at take (serial path) and the address joins the hold set.
// Nothing else differs: the fold applies exactly the final MVS versions
// the early snapshot answers.
//
// Default off.
var lagEarlySnapOn = parseOptIn(os.Getenv("N42_LAG_EARLY_SNAPSHOT"))

// SetLagEarlySnapshotForTest switches N42_LAG_EARLY_SNAPSHOT and returns
// the restore.
func SetLagEarlySnapshotForTest(on bool) func() {
	prev := lagEarlySnapOn
	lagEarlySnapOn = on
	return func() { lagEarlySnapOn = prev }
}

// lagEarlyAfterFoldForTest runs on a block whose early snapshot was
// published, right after its fold (tests: a write after the fold).
var lagEarlyAfterFoldForTest func(ibs *state.IntraBlockState, txs []*transaction.Transaction)

// lagEarlyHoldMax bounds the learned hold set.
const lagEarlyHoldMax = 64

var errEarlyDropped = errors.New("lag pipeline: early snapshot dropped (its block's execution was discarded)")

// earlySnap is the early snapshot of block hash (the state.EarlySource of
// its PostState).
type earlySnap struct {
	hash   types.Hash
	number uint64
	header *block.Header
	at     time.Time

	mvs    *parallel.MVS
	numTxs int
	// start: the block-start system calls' writes; below: the complete
	// post-state of the block's parent, the next run's layer under start;
	// belowParent: the parent's parent, whose write may still be pending.
	start       *state.PostState
	below       *state.PostState
	belowParent types.Hash
	hold        []types.Address
	post        *state.PostState // the early layer itself

	full    atomic.Pointer[state.PostState]
	done    chan struct{}
	endOnce sync.Once
	dead    atomic.Bool
	// ok: the block wrote nothing after its fold outside hold (set before
	// done closes on publication).
	ok atomic.Bool

	// refs: one for the publisher until publication or drop, one per run
	// reading it until that run's goroutine ends; free (the executor's
	// Release) runs when the last goes.
	refs atomic.Int32
	free func()
}

// earlyHoldWaits counts the reads that waited for a complete post-state
// (held-back addresses).
var earlyHoldWaits atomic.Uint64

func (es *earlySnap) unref() {
	if es.refs.Add(-1) == 0 && es.free != nil {
		es.free()
	}
}

// end publishes the complete post-state (full non-nil) or drops the
// snapshot; once.
func (es *earlySnap) end(full *state.PostState, ok bool) {
	es.endOnce.Do(func() {
		if full != nil {
			es.ok.Store(ok)
			es.full.Store(full)
		} else {
			es.dead.Store(true)
		}
		close(es.done)
		// below and start stay: a next run may still be building its
		// layers from them. Neither reaches an earlier early snapshot
		// (below is a complete post-state), so no chain of them stays
		// reachable.
		es.unref()
	})
}

func (es *earlySnap) EarlyAccount(addr types.Address) (*account.StateAccount, *uint256.Int, bool, bool) {
	full, _, _, found, delta := es.mvs.ReadAccount(parallel.LocationKey{Address: addr, Field: parallel.FieldBalance}, es.numTxs)
	if !found {
		if delta == nil {
			return nil, nil, false, false
		}
		return nil, delta, true, true
	}
	if full == nil && delta == nil {
		return nil, nil, false, true
	}
	acc := &account.StateAccount{Initialised: true}
	if full != nil {
		dec, err := parallel.DecodeAccount(full)
		if err != nil {
			// Not answerable here: the complete post-state answers it.
			return es.waitAccount(addr)
		}
		acc = dec
	}
	if delta != nil {
		acc.Balance.Add(&acc.Balance, delta)
	}
	return acc, nil, false, true
}

// waitAccount answers addr from the complete post-state (an encoding the
// early read cannot decode; a dropped snapshot answers absent, and its
// reader's run is discarded).
func (es *earlySnap) waitAccount(addr types.Address) (*account.StateAccount, *uint256.Int, bool, bool) {
	f, err := es.WaitFull()
	if err != nil {
		return nil, nil, false, true
	}
	a, ok := f.AccountOf(addr)
	if !ok {
		return nil, nil, false, false
	}
	return a, nil, false, true
}

func (es *earlySnap) EarlyStorage(addr types.Address, key *types.Hash) ([]byte, bool) {
	acc, _, onBase, found := es.EarlyAccount(addr)
	if found && !onBase && (acc == nil || state.AccountIsEmpty(acc)) {
		// Removed: its storage reads zero, as the fold leaves it.
		return nil, true
	}
	if v, _, _, ok := es.mvs.Read(parallel.LocationKey{Address: addr, Field: parallel.FieldStorage, Slot: *key}, es.numTxs); ok {
		return v, true
	}
	if w, _, _, ok := es.mvs.Read(parallel.LocationKey{Address: addr, Field: parallel.FieldStorageWipe}, es.numTxs); ok && w != nil {
		return nil, true
	}
	return nil, false
}

func (es *earlySnap) EarlyCode(addr types.Address, _ types.Hash) ([]byte, bool) {
	if c, _, _, ok := es.mvs.Read(parallel.LocationKey{Address: addr, Field: parallel.FieldCode}, es.numTxs); ok && c != nil {
		return c, true
	}
	return nil, false
}

func (es *earlySnap) Hold(addr types.Address) bool {
	for i := range es.hold {
		if es.hold[i] == addr {
			earlyHoldWaits.Add(1)
			return true
		}
	}
	return false
}

func (es *earlySnap) Full() *state.PostState { return es.full.Load() }

func (es *earlySnap) WaitFull() (*state.PostState, error) {
	<-es.done
	if f := es.full.Load(); f != nil {
		return f, nil
	}
	return nil, errEarlyDropped
}

// lagEarlyStats: early snapshots published, next runs launched on them,
// next runs used by their import, discarded because the block wrote
// outside the hold set, early snapshots dropped.
type lagEarlyStats struct {
	published, launched, used, unsafe, dropped atomic.Uint64
}

// lagPipeEarly publishes the early snapshot of a's block (a is the spec or
// the run its import took and waits for; its Block-STM run just ended) and launches its queued child on it. It
// reports whether the snapshot took the run's store (then free is called
// once nothing reads it, and the caller must not release the executor).
func (bc *BlockChain) lagPipeEarly(a *aheadExec, mvs *parallel.MVS, numTxs int, start *state.PostState, free func()) bool {
	p := &bc.lagPipe
	p.mu.Lock()
	if (p.spec != a && p.taken != a) || p.early != nil || p.next != nil {
		p.mu.Unlock()
		return false
	}
	hdr := a.blk.Header().(*block.Header)
	below := a.parPost
	if src := below.Early(); src != nil {
		// a was promoted at its parent's hook: the parent is complete.
		if below = src.Full(); below == nil {
			p.mu.Unlock()
			return false
		}
	}
	es := &earlySnap{hash: a.blk.Hash(), number: hdr.Number.Uint64(), header: hdr, at: time.Now(),
		mvs: mvs, numTxs: numTxs, start: start, below: below, belowParent: a.parHeader.ParentHash,
		done: make(chan struct{}), free: free}
	es.hold = append(deferredFeeRecipients(bc.chainConfig, hdr), p.earlyHold...)
	es.post = state.NewEarlyPostState(es)
	es.refs.Store(1)
	p.early = es
	p.earlyStats.published.Add(1)
	p.mu.Unlock()
	bc.lagPipeLaunchEarly(lagLaunchHook)
	return true
}

// lagPipeLaunchEarly starts the next run: the queued child of the early
// snapshot's block, if there is one and no next run yet.
func (bc *BlockChain) lagPipeLaunchEarly(by int) {
	if !lagEarlySnapOn || !bc.lagPipeUsable() {
		return
	}
	p := &bc.lagPipe
	p.mu.Lock()
	es := p.early
	if es == nil || p.next != nil || es.dead.Load() || es.full.Load() != nil {
		p.mu.Unlock()
		return
	}
	child := bc.lagQueuedChild(es.hash)
	if child == nil {
		p.mu.Unlock()
		return
	}
	par := &lagPipeParent{hash: es.hash, number: es.number, header: es.header, post: es.post, hookAt: es.at, launched: true}
	a := p.newAheadLocked(child, par, by)
	a.es = es
	es.refs.Add(1)
	p.next = a
	p.earlyStats.launched.Add(1)
	p.mu.Unlock()
	go bc.runAhead(a, par)
}

// dropEarlyLocked drops the early snapshot and its next run (p.mu held).
func (p *lagPipeline) dropEarlyLocked() {
	if a := p.next; a != nil {
		p.next = nil
		p.discardLocked(a, &p.discSuperseded)
	}
	if es := p.early; es != nil {
		p.early = nil
		p.earlyStats.dropped.Add(1)
		// Wakes the next run's held reads (they fail, the run is
		// discarded).
		es.end(nil, false)
	}
}

// dropEarlyOfLocked drops the early snapshot when it is hash's or its next
// run is hash's block (p.mu held).
func (p *lagPipeline) dropEarlyOfLocked(hash types.Hash) {
	if (p.early != nil && p.early.hash == hash) || (p.next != nil && p.next.blk.Hash() == hash) {
		p.dropEarlyLocked()
	}
}

// earlyPublishLocked switches the early snapshot of the block h whose
// complete post-state is being published (p.mu held): written holds the
// addresses the block wrote after its fold. The next run becomes the spec.
// An early snapshot of another block is dropped.
func (p *lagPipeline) earlyPublishLocked(h types.Hash, post *state.PostState, written map[types.Address]struct{}) {
	es := p.early
	if es == nil {
		return
	}
	if es.hash != h || written == nil {
		// Not this block's, or its import did not record its writes
		// (another path imported it).
		p.dropEarlyLocked()
		return
	}
	ok := true
	for addr := range written {
		if !es.holds(addr) {
			ok = false
			p.learnHoldLocked(addr)
		}
	}
	if !ok {
		p.earlyStats.unsafe.Add(1)
		log.Debug("lag pipeline: early snapshot missed a write after the fold", "number", es.number, "hash", h.Hex()[:12], "written", len(written))
	}
	p.early = nil
	es.end(post, ok)
	if a := p.next; a != nil {
		p.next = nil
		if p.spec == nil {
			a.parentOK = false
			p.spec = a
		} else {
			p.discardLocked(a, &p.discSuperseded)
		}
	}
}

func (es *earlySnap) holds(addr types.Address) bool {
	for i := range es.hold {
		if es.hold[i] == addr {
			return true
		}
	}
	return false
}

func (p *lagPipeline) learnHoldLocked(addr types.Address) {
	for _, a := range p.earlyHold {
		if a == addr {
			return
		}
	}
	if len(p.earlyHold) >= lagEarlyHoldMax {
		return
	}
	p.earlyHold = append(p.earlyHold, addr)
}

// lagEarlyLogFields are the early-snapshot counters for the pipeline's log
// line.
func (bc *BlockChain) lagEarlyLogFields() []interface{} {
	s := &bc.lagPipe.earlyStats
	return []interface{}{"earlyPub", s.published.Load(), "earlyLaunch", s.launched.Load(), "earlyUsed", s.used.Load(),
		"earlyUnsafe", s.unsafe.Load(), "earlyDropped", s.dropped.Load(), "earlyHeldReads", earlyHoldWaits.Load()}
}
