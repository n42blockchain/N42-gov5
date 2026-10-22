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
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// Stage pipelining in the lag executor (docs/QS_MULTI_MILLION_DESIGN.md
// P2a2): block N+1 executes while block N's state root and write run.
//
// The lag executor imports blocks one at a time in parent order, and an
// import is exec, then root, then write. When an available block N is
// imported (by the executor or by another path under the chain lock), the
// moment N's dirty set is final -- rewards applied, just before the root
// computer runs (IntraBlockState.SetRootPostStateHook) -- N's post-state is
// published (the root's own dirty set, an immutable snapshot) and, if N's
// child is queued (available), the child's Block-STM execution starts on
// its own goroutine; a child that becomes available later, while N is
// still rooted or written, starts the moment it is queued
// (ExecuteParallelAhead): its readers see the store at N-1 (written) and
// the live QMDB tree, with N's snapshot layered on top, so they read N's
// post-state without waiting for N's root or write and without touching
// N's IntraBlockState. N's root and write proceed on that same state; the
// child only reads the tree, taking its reader lock per lookup, never for
// its whole run, and an entry N's write evicts under it is re-read through
// a fresh transaction (commitment.NewLookupSourceRefreshing).
//
// When the lag executor then imports the child (its parent is applied), the
// import takes the ahead execution instead of running Block-STM again and
// completes it -- block end, Finalize with the root on the live tree,
// validation, write -- in the usual order, under the chain lock. Writes
// land in parent order because the root and write of every block still run
// on the executor's goroutine, one block at a time.
//
// At most one ahead execution exists (depth 1). It is used only when its
// parent's import, the one that published the snapshot, succeeded and the
// applied head is exactly that parent; otherwise it is discarded and the
// block executes on the serial path. A parent that fails, is forgotten or
// orphaned discards it. N42_LAG_PIPELINE=0 disables the pipeline.
//
// A written parent's snapshot stays published until the next block
// publishes its own (P12): a child that becomes available only after its
// parent's import finished -- at deferred depth 2 the leader proposes N+1
// once its N-1 is applied, so on the fleet one follower import in three
// found its child missing at the parent's settle and ran the serial path,
// +85 ms -- still runs on the ahead path, launched when it is queued or, at
// the latest, by its own import (lagPipeTake), instead of the serial path
// that joins the parent's write and materialises the pending credits.
//
// Header fields are untouched: under deferred execution depth k a header
// carries the result of the block k levels below it, so N's root is first
// needed by header N+k, built only after the leader gate (LagReady) sees N
// written -- the gate stays on the written result.

// lagPipelineOn is N42_LAG_PIPELINE (default on; "0", "false" or "off"
// disables).
var lagPipelineOn = parseLagPipeline(os.Getenv("N42_LAG_PIPELINE"))

func parseLagPipeline(v string) bool {
	switch v {
	case "0", "false", "FALSE", "off", "OFF", "no":
		return false
	}
	return true
}

// SetLagPipelineForTest switches the pipeline and returns the restore.
func SetLagPipelineForTest(on bool) func() {
	prev := lagPipelineOn
	lagPipelineOn = on
	return func() { lagPipelineOn = prev }
}

// lagPipeChildWaitForTest makes the snapshot publication wait this long for
// the child to be queued (tests: a deterministic overlap). Zero in
// production.
var lagPipeChildWaitForTest time.Duration

// lagPipeBeforePublishForTest runs at the snapshot point of a publishing
// import, before the snapshot is published (tests: a child made available
// earlier must wait for it).
var lagPipeBeforePublishForTest func(parent types.Hash)

// lagPipeAfterPublishForTest runs at the snapshot point of a publishing
// import after the child's launch (tests: the root's work after the
// publication, which the child's ahead run overlaps).
var lagPipeAfterPublishForTest func(parent types.Hash)

// lagPipeAheadStartedForTest runs on an ahead execution's goroutine once
// it holds its read transaction (tests: orphan the child mid-run).
var lagPipeAheadStartedForTest func(child *block.Block)

