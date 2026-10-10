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
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// Stage pipelining in the lag executor (docs/QS_MULTI_MILLION_DESIGN.md
// P2a2): block N+1 executes while block N's state root and write run.
//
// The lag executor imports blocks one at a time in parent order, and an
// import is exec, then root, then write. When it imports N, the moment N's
// dirty set is final -- rewards applied, just before the root computer runs
// (IntraBlockState.SetBeforeRootHook) -- N's post-state is captured
// (state.CapturePostState, an immutable snapshot) and, if N's child is
// queued, the child's Block-STM execution starts on its own goroutine
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
	// armed: the block whose import, run by the lag executor, publishes
	// its post-state.
	armed types.Hash
	// parent: the published snapshot of the block being rooted and
	// written now (nil once it is written or failed).
	parent *lagPipeParent
	// spec: the one ahead execution.
	spec *aheadExec

	launched  atomic.Uint64
	used      atomic.Uint64
	discarded atomic.Uint64
	openTxs   atomic.Int64
}

type lagPipeParent struct {
	hash   types.Hash
	header *block.Header
	post   *state.PostState
}

// aheadExec is one block executed ahead of its parent's root and write.
type aheadExec struct {
	blk      *block.Block
	parent   types.Hash
	parentOK bool // the parent's publishing import wrote it (under lagPipeline.mu)
	done     chan struct{}
	started  time.Time

	// Set by the executing goroutine before done closes.
	ibs    *state.IntraBlockState
	reader state.StateReader
	run    *parallelRun
	tx     kv.Tx
	err    error
	took   time.Duration

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
	})
}

// LagPipelineStats returns the ahead executions started, used by an import
// and discarded, and the read transactions they still hold.
func (bc *BlockChain) LagPipelineStats() (launched, used, discarded uint64, openTxs int64) {
	p := &bc.lagPipe
	return p.launched.Load(), p.used.Load(), p.discarded.Load(), p.openTxs.Load()
}

// lagPipeUsable: the chain can run ahead executions at all.
func (bc *BlockChain) lagPipeUsable() bool {
	if !lagPipelineOn || !bc.parallelEVM || bc.zkProving || !bc.qmdbEnabled || bc.qmdbRootComputer == nil || bc.ChainDB == nil {
		return false
	}
	_, ok := bc.process.(*StateProcessor)
	return ok
}

// lagPipeArm marks b's coming import (by the lag executor) as the one that
// publishes its post-state; the zero hash disarms.
func (bc *BlockChain) lagPipeArm(h types.Hash) {
	p := &bc.lagPipe
	p.mu.Lock()
	p.armed = h
	p.mu.Unlock()
}

// lagPipeHook installs the publication hook on ibs when blk's import is the
// armed one.
func (bc *BlockChain) lagPipeHook(blk block.IBlock, ibs *state.IntraBlockState) {
	if !bc.lagPipeUsable() {
		return
	}
	p := &bc.lagPipe
	p.mu.Lock()
	armed := p.armed == blk.Hash() && blk.Hash() != (types.Hash{})
	p.mu.Unlock()
	if !armed {
		return
	}
	hdr, ok := blk.Header().(*block.Header)
	if !ok {
		return
	}
	ibs.SetBeforeRootHook(func() { bc.lagPipePublish(hdr, state.CapturePostState(ibs)) })
}

