// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"weak"

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

// N42_LAG_EARLY_RETRY=1 (default off; only meaningful with
// N42_LAG_EARLY_SNAPSHOT=1): an early offer refused only because the
// block's parent has not reached its root hook yet is parked and published
// at that hook instead of being lost.
//
// The refusal protects the bounded layer stack. A next run (the child of
// the early snapshot's block) reads early(N) / start(N) / complete(N-1) /
// pending write of N-2: the layer under an early snapshot must be a
// complete post-state, never another early snapshot, so that no chain of
// early snapshots (and of the executors they keep alive) builds up, and
// the pipeline holds one early snapshot and one next run at a time. When
// N's own run is still the next run on N-1's early snapshot as its
// Block-STM ends (N-1 has not hooked), N's snapshot would have to sit on
// N-1's early layer, so it is refused. Without the retry the child N+1
// then launches only at N's own hook, ~130 ms later on the fleet.
//
// Parked, the offer keeps the run's store (it counts as taken: the run
// does not release the executor, the parked snapshot does). At N-1's hook
// (earlyPublishLocked), when N's run is promoted to the spec, the parked
// snapshot is published exactly as a fresh offer would be at that moment
// -- on N-1's complete post-state -- and N+1 launches on it. In every other
// outcome (N's run discarded or superseded, N-1 settled without its hook,
// the pipeline reset or the block forgotten, N's take before the
// promotion) the parked snapshot is dropped with the next run: the drop
// releases its publisher reference once, the run its own when its
// goroutine ends.
var lagEarlyRetryOn = parseOptIn(os.Getenv("N42_LAG_EARLY_RETRY"))

// SetLagEarlyRetryForTest switches N42_LAG_EARLY_RETRY and returns the
// restore.
func SetLagEarlyRetryForTest(on bool) func() {
	prev := lagEarlyRetryOn
	lagEarlyRetryOn = on
	return func() { lagEarlyRetryOn = prev }
}

// N42_LAG_EARLY_DEPTH2=1 (default off; only meaningful with
// N42_LAG_EARLY_SNAPSHOT=1): a next run whose Block-STM ends before its
// parent's root hook publishes its early snapshot on the parent's early
// snapshot, and its child starts on it at once, instead of the offer being
// refused (or parked until that hook, N42_LAG_EARLY_RETRY: run 67, 14% of
// the early launches, 63 ms each, 8.9 ms a block).
//
// The stack stays bounded at two early layers: early(N) over early(N-1)
// over complete(N-2) (mid holds early(N-1)'s layer and block-start writes,
// below the complete post-state, parentEs early(N-1)). A snapshot is only
// stacked on one whose own layers below are complete (stackBelow: a
// snapshot that was itself stacked counts once its parent's complete
// post-state is published, and is then read through that post-state), so a
// third early layer never forms; an offer that would need one is parked
// (with N42_LAG_EARLY_RETRY) or refused. Slots: early/next (the parent's
// snapshot and the run on it, as before), early2/next2 (the stacked
// snapshot and the run on it).
//
// What a run on the stack reads, and why each read is the serial value:
//   - Each early layer answers from its block's final MVS versions, which
//     its fold applies unchanged; reads of its hold set wait for its
//     complete post-state; and every read that went to the layers below
//     re-checks that layer's complete post-state afterwards (a store or
//     tree value that already holds the block was read after its hook
//     published it). Both layers do this independently, so a value of N-1
//     or N reaching the reader through the store is caught by N-1's or N's
//     own re-check.
//   - What N-1 wrote after its fold outside its hold set makes N-1's
//     snapshot unsafe at N-1's hook: the stacked snapshot and its run are
//     dropped there (N's run read the same layer and is discarded at take,
//     as before), and a run's take also checks the snapshot under its
//     parent's (esCheck). What N wrote after its fold is checked at N's
//     hook, as for a single layer.
//   - Any drop of N-1's snapshot or of N's run (discard, reset, forget,
//     failed or foreign import of N-1 or N) drops the stacked snapshot and
//     its run with it (dropEarlyLocked, discardLocked, dropEarlyOfLocked,
//     lagPipeSettle).
//   - The pending-write layer is the one under complete(N-2): N-3's, the
//     same relation as a single early layer one block lower.
//
// At N-1's hook the stack rotates: N's run becomes the spec, early2/next2
// become early/next (early(N) now reads N-1 through its complete
// post-state), and a parked offer of the new next run is stacked on
// early(N). References: the stacked run holds one on each of the two
// snapshots it reads until its goroutine ends; a run started on a rotated
// snapshot reads the lower one only through its complete post-state and
// holds none on it.
var lagEarlyDepth2On = parseOptIn(os.Getenv("N42_LAG_EARLY_DEPTH2"))