// lagPipeAheadClaimedForTest runs on an ahead execution's goroutine once it
// claimed its preparation, before it reads any state (tests: hold its reads
// until the parent's root applied).
var lagPipeAheadClaimedForTest func(bc *BlockChain, child *block.Block)

// lagPipeAwaitAheadForTest makes the publication wait for the child's ahead
// execution to finish before the parent's root runs (tests: the child can
// then see the parent's writes only through the snapshot, never through the
// live tree).
var lagPipeAwaitAheadForTest bool

// lagPipeFailWriteForTest, when set, fails the next write of that block once
// with a transient error, after its post-state was published.
var lagPipeFailWriteForTest atomic.Pointer[types.Hash]

var errLagPipeInjectedWrite = errors.New("lag pipeline: injected write failure (test)")

type lagPipeline struct {
	mu sync.Mutex
	// parent: the published snapshot of the block being rooted and
	// written now (nil once it is written or failed).
	parent *lagPipeParent
	// spec: the one ahead execution.
	spec *aheadExec
	// older: the snapshots of the last written parents before parent,
	// newest first, at most aheadBaseDepth-1: a child's base prefill read
	// below its parent layers them (aheadLayersLocked). Each is a block's
	// published dirty set, kept alive one block or a few longer than the
	// write that held it.
	older []*lagPipeParent
	// draining: discarded ahead executions whose goroutines may still run.
	// They share the block's transaction objects (the sender cache), so no
	// other execution of the same block starts until they finish.
	draining []*aheadExec

	seq       uint64 // ahead executions launched (under mu)
	launched  atomic.Uint64
	used      atomic.Uint64
	discarded atomic.Uint64
	// Discards by reason (each also counts in discarded), and the ahead
	// wall time of discarded runs that carried transactions.
	discErr, discNotApplicable, discParent, discHash, discHead atomic.Uint64
	discFail, discReset, discForget, discSuperseded            atomic.Uint64
	discFull, discWorkMs                                       atomic.Uint64
	openTxs                                                    atomic.Int64

	// Parents written without a child executed ahead, by the reason the
	// last launch attempt for them gave up (counted once per parent).
	skipNotAvail atomic.Uint64 // no child available (checked) yet
	skipParent   atomic.Uint64 // a block at the child height on another parent
	skipBusy     atomic.Uint64 // another ahead execution in flight
	skipOff      atomic.Uint64 // pipeline disabled or unusable on this chain
	// late: ahead executions launched on a parent whose import had already
	// settled (the child came after it); takeLaunch: those of them launched
	// by the child's own import.
	late       atomic.Uint64
	takeLaunch atomic.Uint64
}

// dropParentLocked unpublishes the snapshot (p.mu held), counting a written
// parent no child ran ahead on by the reason the last attempt gave up.
func (p *lagPipeline) dropParentLocked() {
	par := p.parent
	if par == nil {
		return
	}
	p.parent = nil
	if par.written && par.post != nil {
		p.keepOlderLocked(par)
	}
	if !par.written || par.launched {
		return
	}
	switch par.skip {
	case lagSkipParent:
		p.skipParent.Add(1)
	case lagSkipBusy:
		p.skipBusy.Add(1)
	default:
		p.skipNotAvail.Add(1)
	}
}

// keepOlderLocked records a written parent's snapshot among the older ones
// (p.mu held), dropping the oldest beyond aheadBaseDepth-1.
func (p *lagPipeline) keepOlderLocked(par *lagPipeParent) {
	keep := aheadBaseDepth - 1
	if keep <= 0 {
		p.older = nil
		return
	}
	o := make([]*lagPipeParent, 0, keep)
	o = append(o, par)
	for _, e := range p.older {
		if len(o) == keep {
			break
		}
		if e.hash != par.hash {
			o = append(o, e)
		}
	}
	p.older = o
}

// forgetOlderLocked drops hash's snapshot from the older ones (p.mu held).
func (p *lagPipeline) forgetOlderLocked(hash types.Hash) {
	o := p.older[:0:0]
	for _, e := range p.older {
		if e.hash != hash {
			o = append(o, e)
		}
	}
	p.older = o
}