// lagPipePublish records the post-state of the block being rooted now and
// starts its queued child ahead. Runs on the importing goroutine, inside
// the root computation.
func (bc *BlockChain) lagPipePublish(hdr *block.Header, post *state.PostState) {
	p := &bc.lagPipe
	h := hdr.Hash()
	p.mu.Lock()
	p.parent = &lagPipeParent{hash: h, header: hdr, post: post}
	if s := p.spec; s != nil && s.parent != h {
		// An ahead execution on another parent cannot be used any more.
		p.spec = nil
		p.discardLocked(s)
	}
	p.mu.Unlock()
	if d := lagPipeChildWaitForTest; d > 0 {
		deadline := time.Now().Add(d)
		for bc.lagQueuedChild(h) == nil && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	}
	bc.lagPipeLaunch()
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

// lagPipeLaunch starts the ahead execution of the published parent's queued
// child, if there is a parent, a child and no ahead execution yet.
func (bc *BlockChain) lagPipeLaunch() {
	if !bc.lagPipeUsable() {
		return
	}
	p := &bc.lagPipe
	p.mu.Lock()
	par := p.parent
	if par == nil || p.spec != nil {
		p.mu.Unlock()
		return
	}
	child := bc.lagQueuedChild(par.hash)
	if child == nil || !bc.process.(*StateProcessor).ParallelApplicable(child) {
		p.mu.Unlock()
		return
	}
	a := &aheadExec{blk: child, parent: par.hash, done: make(chan struct{}), started: time.Now(), pipe: p}
	p.spec = a
	p.mu.Unlock()
	p.launched.Add(1)
	go bc.runAhead(a, par)
}

// runAhead executes a.blk on par's post-state.
func (bc *BlockChain) runAhead(a *aheadExec, par *lagPipeParent) {
	defer close(a.done)
	defer func() { a.took = time.Since(a.started) }()
	if err := bc.ResolveFrames(a.blk); err != nil {
		a.err = err
		return
	}
	tx, err := bc.ChainDB.BeginRo(bc.ctx)
	if err != nil {
		a.err = err
		return
	}
	a.tx = tx
	a.pipe.openTxs.Add(1)
	var base state.StateReader = state.NewPlainStateReader(tx)
	if mode := commitment.QMDBStateReadMode(); mode != commitment.QMDBReadOff {
		base = commitment.NewQMDBStateReader(commitment.NewLookupSourceRefreshing(bc.qmdbRootComputer, tx, bc.ChainDB), base, mode)
	}
	layers := []*state.PostState{par.post}
	prefetch := state.NewAccountPrefetch(state.LayerPostStates(layers, base))
	ibs := state.New(prefetch)
	ibs.SetAccountPrefetch(prefetch)
	ibs.SetPostStateLayers(layers)
	hdr := a.blk.Header().(*block.Header)
	getHeader := func(hash types.Hash, number uint64) *block.Header {
		if hash == par.hash {
			return par.header
		}
		return rawdb.ReadHeader(tx, hash, number)
	}
	run, err := bc.process.(*StateProcessor).ExecuteParallelAhead(a.blk, ibs, GetHashFn(hdr, getHeader))
	if err != nil {
		a.err = err
		return
	}
	a.ibs, a.reader, a.run = ibs, prefetch, run
}

// lagPipeTake hands blk's ahead execution to its import, or nil when there
// is none or it cannot be used (then it is discarded and the import runs
// the serial path). It waits for the execution to finish. The caller owns
// the result and releases it after the write.
func (bc *BlockChain) lagPipeTake(blk block.IBlock, number uint64) *aheadExec {
	p := &bc.lagPipe
	p.mu.Lock()
	a := p.spec
	if a == nil || a.blk.Hash() != blk.Hash() {
		p.mu.Unlock()
		return nil
	}
	p.spec = nil
	parentOK := a.parentOK
	p.mu.Unlock()
	<-a.done
	reason := ""
	switch {
	case a.err != nil:
		reason = a.err.Error()
	case !parentOK:
		reason = "parent not written by its publishing import"
	case a.parent != blk.ParentHash():
		reason = "different parent"
	case !bc.AppliedHeadIsExactly(a.parent, number-1):
		reason = "applied head is not the parent"
	}
	if reason != "" {
		log.Debug("lag pipeline: ahead execution discarded", "number", number, "hash", blk.Hash().Hex()[:12], "reason", reason)
		p.discarded.Add(1)
		a.release()
		return nil
	}
	p.used.Add(1)
	return a
}

// discardLocked drops an ahead execution (p.mu held): its transaction is
// closed once its goroutine finishes.
func (p *lagPipeline) discardLocked(a *aheadExec) {
	p.discarded.Add(1)
	go a.release()
}

// lagPipeAfterImport settles the pipeline after the lag executor's import
// of b: written, its child's ahead execution may be used; failed, the
// snapshot it published and that execution are dropped.
func (bc *BlockChain) lagPipeAfterImport(b *block.Block, written bool) {
	p := &bc.lagPipe
	h := b.Hash()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.armed = types.Hash{}
	if p.parent != nil && p.parent.hash == h {
		p.parent = nil
	}
	if a := p.spec; a != nil && a.parent == h {
		if written {
			a.parentOK = true
		} else {
			p.spec = nil
			p.discardLocked(a)
		}
	}
}

// lagPipeForget drops what the pipeline holds for hash (forgotten: invalid,
// a losing fork, or written by another path).
func (bc *BlockChain) lagPipeForget(hash types.Hash) {
	p := &bc.lagPipe
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.parent != nil && p.parent.hash == hash {
		p.parent = nil
	}
	if a := p.spec; a != nil && (a.parent == hash || a.blk.Hash() == hash) {
		p.spec = nil
		p.discardLocked(a)
	}
}

// lagPipeInjectedFailure is the test hook of lagPipeFailWriteForTest.
func lagPipeInjectedFailure(h types.Hash) error {
	if want := lagPipeFailWriteForTest.Load(); want != nil && *want == h && lagPipeFailWriteForTest.CompareAndSwap(want, nil) {
		return errLagPipeInjectedWrite
	}
	return nil
}