// SetLagEarlyDepth2ForTest switches N42_LAG_EARLY_DEPTH2 and returns the
// restore.
func SetLagEarlyDepth2ForTest(on bool) func() {
	prev := lagEarlyDepth2On
	lagEarlyDepth2On = on
	return func() { lagEarlyDepth2On = prev }
}

// N42_LAG_EARLY_PREFILL=1 (default off): a run started on an early
// snapshot keeps its base prefill (ahead_base_prefill.go) instead of
// dropping it, when the prefill was read on the tree at or above the parent
// of the complete post-state at the bottom of the run's layers.
//
// Run 67 (B2): 872 of 994 early launches dropped a prefill read on the tree
// at the grandparent (N-2, the complete layer itself), so their block-start
// calls read 3 keys through the tree's lock (2.4 ms mean, 11 ms p90, behind
// a root's apply) and every worker read through the tree; hook launches,
// which keep theirs, answer those reads from the prefill (0.4 ms).
//
// Why the values are right: the prefill holds, for each key, the tree's
// value in the state of a block h of the child's own chain with at <= h <=
// the child's parent (its fill checks that only roots of that chain ran
// while it read). Under the layers -- early snapshots, block-start writes,
// the complete post-state of block C, the pending write of C-1 -- a base
// value of state h is the serial one when C-1 <= h <= C, the layers
// answering every key C (and an unwritten C-1) changed: exactly the live
// tree's case. When h is an early layer's block E, the value was read after
// E's root applied, so after E's hook published its complete post-state;
// every early read re-checks that post-state after reading the layers below
// it (state/early_post_state.go), so such a read is answered from E's
// complete post-state, as a live tree read that saw E is. Below C-1 a layer
// would be missing: the prefill is dropped.
var lagEarlyPrefillOn = parseOptIn(os.Getenv("N42_LAG_EARLY_PREFILL"))

// SetLagEarlyPrefillForTest switches N42_LAG_EARLY_PREFILL and returns the
// restore.
func SetLagEarlyPrefillForTest(on bool) func() {
	prev := lagEarlyPrefillOn
	lagEarlyPrefillOn = on
	return func() { lagEarlyPrefillOn = prev }
}

// lagEarlyPrefillUsed counts runs on an early snapshot that kept their base
// prefill (process-wide).
var lagEarlyPrefillUsed atomic.Uint64