// aheadLayersLocked returns the post-states of the blocks of par's chain
// from par's parent down to height at+1, newest first, the block at at+1
// being a child of atHash -- what a reader over par's post-state needs
// layered for base values read on the tree in state at (p.mu held). ok is
// false when one of them is not kept; at >= par.number-1 needs none.
func (p *lagPipeline) aheadLayersLocked(par *lagPipeParent, at uint64, atHash types.Hash) (layers []*state.PostState, ok bool) {
	if par == nil || par.header == nil || at > par.number {
		return nil, false
	}
	if at == par.number {
		return nil, par.hash == atHash
	}
	want, num := par.header.ParentHash, par.number-1
	for num > at {
		var e *lagPipeParent
		for _, o := range p.older {
			if o.hash == want && o.number == num {
				e = o
				break
			}
		}
		if e == nil || e.post == nil || e.header == nil {
			return nil, false
		}
		layers = append(layers, e.post)
		want, num = e.header.ParentHash, num-1
	}
	return layers, want == atHash
}

// Reasons a launch attempt for a published parent gave up.
const (
	lagSkipNotAvail = iota + 1
	lagSkipParent
	lagSkipBusy
)

type lagPipeParent struct {
	hash   types.Hash
	number uint64
	header *block.Header
	post   *state.PostState
	// Under lagPipeline.mu: a child was launched on this snapshot, or the
	// reason the last attempt did not launch one; written: the publishing
	// import wrote (or handed off) the block, the snapshot stays published
	// for a child that comes later.
	launched bool
	skip     int
	written  bool
}

// aheadExec is one block executed ahead of its parent's root and write.
type aheadExec struct {
	blk      *block.Block
	parent   types.Hash
	parentOK bool // the parent's publishing import wrote it (under lagPipeline.mu)
	done     chan struct{}
	started  time.Time
	seq      uint64        // launch order (drainingLocked)
	pre      time.Duration // from launch to the start of the Block-STM run
	// queueWait: from the block's queueing for the lag executor to the
	// start of its ahead run (zero when it was not queued).
	queueWait time.Duration

	// Set by the executing goroutine before done closes.
	ibs    *state.IntraBlockState
	reader state.StateReader
	// pf is the base prefill the run's tree reads answered from; released
	// with the run (release): its lookup sources die with the run's
	// transactions.
	pf   *commitment.BasePrefill
	run  *parallelRun
	tx   kv.Tx
	err  error
	took time.Duration

	once sync.Once
	pipe *lagPipeline
}

// release closes the ahead execution's read transaction (once its goroutine
// finished).
func (a *aheadExec) release() {
	a.once.Do(func() {
		<-a.done
		if a.tx != nil {
			a.tx.Rollback()
			a.pipe.openTxs.Add(-1)
		}
		// Every source reading through the prefill belonged to the run:
		// its workers' and prefetch readers ended with it, and the state's
		// own source with the transaction just closed.
		a.pf.Release()
	})
}

// LagPipelineStats returns the ahead executions started, used by an import
// and discarded, and the read transactions they still hold.
func (bc *BlockChain) LagPipelineStats() (launched, used, discarded uint64, openTxs int64) {
	p := &bc.lagPipe
	return p.launched.Load(), p.used.Load(), p.discarded.Load(), p.openTxs.Load()
}

// LagPipelineSkips is the pipeline's skip breakdown: parents written with no
// child executed ahead because none was available yet, because the block at
// the child height had another parent, or because another ahead execution
// was in flight; and imports of queued blocks with the pipeline disabled.
type LagPipelineSkips struct {
	NotAvailable, ParentMismatch, InFlight, Disabled uint64
}

// LagPipelineSkipStats returns the skip counters (since start).
func (bc *BlockChain) LagPipelineSkipStats() LagPipelineSkips {
	p := &bc.lagPipe
	return LagPipelineSkips{NotAvailable: p.skipNotAvail.Load(), ParentMismatch: p.skipParent.Load(),
		InFlight: p.skipBusy.Load(), Disabled: p.skipOff.Load()}
}