// earlyPrefillUsable reports whether a run on es may read through the base
// prefill ab (see N42_LAG_EARLY_PREFILL).
func earlyPrefillUsable(ab *aheadBase, es *earlySnap) bool {
	if ab == nil || ab.pf == nil || es == nil || es.below == nil || es.belowNum == 0 {
		return false
	}
	return ab.at+1 >= es.belowNum && ab.at <= es.number
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

	// mvs: the block's store, cleared with start, below and free once the
	// last reference goes (release): nothing that still reaches this
	// snapshot -- the next block's state layers its post -- may keep the
	// store or the executor reachable (run 66, see release).
	mvs    atomic.Pointer[parallel.MVS]
	numTxs int
	// start: the block-start system calls' writes; below: the complete
	// post-state of the block's parent, the next run's layer under start;
	// belowParent: the parent's parent, whose write may still be pending.
	start       *state.PostState
	below       *state.PostState
	belowParent types.Hash
	// belowNum: the height of below's block.
	belowNum uint64
	// mid, parentEs (N42_LAG_EARLY_DEPTH2): a snapshot published while its
	// block's parent had not hooked sits on the parent's early snapshot:
	// mid is that snapshot's layer and its block-start writes, between
	// start and below (below then the grandparent's complete post-state),
	// parentEs the parent's snapshot (its ok is checked at take; no
	// reference: readers hold their own). Nil otherwise.
	mid      []*state.PostState
	parentEs *earlySnap
	hold     []types.Address
	post     *state.PostState // the early layer itself

	full    atomic.Pointer[state.PostState]
	done    chan struct{}
	endOnce sync.Once
	dead    atomic.Bool
	// ok: the block wrote nothing after its fold outside hold (set before
	// done closes on publication).
	ok atomic.Bool

	// heldNanos: the time reads spent waiting for the complete post-state
	// (WaitFull; diagnostics).
	heldNanos atomic.Int64

	// refs: one for the publisher until publication or drop, one for the
	// publishing run until its goroutine ends (its fold and receipts read
	// the executor after the offer), one per run reading it until that
	// run's goroutine ends; free (the executor's Release) runs when the
	// last goes.
	refs atomic.Int32
	free func()
}

// earlyHoldWaits counts the reads that waited for a complete post-state
// (held-back addresses).
var earlyHoldWaits atomic.Uint64

func (es *earlySnap) unref() {
	n := es.refs.Add(-1)
	if fn := lagEarlyUnrefForTest; fn != nil {
		fn(es, n)
	}
	if n == 0 {
		es.release()
	}
}

// release runs when the last reference goes: the snapshot has ended (its
// complete post-state is published, or it was dropped) and no run reads it
// any more. It hands the executor back and drops every reference to the
// block's run.
//
// The snapshot itself stays reachable: the next block's run layers its post
// (a.parPost, the run's state), and that state lives as long as the block's
// import, write and the executor that ran it -- whose Block-STM closures
// reach the run's state. A snapshot that kept its executor (free is the
// executor's bound Release) or its store therefore kept the previous
// block's run reachable, its state the snapshot below, and so on: with
// every block on its parent's early snapshot (N42_LAG_EARLY_RETRY, run 66)
// an unbounded chain of stores, ~130 MB a block; without the retry a chain
// as long as the run of consecutive early launches. Reads after this point
// are answered by the complete post-state (every early read checks Full
// first, and again after the layers below); a dropped snapshot's readers
// are discarded.
func (es *earlySnap) release() {
	if free := es.free; free != nil {
		es.free = nil
		free()
	}
	es.mvs.Store(nil)
	es.start, es.below = nil, nil
	es.mid, es.parentEs = nil, nil
	earlySnapsLive.Add(-1)
}

// earlySnapsLive counts the early snapshots made (published or parked)
// whose store is not released yet; earlyRecent holds the last ones weakly,
// to count how many are still reachable (lagEarlyLogFields).
var earlySnapsLive atomic.Int64

const earlyRecentN = 64

var earlyRecent struct {
	mu   sync.Mutex
	next int
	ring [earlyRecentN]weak.Pointer[earlySnap]
}

// noteEarlySnap counts a new snapshot (its refs set).
func noteEarlySnap(es *earlySnap) {
	earlySnapsLive.Add(1)
	earlyRecent.mu.Lock()
	earlyRecent.ring[earlyRecent.next%earlyRecentN] = weak.Make(es)
	earlyRecent.next++
	earlyRecent.mu.Unlock()
}

// earlyReachable counts the last snapshots made that are still reachable
// (not collected yet: a few are expected, a chain shows as nearly all).
func earlyReachable() int {
	earlyRecent.mu.Lock()
	defer earlyRecent.mu.Unlock()
	n := 0
	for i := range earlyRecent.ring {
		if earlyRecent.ring[i].Value() != nil {
			n++
		}
	}
	return n
}

// lagEarlyUnrefForTest sees every reference an early snapshot drops, with
// the count left (tests: the store is released exactly once).
var lagEarlyUnrefForTest func(es *earlySnap, left int32)

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
	mvs := es.mvs.Load()
	if mvs == nil {
		// Released: the reader's check of Full after the layers below
		// answers it from the complete post-state.
		return nil, nil, false, false
	}
	full, _, _, found, delta := mvs.ReadAccount(parallel.LocationKey{Address: addr, Field: parallel.FieldBalance}, es.numTxs)
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
	mvs := es.mvs.Load()
	if mvs == nil {
		return nil, false // released: see EarlyAccount
	}
	if v, _, _, ok := mvs.Read(parallel.LocationKey{Address: addr, Field: parallel.FieldStorage, Slot: *key}, es.numTxs); ok {
		return v, true
	}
	if w, _, _, ok := mvs.Read(parallel.LocationKey{Address: addr, Field: parallel.FieldStorageWipe}, es.numTxs); ok && w != nil {
		return nil, true
	}
	return nil, false
}

func (es *earlySnap) EarlyCode(addr types.Address, _ types.Hash) ([]byte, bool) {
	mvs := es.mvs.Load()
	if mvs == nil {
		return nil, false // released: see EarlyAccount
	}
	if c, _, _, ok := mvs.Read(parallel.LocationKey{Address: addr, Field: parallel.FieldCode}, es.numTxs); ok && c != nil {
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
	select {
	case <-es.done:
	default:
		t0 := time.Now()
		<-es.done
		es.heldNanos.Add(int64(time.Since(t0)))
	}
	if f := es.full.Load(); f != nil {
		return f, nil
	}
	return nil, errEarlyDropped
}

// lagEarlyStats: early snapshots published, next runs launched on them,
// next runs used by their import, discarded because the block wrote
// outside the hold set, early snapshots dropped.
// parked, parkedPublished, parkedDropped: offers parked
// (N42_LAG_EARLY_RETRY), and how they ended.
type lagEarlyStats struct {
	published, launched, used, unsafe, dropped atomic.Uint64
	parked, parkedPublished, parkedDropped     atomic.Uint64
	// stacked, stackedLaunched: snapshots published on an early snapshot
	// and runs started on them (N42_LAG_EARLY_DEPTH2); each also counts in
	// published / launched.
	stacked, stackedLaunched atomic.Uint64
}

// stackBelow returns what a snapshot stacked on es (its block's parent's
// early snapshot, not hooked yet) has under its own block-start writes: es's
// layer and block-start writes (mid), then es's own layers below collapsed
// to one complete post-state. ok is false when that is not possible: es
// sits on an early snapshot whose complete post-state is not published (a
// third early layer) or that was unsafe or dropped.
func (es *earlySnap) stackBelow() (mid []*state.PostState, below *state.PostState, belowParent types.Hash, belowNum uint64, ok bool) {
	if es.post == nil {
		return nil, nil, types.Hash{}, 0, false
	}
	mid = []*state.PostState{es.post}
	if es.start != nil {
		mid = append(mid, es.start)
	}
	if pe := es.parentEs; pe != nil {
		f := pe.Full()
		if f == nil || !pe.ok.Load() || pe.header == nil {
			return nil, nil, types.Hash{}, 0, false
		}
		return mid, f, pe.header.ParentHash, pe.number, true
	}
	if es.below == nil {
		return nil, nil, types.Hash{}, 0, false
	}
	return mid, es.below, es.belowParent, es.belowNum, true
}

// stackLocked fills es (its store, start and hold already set) as a
// snapshot stacked on p.early and publishes it in the early2 slot (p.mu
// held); false when p.early cannot carry it.
func (p *lagPipeline) stackLocked(es *earlySnap) bool {
	pe := p.early
	if pe == nil || p.early2 != nil || p.next2 != nil || pe.dead.Load() || pe.full.Load() != nil {
		return false
	}
	mid, below, bp, bn, ok := pe.stackBelow()
	if !ok {
		return false
	}
	es.mid, es.below, es.belowParent, es.belowNum, es.parentEs = mid, below, bp, bn, pe
	es.at = time.Now()
	es.hold = append(es.hold, p.earlyHold...)
	es.post = state.NewEarlyPostState(es)
	p.early2 = es
	p.earlyStats.published.Add(1)
	p.earlyStats.stacked.Add(1)
	return true
}

// dropEarly2Locked drops the stacked snapshot and the run on it (p.mu
// held); a parked offer of that run goes with it.
func (p *lagPipeline) dropEarly2Locked() {
	if a := p.next2; a != nil {
		p.next2 = nil
		p.discardLocked(a, &p.discSuperseded)
	}
	if es := p.early2; es != nil {
		p.early2 = nil
		p.earlyStats.dropped.Add(1)
		es.end(nil, false)
	}
}

// lagPipeEarly publishes the early snapshot of a's block (a is the spec or
// the run its import took and waits for; its Block-STM run just ended) and launches its queued child on it. It
// reports whether the snapshot took the run's store (then free is called
// once nothing reads it, and the caller must not release the executor).
func (bc *BlockChain) lagPipeEarly(a *aheadExec, mvs *parallel.MVS, numTxs int, start *state.PostState, free func()) bool {
	p := &bc.lagPipe
	p.mu.Lock()
	if lagEarlyDepth2On && p.next == a && a.es != nil && p.early == a.es {
		// a still reads its parent's early snapshot: stack a's on it.
		hdr := a.blk.Header().(*block.Header)
		es := &earlySnap{hash: a.blk.Hash(), number: hdr.Number.Uint64(), header: hdr,
			numTxs: numTxs, start: start, done: make(chan struct{}), free: free}
		es.mvs.Store(mvs)
		es.hold = deferredFeeRecipients(bc.chainConfig, hdr)
		if p.stackLocked(es) {
			es.refs.Store(2)
			noteEarlySnap(es)
			a.pub = es
			p.mu.Unlock()
			bc.lagPipeLaunchEarly(lagLaunchHook)
			return true
		}
	}
	if lagEarlyDepth2On && lagEarlyRetryOn && p.next2 == a && a.es != nil && p.early2 == a.es && p.parked == nil {
		// a reads a stacked snapshot: a third early layer. Park its offer
		// until the stack rotates (earlyPublishLocked), then stack it.
		hdr := a.blk.Header().(*block.Header)
		es := &earlySnap{hash: a.blk.Hash(), number: hdr.Number.Uint64(), header: hdr,
			numTxs: numTxs, start: start, done: make(chan struct{}), free: free}
		es.mvs.Store(mvs)
		es.hold = deferredFeeRecipients(bc.chainConfig, hdr)
		es.refs.Store(2)
		noteEarlySnap(es)
		a.pub = es
		p.parked, p.parkedRun = es, a
		p.earlyStats.parked.Add(1)
		p.mu.Unlock()
		return true
	}
	if lagEarlyRetryOn && p.next == a && a.es != nil && p.early == a.es && p.parked == nil {
		// a still reads its parent's early snapshot: the parent has not
		// hooked, and a snapshot of a's block would have to sit on an early
		// layer. Park it until the parent's hook promotes a.
		hdr := a.blk.Header().(*block.Header)
		es := &earlySnap{hash: a.blk.Hash(), number: hdr.Number.Uint64(), header: hdr,
			numTxs: numTxs, start: start, belowParent: a.parHeader.ParentHash,
			done: make(chan struct{}), free: free}
		es.mvs.Store(mvs)
		es.hold = deferredFeeRecipients(bc.chainConfig, hdr)
		// The parked slot (the publisher once published) and the run.
		es.refs.Store(2)
		noteEarlySnap(es)
		a.pub = es
		p.parked, p.parkedRun = es, a
		p.earlyStats.parked.Add(1)
		p.mu.Unlock()
		return true
	}
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
		numTxs: numTxs, start: start, below: below, belowParent: a.parHeader.ParentHash,
		belowNum: a.parHeader.Number.Uint64(), done: make(chan struct{}), free: free}
	es.mvs.Store(mvs)
	es.hold = append(deferredFeeRecipients(bc.chainConfig, hdr), p.earlyHold...)
	es.post = state.NewEarlyPostState(es)
	es.refs.Store(2)
	noteEarlySnap(es)
	a.pub = es
	p.early = es
	p.earlyStats.published.Add(1)
	p.mu.Unlock()
	bc.lagPipeLaunchEarly(lagLaunchHook)
	return true
}