// lagPipeLogFields are the cumulative pipeline counters for the
// "blockimport phases" line.
func (bc *BlockChain) lagPipeLogFields() []interface{} {
	p := &bc.lagPipe
	return []interface{}{
		"aheadAtt", p.launched.Load(), "aheadUsed", p.used.Load(), "aheadDisc", p.discarded.Load(),
		"aheadDiscErr", p.discErr.Load(), "aheadDiscNotApplicable", p.discNotApplicable.Load(),
		"aheadDiscParent", p.discParent.Load(), "aheadDiscHash", p.discHash.Load(),
		"aheadDiscHead", p.discHead.Load(), "aheadDiscFail", p.discFail.Load(),
		"aheadDiscReset", p.discReset.Load(), "aheadDiscForget", p.discForget.Load(),
		"aheadDiscSuperseded", p.discSuperseded.Load(),
		"aheadDiscFull", p.discFull.Load(), "aheadDiscWorkMs", p.discWorkMs.Load(),
		"aheadSkipNotAvail", p.skipNotAvail.Load(), "aheadSkipParent", p.skipParent.Load(),
		"aheadSkipBusy", p.skipBusy.Load(), "aheadSkipOff", p.skipOff.Load(),
		"aheadLate", p.late.Load(), "aheadTakeLaunch", p.takeLaunch.Load(),
		"prepHits", aheadPrepHits.Load(), "prepMisses", aheadPrepMisses.Load(),
		"prepEarlyStarts", aheadPrepEarlyStarts.Load(),
	}
}

// lagPipeUsable: the chain can run ahead executions at all.
func (bc *BlockChain) lagPipeUsable() bool {
	if !lagPipelineOn || !bc.parallelEVM || bc.zkProving || !bc.qmdbEnabled || bc.qmdbRootComputer == nil || bc.ChainDB == nil {
		return false
	}
	_, ok := bc.process.(*StateProcessor)
	return ok
}

// lagPipeHook installs the publication hook on ibs when blk is an available
// block queued for the lag executor -- whichever path imports it: the lag
// executor itself or, with N42_DEFERRED_CHECK_CONCURRENT=1, the block-push
// handler's InsertChain, which takes the chain lock first as often as not
// (S96: the push path imported most blocks, which then never published a
// snapshot, and the executor's later "already stored" pass discarded the
// child's ahead execution as written by another path).
func (bc *BlockChain) lagPipeHook(blk block.IBlock, ibs *state.IntraBlockState) {
	h := blk.Hash()
	if h == (types.Hash{}) || !bc.LagImportQueued(h) {
		return
	}
	if !bc.lagPipeUsable() {
		bc.lagPipe.skipOff.Add(1)
		return
	}
	hdr, ok := blk.Header().(*block.Header)
	if !ok {
		return
	}
	ibs.SetRootPostStateHook(func(post *state.PostState) {
		// The root about to run applies post's dirty set: the tree is
		// then this block's post-state (a child's base prefill waits for
		// it, ahead_base_prefill.go).
		if rc := bc.qmdbRootComputer; rc != nil {
			rc.LabelNextRoot(post, hdr.Number.Uint64(), h, hdr.ParentHash)
		}
		t0 := time.Now()
		lagPublishAtNanos.Store(t0.UnixNano())
		bc.lagPipePublish(hdr, post)
		lagPublishNanos.Store(int64(time.Since(t0)))
	})
}

// lagPublishNanos is the duration of the most recent snapshot publication
// (the child's launch; the snapshot itself is the root's dirty set), spent
// inside the state root.
var lagPublishNanos atomic.Int64

// lagPublishAtNanos is when the most recent publication started (Unix
// nanoseconds): FinishParallel's time to it is the import's part of the
// cycle before the child can start ("finPub" on the phases line).
var lagPublishAtNanos atomic.Int64