// publishParkedLocked publishes the parked early snapshot of a, just
// promoted to the spec at its parent's hook, on the parent's complete
// post-state full (p.mu held). It reports whether it published; the caller
// launches the child after unlocking (lagPipeLaunchEarly).
func (p *lagPipeline) publishParkedLocked(a *aheadExec, full *state.PostState) bool {
	es := p.parked
	if es == nil || p.parkedRun != a {
		return false
	}
	p.parked, p.parkedRun = nil, nil
	if p.early != nil || full == nil {
		// Not reachable from earlyPublishLocked (it cleared early and passes
		// the published post-state); dropped rather than stacked.
		p.earlyStats.parkedDropped.Add(1)
		es.end(nil, false)
		return false
	}
	es.below = full
	es.belowNum = es.number - 1
	es.at = time.Now()
	es.hold = append(es.hold, p.earlyHold...)
	es.post = state.NewEarlyPostState(es)
	p.early = es
	p.earlyStats.published.Add(1)
	p.earlyStats.parkedPublished.Add(1)
	return true
}

// dropParkedOfLocked drops the parked early snapshot of run a (p.mu held):
// its publisher reference is released here, the run's when its goroutine
// ends.
func (p *lagPipeline) dropParkedOfLocked(a *aheadExec) {
	es := p.parked
	if es == nil || p.parkedRun != a {
		return
	}
	p.parked, p.parkedRun = nil, nil
	p.earlyStats.parkedDropped.Add(1)
	es.end(nil, false)
}

// lagPipeLaunchEarly starts the next run: the queued child of the early
// snapshot's block, if there is one and no next run yet.
func (bc *BlockChain) lagPipeLaunchEarly(by int) {
	if !lagEarlySnapOn || !bc.lagPipeUsable() {
		return
	}
	p := &bc.lagPipe
	p.mu.Lock()
	var a, a2 *aheadExec
	var par, par2 *lagPipeParent
	if es := p.early; es != nil && p.next == nil && !es.dead.Load() && es.full.Load() == nil {
		if child := bc.lagQueuedChild(es.hash); child != nil {
			par = &lagPipeParent{hash: es.hash, number: es.number, header: es.header, post: es.post, hookAt: es.at, launched: true}
			a = p.newAheadLocked(child, par, by)
			a.es = es
			// A rotated snapshot's lower one is complete and was safe:
			// read through its complete post-state, checked again at take.
			a.esCheck = es.parentEs
			es.refs.Add(1)
			p.next = a
			p.earlyStats.launched.Add(1)
		}
	}
	if es := p.early2; lagEarlyDepth2On && es != nil && p.next2 == nil && !es.dead.Load() && es.full.Load() == nil &&
		es.parentEs != nil && es.parentEs == p.early {
		if child := bc.lagQueuedChild(es.hash); child != nil {
			par2 = &lagPipeParent{hash: es.hash, number: es.number, header: es.header, post: es.post, hookAt: es.at, launched: true}
			a2 = p.newAheadLocked(child, par2, by)
			// The run reads both snapshots' stores: one reference on each
			// (the lower one is published, so it holds its publisher's).
			a2.es, a2.es2, a2.esCheck = es, es.parentEs, es.parentEs
			es.refs.Add(1)
			es.parentEs.refs.Add(1)
			p.next2 = a2
			p.earlyStats.launched.Add(1)
			p.earlyStats.stackedLaunched.Add(1)
		}
	}
	p.mu.Unlock()
	if a != nil {
		go bc.runAhead(a, par)
	}
	if a2 != nil {
		go bc.runAhead(a2, par2)
	}
}