// lagPipePublish records the post-state of the block being rooted now and
// starts its queued child ahead. Runs on the importing goroutine, inside
// the root computation.
func (bc *BlockChain) lagPipePublish(hdr *block.Header, post *state.PostState) {
	p := &bc.lagPipe
	h := hdr.Hash()
	if fn := lagPipeBeforePublishForTest; fn != nil {
		fn(h)
	}
	p.mu.Lock()
	p.dropParentLocked()
	p.parent = &lagPipeParent{hash: h, number: hdr.Number.Uint64(), header: hdr, post: post}
	if s := p.spec; s != nil && s.parent != h {
		// An ahead execution on another parent cannot be used any more.
		p.spec = nil
		p.discardLocked(s, &p.discSuperseded)
	}
	p.mu.Unlock()
	if d := lagPipeChildWaitForTest; d > 0 {
		deadline := time.Now().Add(d)
		for bc.lagQueuedChild(h) == nil && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	bc.lagPipeLaunch()
	if fn := lagPipeAfterPublishForTest; fn != nil {
		fn(h)
	}
	if lagPipeAwaitAheadForTest {
		p.mu.Lock()
		a := p.spec
		p.mu.Unlock()
		if a != nil && a.parent == h {
			<-a.done
		}
	}
}

// lagQueuedChild returns a queued child of parent, or nil.
func (bc *BlockChain) lagQueuedChild(parent types.Hash) *block.Block {
	le := &bc.lagExec
	le.mu.Lock()
	defer le.mu.Unlock()
	var best *block.Block
	for _, q := range le.queued {
		if q.blk.ParentHash() != parent {
			continue
		}
		// Siblings: the lowest hash, so every caller picks the same one.
		if best == nil || lessHash(q.blk.Hash(), best.Hash()) {
			best = q.blk
		}
	}
	return best
}

func lessHash(a, b types.Hash) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// lagQueuedAtHeight reports whether a block of height number waits in the
// lag executor.
func (bc *BlockChain) lagQueuedAtHeight(number uint64) bool {
	le := &bc.lagExec
	le.mu.Lock()
	defer le.mu.Unlock()
	for _, q := range le.queued {
		if q.blk.Number64().Uint64() == number {
			return true
		}
	}
	return false
}

// lagPipeLaunch starts the ahead execution of the published parent's queued
// child, if there is a parent, a child and no ahead execution yet. It runs
// at both ends of the race between the child's availability and the
// parent's snapshot: when the snapshot is published (a child that became
// available earlier waited for it in the queue) and when a child is queued
// (its parent already published). Whether the child runs on Block-STM at
// all is decided after its frames are resolved (runAhead): a block sealed
// here holds only frame hashes until then.
func (bc *BlockChain) lagPipeLaunch() {
	if !bc.lagPipeUsable() {
		return
	}
	p := &bc.lagPipe
	p.mu.Lock()
	par := p.parent
	if par == nil || par.launched || (p.spec != nil && p.spec.parent == par.hash) {
		// Nothing published, or this snapshot's child already ran or runs:
		// a written snapshot stays published while its child imports, and
		// that import may already hold the ahead run (taken) or run serially.
		p.mu.Unlock()
		return
	}
	child := bc.lagQueuedChild(par.hash)
	if child == nil {
		if bc.lagQueuedAtHeight(par.number + 1) {
			par.skip = lagSkipParent
		} else {
			par.skip = lagSkipNotAvail
		}
		p.mu.Unlock()
		return
	}
	if p.spec != nil {
		par.skip = lagSkipBusy
		p.mu.Unlock()
		return
	}
	a := p.newAheadLocked(child, par)
	p.spec = a
	p.mu.Unlock()
	go bc.runAhead(a, par)
}

// newAheadLocked creates the ahead execution of child on par's snapshot
// (p.mu held); the caller starts runAhead.
func (p *lagPipeline) newAheadLocked(child *block.Block, par *lagPipeParent) *aheadExec {
	a := &aheadExec{blk: child, parent: par.hash, parentOK: par.written, done: make(chan struct{}), started: time.Now(), pipe: p}
	p.seq++
	a.seq = p.seq
	par.launched = true
	p.launched.Add(1)
	if par.written {
		p.late.Add(1)
	}
	return a
}

// runAhead executes a.blk on par's post-state.
func (bc *BlockChain) runAhead(a *aheadExec, par *lagPipeParent) {
	defer close(a.done)
	defer func() { a.took = time.Since(a.started) }()
	if since := bc.lagQueuedSince(a.blk.Hash()); !since.IsZero() {
		a.queueWait = time.Since(since)
	}
	// A discarded run of the same block may still be filling its
	// transactions' senders.
	a.pipe.awaitDrained(a.blk.Hash(), a)
	if err := bc.ResolveFrames(a.blk); err != nil {
		a.err = err
		return
	}
	// Whether the block runs on Block-STM at all is decided once, by
	// ExecuteParallelAhead (ErrParallelNotApplicable): the check walks
	// every transaction.
	layers := []*state.PostState{par.post}
	if j := bc.lagWritePending(par.header.ParentHash); j != nil {
		// The grandparent's write is in flight (lag_writer.go): the store
		// does not hold it yet, its post-state does. Checked before the
		// transaction opens: a write that is not pending now is durable in it.
		if j.post != nil {
			layers = append(layers, j.post)
		} else {
			<-j.done
		}
	}
	// The block's preparation, and with it the base prefill its tree reads
	// answer first (ahead_base_prefill.go), is claimed before the readers
	// are built -- before the run's transaction opens: the prefill's own
	// transactions may wait for read permits -- and the run adopts the rest.
	claim := &aheadClaim{}
	claim.prep, claim.wait = bc.claimAheadPrep(a.blk.Hash())
	if ab := claim.prep.prefill(); ab != nil && ab.pf != nil {
		layers = bc.lagPipe.aheadBaseLayers(ab, par, layers)
		a.pf = ab.pf
	}
	if fn := lagPipeAheadClaimedForTest; fn != nil {
		fn(bc, a.blk)
	}
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if err != nil {
		a.err = err
		return
	}
	a.tx = tx
	a.pipe.openTxs.Add(1)
	if fn := lagPipeAheadStartedForTest; fn != nil {
		fn(a.blk)
	}
	var base state.StateReader = state.NewPlainStateReader(tx)
	if mode := commitment.QMDBStateReadMode(); mode != commitment.QMDBReadOff {
		src := commitment.NewLookupSourceRefreshing(bc.qmdbRootComputer, tx, bc.ChainDB)
		if ab := claim.prep.prefill(); ab != nil && ab.pf != nil {
			src.SetPrefill(ab.pf)
		}
		base = commitment.NewQMDBStateReader(src, base, mode)
	}
	prefetch := state.NewAccountPrefetch(state.LayerPostStates(layers, base))
	ibs := state.New(prefetch)
	ibs.SetAccountPrefetch(prefetch)
	ibs.SetDetachedPendingCredits(true)
	ibs.SetPostStateLayers(layers)
	hdr := a.blk.Header().(*block.Header)
	getHeader := func(hash types.Hash, number uint64) *block.Header {
		if hash == par.hash {
			return par.header
		}
		return bc.readHeaderOrPending(tx, hash, number)
	}
	a.pre = time.Since(a.started)
	run, err := bc.process.(*StateProcessor).executeParallelAhead(a.blk, ibs, GetHashFn(hdr, getHeader), claim)
	if err != nil {
		a.err = err
		return
	}
	a.ibs, a.reader, a.run = ibs, prefetch, run
}

// aheadBaseLayers returns the run's layers for its base prefill ab (claimed,
// finished): read on the tree at par's height or its parent's, layers as
// they are; read deeper, par's post-state over the kept post-states of
// every block down to ab.at+1 (P15b) -- they also answer every key of a
// grandparent write still pending. When one is not kept the prefill is
// dropped (ab.pf = nil): the run reads through the tree per lookup.
func (p *lagPipeline) aheadBaseLayers(ab *aheadBase, par *lagPipeParent, layers []*state.PostState) []*state.PostState {
	if ab.at+1 >= par.number {
		if (ab.at == par.number && ab.atHash == par.hash) || (ab.at+1 == par.number && ab.atHash == par.header.ParentHash) {
			return layers
		}
		ab.pf, ab.skip = nil, "tree off the chain at the claim"
		return layers
	}
	if fn := lagPipeAheadLayersForTest; fn != nil {
		fn(p)
	}
	p.mu.Lock()
	older, ok := p.aheadLayersLocked(par, ab.at, ab.atHash)
	p.mu.Unlock()
	if !ok {
		ab.pf, ab.skip = nil, "an ancestor's post-state is not kept"
		return layers
	}
	return append([]*state.PostState{par.post}, older...)
}

// lagPipeAheadLayersForTest runs before a deep prefill's layers are looked
// up (tests: forget a kept post-state).
var lagPipeAheadLayersForTest func(p *lagPipeline)

// lagPipeTake hands blk's ahead execution to its import, or nil when there
// is none or it cannot be used (then it is discarded and the import runs
// the serial path). It waits for the execution to finish. The caller owns
// the result and releases it after the write.
func (bc *BlockChain) lagPipeTake(blk block.IBlock, number uint64) *aheadExec {
	p := &bc.lagPipe
	p.mu.Lock()
	a := p.spec
	if a == nil {
		// Nothing ran ahead: the child came after its parent's import. On
		// the parent's still-published snapshot it runs the ahead path now.
		if par := p.parent; par != nil && par.hash == blk.ParentHash() && par.written && !par.launched && bc.lagPipeUsable() {
			if child, ok := blk.(*block.Block); ok && bc.LagImportQueued(child.Hash()) {
				a = p.newAheadLocked(child, par)
				p.takeLaunch.Add(1)
				go bc.runAhead(a, par)
			}
		}
	}
	if a == nil || a.blk.Hash() != blk.Hash() {
		if par := p.parent; par != nil && par.hash == blk.ParentHash() {
			// blk runs the serial path: nothing may launch it on this
			// snapshot from now on.
			p.dropParentLocked()
		}
		p.mu.Unlock()
		// The serial path executes blk next: a discarded run of it must not
		// still be filling its transactions' senders, nor a preparation of
		// it reading them.
		p.awaitDrained(blk.Hash(), nil)
		bc.dropAheadPrep(blk.Hash())
		return nil
	}
	if p.spec == a {
		p.spec = nil
	}
	parentOK := a.parentOK
	p.mu.Unlock()
	<-a.done
	reason := ""
	var cnt *atomic.Uint64
	switch {
	case a.err != nil:
		reason = a.err.Error()
		cnt = &p.discErr
		if errors.Is(a.err, ErrParallelNotApplicable) {
			cnt = &p.discNotApplicable
		}
	case !parentOK:
		reason = "parent not written by its publishing import"
		cnt = &p.discParent
	case a.parent != blk.ParentHash():
		reason = "different parent"
		cnt = &p.discHash
	case !bc.lagAppliedIs(a.parent, number-1):
		reason = "applied head is not the parent"
		cnt = &p.discHead
	}
	if reason != "" {
		log.Debug("lag pipeline: ahead execution discarded", "number", number, "hash", blk.Hash().Hex()[:12], "reason", reason)
		p.discarded.Add(1)
		cnt.Add(1)
		p.addDiscWork(a)
		a.release()
		p.awaitDrained(blk.Hash(), nil)
		bc.dropAheadPrep(blk.Hash())
		return nil
	}
	p.used.Add(1)
	return a
}

// discardLocked drops an ahead execution (p.mu held): its transaction is
// closed once its goroutine finishes.
func (p *lagPipeline) discardLocked(a *aheadExec, reason *atomic.Uint64) {
	p.discarded.Add(1)
	reason.Add(1)
	p.draining = append(p.draining, a)
	go func() {
		a.release()
		p.addDiscWork(a)
	}()
}

// addDiscWork accumulates the wall time of a discarded run that carried
// transactions (diagnostics); it waits for the run to finish.
func (p *lagPipeline) addDiscWork(a *aheadExec) {
	<-a.done
	if len(a.blk.Transactions()) > 0 {
		p.discFull.Add(1)
		p.discWorkMs.Add(uint64(a.took.Milliseconds()))
	}
}

// drainingLocked returns a discarded execution of hash still running, and
// forgets the finished ones (p.mu held).
// With self set (an ahead execution about to start), only executions
// launched before self count: a discarded execution that has not started
// yet would otherwise wait for itself, or two of them for each other (a
// deadlock the race detector's slower goroutine starts exposed).
func (p *lagPipeline) drainingLocked(hash types.Hash, self *aheadExec) *aheadExec {
	var running *aheadExec
	keep := p.draining[:0]
	for _, a := range p.draining {
		select {
		case <-a.done:
			continue
		default:
		}
		keep = append(keep, a)
		if a.blk.Hash() == hash && (self == nil || a.seq < self.seq) {
			running = a
		}
	}
	for i := len(keep); i < len(p.draining); i++ {
		p.draining[i] = nil
	}
	p.draining = keep
	return running
}

// awaitDrained waits until no discarded execution of hash runs (with self,
// none launched before self).
func (p *lagPipeline) awaitDrained(hash types.Hash, self *aheadExec) {
	for {
		p.mu.Lock()
		a := p.drainingLocked(hash, self)
		p.mu.Unlock()
		if a == nil {
			return
		}
		<-a.done
	}
}

// lagPipeSettle settles the pipeline after an import of h that ran its
// execution (insertChain, under the chain lock, on every path): written,
// its child's ahead execution may be used, and the snapshot stays published
// for a child not launched yet; failed, the snapshot it published and that
// execution are dropped. A parent dropped with no child launched on its
// snapshot counts the reason the last attempt gave up (dropParentLocked).
func (bc *BlockChain) lagPipeSettle(h types.Hash, written bool) {
	p := &bc.lagPipe
	p.mu.Lock()
	defer p.mu.Unlock()
	if par := p.parent; par != nil && par.hash == h {
		if written {
			par.written = true
		} else {
			p.dropParentLocked()
		}
	}
	if a := p.spec; a != nil && a.parent == h {
		if written {
			a.parentOK = true
		} else {
			p.spec = nil
			p.discardLocked(a, &p.discFail)
		}
	}
}

// lagPipeForget drops what the pipeline holds for hash (forgotten: invalid
// or a losing fork).
func (bc *BlockChain) lagPipeForget(hash types.Hash) {
	p := &bc.lagPipe
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.parent != nil && p.parent.hash == hash {
		p.dropParentLocked()
	}
	p.forgetOlderLocked(hash)
	if a := p.spec; a != nil && (a.parent == hash || a.blk.Hash() == hash) {
		p.spec = nil
		p.discardLocked(a, &p.discForget)
		if par := p.parent; par != nil && par.hash == a.parent && a.blk.Hash() == hash {
			// The orphan's sibling may still run ahead on this snapshot.
			par.launched = false
		}
	}
}

// lagPipeReset drops the published snapshot and the ahead execution (a
// failed write reloaded the tree from the store: no snapshot above it
// holds).
func (bc *BlockChain) lagPipeReset() {
	p := &bc.lagPipe
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropParentLocked()
	p.older = nil
	if a := p.spec; a != nil {
		p.spec = nil
		p.discardLocked(a, &p.discReset)
	}
}

// lagPipeInjectedFailure is the test hook of lagPipeFailWriteForTest.
func lagPipeInjectedFailure(h types.Hash) error {
	if want := lagPipeFailWriteForTest.Load(); want != nil && *want == h && lagPipeFailWriteForTest.CompareAndSwap(want, nil) {
		return errLagPipeInjectedWrite
	}
	return nil
}