// dropEarlyLocked drops the early snapshot and its next run (p.mu held).
func (p *lagPipeline) dropEarlyLocked() {
	// The stacked snapshot sits on early, its run on both.
	p.dropEarly2Locked()
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
		return
	}
	if (p.early2 != nil && p.early2.hash == hash) || (p.next2 != nil && p.next2.blk.Hash() == hash) {
		p.dropEarly2Locked()
	}
}

// earlyPublishLocked switches the early snapshot of the block h whose
// complete post-state is being published (p.mu held): written holds the
// addresses the block wrote after its fold. The next run becomes the spec.
// An early snapshot of another block is dropped. It reports whether the
// next run's parked early snapshot was published (N42_LAG_EARLY_RETRY): the
// caller then launches its child (lagPipeLaunchEarly) after unlocking.
func (p *lagPipeline) earlyPublishLocked(h types.Hash, post *state.PostState, written map[types.Address]struct{}) bool {
	es := p.early
	if es == nil {
		return false
	}
	if es.hash != h || written == nil {
		// Not this block's, or its import did not record its writes
		// (another path imported it).
		p.dropEarlyLocked()
		return false
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
	if !ok {
		// The stacked snapshot and its run read h's unsafely too.
		p.dropEarly2Locked()
	}
	p.early = nil
	es.end(post, ok)
	if a := p.next; a != nil {
		p.next = nil
		if p.spec == nil {
			a.parentOK = false
			p.spec = a
			// A parked snapshot of a's block now sits on h's complete
			// post-state, as an offer made from here on would. Unless a
			// read h's early snapshot unsafely: its take discards it.
			if !ok {
				p.dropParkedOfLocked(a)
				return false
			}
			if es2 := p.early2; es2 != nil {
				// N42_LAG_EARLY_DEPTH2: a's stacked snapshot and the run on
				// it move down; a parked offer of that run is stacked now.
				p.early, p.next = es2, p.next2
				p.early2, p.next2 = nil, nil
				p.publishParked2Locked()
				return true
			}
			return p.publishParkedLocked(a, post)
		}
		// Drops a's parked snapshot with it.
		p.discardLocked(a, &p.discSuperseded)
	}
	return false
}

// publishParked2Locked stacks the parked offer of the next run on the early
// snapshot it reads, just rotated down (N42_LAG_EARLY_DEPTH2, p.mu held);
// it is dropped when the stack cannot carry it.
func (p *lagPipeline) publishParked2Locked() {
	es := p.parked
	if es == nil || p.next == nil || p.parkedRun != p.next || p.next.es != p.early {
		return
	}
	p.parked, p.parkedRun = nil, nil
	if !p.stackLocked(es) {
		p.earlyStats.parkedDropped.Add(1)
		es.end(nil, false)
		return
	}
	p.earlyStats.parkedPublished.Add(1)
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
	f := []interface{}{"earlyPub", s.published.Load(), "earlyLaunch", s.launched.Load(), "earlyUsed", s.used.Load(),
		"earlyUnsafe", s.unsafe.Load(), "earlyDropped", s.dropped.Load(), "earlyHeldReads", earlyHoldWaits.Load(),
		// Snapshots not released yet, and of the last 64 made, how many are
		// still reachable (a chain of stores shows as ~64).
		"earlyLive", earlySnapsLive.Load(), "earlyReach", earlyReachable()}
	if lagEarlyRetryOn {
		f = append(f, "earlyParked", s.parked.Load(), "earlyParkedPublished", s.parkedPublished.Load(),
			"earlyParkedDropped", s.parkedDropped.Load())
	}
	if lagEarlyDepth2On {
		f = append(f, "earlyStacked", s.stacked.Load(), "earlyStackedLaunch", s.stackedLaunched.Load())
	}
	return f
}
