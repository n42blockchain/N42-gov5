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
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// The lag executor's ordered writer (docs/QS_MULTI_MILLION_DESIGN.md P4b).
//
// An import is execution, state root, write. The write -- the block's MDBX
// transaction: receipts, block, CommitBlock, changesets, the QMDB flush,
// undo, applied marker, executed result, commit -- is ~85 ms at 163k
// transfers, and the next block's root used to wait for all of it. With
// the ordered writer, an import by the lag executor applies the block in
// memory (the QMDB tree holds it, its post-state is published to the
// pipeline) and hands the durable write to a goroutine; the executor goes
// on to the child, whose root overlaps the write.
//
// Rules:
//
//   - Order: one write in flight. The hand-off of block N+1 waits for N's
//     write (back-pressure), so writes land in block order and the memory
//     a pending write holds is bounded by one block.
//   - Memory first: what the next block's root and the ahead execution read
//     -- the QMDB tree and the published post-state -- is updated on the
//     import goroutine as before. The QMDB rows of the block are captured
//     at the hand-off (FlushTo into a buffer, the tree exactly at the
//     block), so the child's appends can never leak into them. The flush
//     cursor is adopted and the flushed window evicted from RAM only once
//     the write is durable, on the import goroutine (joinLagWrite), never
//     under a root that could fault the window back from disk.
//   - Imported = durable: the lag executor reports a block to consensus
//     (the imported hook, LagStats) only after its write committed, so the
//     leader gate (LagReady), the commit path and every reader of the
//     applied marker keep seeing durable state only. Readers that would
//     otherwise not find the pending block at all -- HasBlock, GetBlock,
//     GetHeader -- see it, so the child's import validates against it.
//   - Fast path: only a lag import of the pending block's child at deferred
//     depth >= 2 runs while the write is in flight; it skips the
//     applied-marker alignment (the tree is exactly the parent, checked by
//     root). Every other path that reads or moves the applied state --
//     another import, a leader write, a branch switch, a peel -- first
//     joins the pending write.
//   - Restart: the block's rows are one MDBX transaction, so a crash
//     leaves the store at the last durable block; a block applied only in
//     memory is executed again by the lag executor.
//   - Failure: a failed write leaves memory ahead of the store, so the tree
//     is reloaded from the store, the pipeline is dropped and the block is
//     queued again.
//
// N42_LAG_WRITE_ASYNC=0 writes synchronously, as before.

// lagWriteAsync is N42_LAG_WRITE_ASYNC (default on; "0", "false" or "off"
// disables).
var lagWriteAsync = parseLagPipeline(os.Getenv("N42_LAG_WRITE_ASYNC"))

// SetLagWriteAsyncForTest switches the ordered writer and returns the
// restore.
func SetLagWriteAsyncForTest(on bool) func() {
	prev := lagWriteAsync
	lagWriteAsync = on
	return func() { lagWriteAsync = prev }
}

// lagWriteBeforeCommitForTest, when set, runs on the writer goroutine before
// a block's transaction; an error aborts the write (tests: a crash before
// the write lands, or a failed write).
var lagWriteBeforeCommitForTest func(number uint64) error

// lagWriteJoinForTest, when set, runs when something starts waiting for the
// write of block number to finish -- the next block's hand-off, or any other
// join (benchmarks: holding each write until then makes the writes
// back-to-back).
var lagWriteJoinForTest func(number uint64)

// lagWriter is the ordered writer's state on the chain.
type lagWriter struct {
	// inflight: the write handed off and not yet joined (under bc.lock).
	inflight *lagWriteJob
	// visible: the block readers see as stored until its write is durable.
	visible atomic.Pointer[lagWriteJob]
	// early: the executed result of the block being imported, published
	// before its hand-off (N42_LAG_RESULT_EARLY, lag_gate_applied.go).
	early atomic.Pointer[lagEarlyResult]

	notifyOnce sync.Once
	notify     chan *lagWriteJob

	handed   atomic.Uint64
	failed   atomic.Uint64
	overlap  atomic.Uint64 // imports that ran while their parent's write was in flight
	lastWait atomic.Int64  // the last hand-off's wait for the previous write
	// lastHandoff: the last hand-off's serial work on the importing
	// goroutine -- its whole span less the wait for the previous write to
	// finish (handoffUs on the "blockimport phases" line).
	lastHandoff atomic.Int64
}

// lagWriteJob is one block's durable write.
type lagWriteJob struct {
	blk      *block.Block
	hdr      *block.Header
	hash     types.Hash
	number   uint64
	receipts []*block.Receipt
	ibs      *state.IntraBlockState
	nopay    map[types.Address]*uint256.Int
	future   *receiptsResultFuture
	flush    qmdbFlushRows
	undo     *qmdb.BlockUndo
	root     types.Hash // the live tree's root right after the block
	// execRoot: the block's computed state root (ibs.LastIntermediateRoot,
	// what the write stores as the executed result's root), read on the
	// import goroutine at the hand-off; execRootOK false when there is none.
	execRoot   types.Hash
	execRootOK bool
	ahead      *aheadExec // its read transaction is released once written
	// post: the block's published post-state (nil when it published none),
	// layered under an ahead run whose reads would otherwise reach the store
	// without this block.
	post *state.PostState
	// prep: the block's CommitBlock and changeset rows, built on their own
	// goroutine from before the hand-off's wait for the previous write
	// (nil: CommitBlock runs inside the write, N42_LAG_WRITE_PREPARE=0).
	prep *stateWritePrep
	// frames: the block's frames being written to the frame store
	// (rawdb.FrameStore), from the hand-off on; nil without one.
	frames *framesWrite
	// logs: the block's Log rows and log-index input, prepared from the
	// hand-off on (nil: the write walks the receipts itself).
	logs *logsPrep
	// handAt: when the hand-off began (the early preparation's lead is
	// measured to it).
	handAt time.Time
	// capture: the FlushTo of flush running off the importing goroutine
	// (N42_LAG_HANDOFF_LEAN), nil when flush was captured in the hand-off.
	// gen: the tree's mutation count at the hand-off (lagWriteFastReady).
	capture *commitment.FlushCapture
	gen     uint64

	done chan struct{}
	err  error
}

// lagWritePrepare is N42_LAG_WRITE_PREPARE (default on): the ordered
// writer's CommitBlock and changeset encoding run before the block's write
// transaction, overlapping the previous block's write
// (docs/QS_MULTI_MILLION_DESIGN.md P11).
var lagWritePrepare = parseLagPipeline(os.Getenv("N42_LAG_WRITE_PREPARE"))

// SetLagWritePrepareForTest switches the prepared state write and returns
// the restore.
func SetLagWritePrepareForTest(on bool) func() {
	prev := lagWritePrepare
	lagWritePrepare = on
	return func() { lagWritePrepare = prev }
}

// stateWritePrep is one block's state write being prepared.
type stateWritePrep struct {
	done chan struct{}
	p    *state.PreparedStateWrite
	err  error
	took time.Duration
	// start: when the preparation started; early: it was started inside the
	// block's state root (N42_LAG_WRITE_EARLY_PREP), not at the hand-off.
	start time.Time
	early bool
}

// startStateWritePrep runs state.PrepareStateWrite on its own goroutine. It
// owns ibs from here on, as the write would.
func (bc *BlockChain) startStateWritePrep(ibs *state.IntraBlockState, number uint64) *stateWritePrep {
	return bc.startStateWritePrepShards(ibs, number, 0)
}

// startStateWritePrepShards is startStateWritePrep with shards address
// shards (0: the default count; the prepared write is the same for any).
func (bc *BlockChain) startStateWritePrepShards(ibs *state.IntraBlockState, number uint64, shards int) *stateWritePrep {
	sp := &stateWritePrep{done: make(chan struct{}), start: time.Now()}
	rules := bc.chainConfig.Rules(number)
	go func() {
		t0 := time.Now()
		if shards > 0 {
			sp.p, sp.err = state.PrepareStateWriteShards(ibs, rules, number, shards)
		} else {
			sp.p, sp.err = state.PrepareStateWrite(ibs, rules, number)
		}
		sp.took = time.Since(t0)
		close(sp.done)
	}()
	return sp
}

// lagWriteEarlyPrep is N42_LAG_WRITE_EARLY_PREP (default off, r67a). With
// it on, a lag import whose write will be handed to the ordered writer
// starts the prepared state write (startStateWritePrep) inside its state
// root, right before the root computer runs (IntraBlockState
// SetRootComputeHook), instead of at the hand-off after the root. The
// preparation then overlaps the root computer (~14 ms at 163k transfers)
// as well as the hand-off's wait for the previous write, and the write's
// prepWait -- the head of the writer chain -- shrinks by as much.
//
// What is written does not change: the preparation is the same function on
// the same, final state (the dirty set is complete when the hook runs; the
// root computer reads its own copies of it, never the state objects), only
// started earlier. Preconditions, checked when the hook is armed:
//
//   - The block executed ahead (lag pipeline): its reader is the ahead
//     run's, whose read transaction stays open until the block is written
//     or the import gives up. The serial path's reader is the import's own
//     read transaction, which closes when execution returns, and its
//     cursors must not be shared with another goroutine.
//   - The write will be handed off (lagWriteUsable): no other writer will
//     run CommitBlock on the state. An import that arms the hook and then
//     does not hand the write off (execution or validation failed, an
//     injected failure) waits for the preparation before it lets the ahead
//     run's read transaction go.
//
// Ordering for readers is the ordered writer's (see the file comment):
// writes land in block order, one in flight; head pointers, the applied
// marker, receipts and the frames are written in the block's one MDBX
// transaction exactly as before, and the block is reported imported only
// once that transaction committed.
var lagWriteEarlyPrep = os.Getenv("N42_LAG_WRITE_EARLY_PREP") == "1"

// SetLagWriteEarlyPrepForTest switches the early preparation and returns
// the restore.
func SetLagWriteEarlyPrepForTest(on bool) func() {
	prev := lagWriteEarlyPrep
	lagWriteEarlyPrep = on
	return func() { lagWriteEarlyPrep = prev }
}

// lagWriteEarlyPrepDelay is N42_LAG_WRITE_EARLY_PREP_DELAY (default off,
// r68a; only with N42_LAG_WRITE_EARLY_PREP). The early preparation is
// armed in the root as before, but started only once the child's ahead
// execution on this block's post-state has finished its setup and begun
// executing (its launch: lagEarlyPrepChildStarted), or at the hand-off,
// whichever comes first -- instead of at the root computer's start, where
// it competed for the CPU with the root and with the child's setup. The
// preparation is the same function on the same final state (the state is
// the writer's from the root's compute hook on); only its start moves.
//
// N42_LAG_WRITE_EARLY_PREP_SHARDS=<n> (default 0: the preparation's
// default count) runs an early preparation at n address shards; the
// prepared write is the same for any count.
var (
	lagWriteEarlyPrepDelay  = os.Getenv("N42_LAG_WRITE_EARLY_PREP_DELAY") == "1"
	lagWriteEarlyPrepShards = parseEarlyPrepShards(os.Getenv("N42_LAG_WRITE_EARLY_PREP_SHARDS"))
)

func parseEarlyPrepShards(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0
	}
	if n > 256 {
		return 256
	}
	return n
}

// SetLagWriteEarlyPrepDelayForTest switches the delayed early preparation
// (and its shard count, 0: default) and returns the restore.
func SetLagWriteEarlyPrepDelayForTest(on bool, shards int) func() {
	prev, prevS := lagWriteEarlyPrepDelay, lagWriteEarlyPrepShards
	lagWriteEarlyPrepDelay, lagWriteEarlyPrepShards = on, shards
	return func() { lagWriteEarlyPrepDelay, lagWriteEarlyPrepShards = prev, prevS }
}

// earlyPrepSlot is one import's early preparation: armed by the root's
// compute hook, started once (start), or settled without a start.
type earlyPrepSlot struct {
	bc     *BlockChain
	ibs    *state.IntraBlockState
	number uint64
	armed  atomic.Bool
	once   sync.Once
	sp     *stateWritePrep // set inside once
}

// start starts the preparation unless it started or was settled already,
// and reports whether this call started it. Only once armed: the state is
// the writer's from the compute hook on.
func (s *earlyPrepSlot) start() bool {
	if !s.armed.Load() {
		return false
	}
	started := false
	s.once.Do(func() {
		sp := s.bc.startStateWritePrepShards(s.ibs, s.number, lagWriteEarlyPrepShards)
		sp.early = true
		s.sp = sp
		lagEarlyPreps.Add(1)
		started = true
	})
	return started
}

// forHandoff returns the preparation for the hand-off, starting it now if
// it is armed and not started yet (nil when never armed).
func (s *earlyPrepSlot) forHandoff() *stateWritePrep {
	s.start()
	return s.settle()
}

// settle stops any later start and returns the started preparation (nil:
// none started).
func (s *earlyPrepSlot) settle() *stateWritePrep {
	s.once.Do(func() {})
	return s.sp
}

// lagEarlyDelay pairs a delayed early preparation with its child's launch,
// whichever of the two comes first (one block at a time).
type lagEarlyDelay struct {
	mu           sync.Mutex
	hash         types.Hash
	slot         *earlyPrepSlot
	childStarted bool
}

// lagEarlyPrepDelayed counts the early preparations started by a child's
// launch (since start).
var lagEarlyPrepDelayed atomic.Uint64

// lagEarlyPrepChildStarted is called by the ahead execution of a child of
// parent once its setup is done and it starts executing.
func (bc *BlockChain) lagEarlyPrepChildStarted(parent types.Hash) {
	if !lagWriteEarlyPrepDelay {
		return
	}
	d := &bc.lagEarlyDelay
	d.mu.Lock()
	if d.hash == parent && d.slot != nil {
		s := d.slot
		d.slot = nil
		d.mu.Unlock()
		if s.start() {
			lagEarlyPrepDelayed.Add(1)
		}
		return
	}
	d.hash, d.slot, d.childStarted = parent, nil, true
	d.mu.Unlock()
}

// lagWriteEarlyPrepArm arms the early preparation of blk's state write on
// ibs (see lagWriteEarlyPrep) and returns its slot, or nil when it is not
// armed. Called on the importing goroutine before execution, with the root
// computer already set.
func (bc *BlockChain) lagWriteEarlyPrepArm(blk *block.Block, ibs *state.IntraBlockState, lag *lagInsert, ahead *aheadExec) *earlyPrepSlot {
	if !lagWriteEarlyPrep || !lagWritePrepare || lag == nil || ahead == nil || bc.chainConfig == nil ||
		!bc.lagWriteUsable(blk, ibs) {
		return nil
	}
	slot := &earlyPrepSlot{bc: bc, ibs: ibs, number: blk.Number64().Uint64()}
	delay := lagWriteEarlyPrepDelay
	hash := blk.Hash()
	ibs.SetRootComputeHook(func() {
		slot.armed.Store(true)
		if !delay {
			slot.start()
			return
		}
		d := &bc.lagEarlyDelay
		d.mu.Lock()
		if d.hash == hash && d.childStarted {
			// The child launched first.
			d.childStarted = false
			d.mu.Unlock()
			if slot.start() {
				lagEarlyPrepDelayed.Add(1)
			}
			return
		}
		d.hash, d.slot, d.childStarted = hash, slot, false
		d.mu.Unlock()
	})
	return slot
}

// lagEarlyPreps counts the preparations started inside a root (since
// start).
var lagEarlyPreps atomic.Uint64

// wait returns the prepared write once it is ready.
func (sp *stateWritePrep) wait() (*state.PreparedStateWrite, error) {
	<-sp.done
	return sp.p, sp.err
}

// framesWrite is one block's frames going to the frame store in their own
// durable transaction, which the block's write waits for before it commits.
type framesWrite struct {
	done chan struct{}
	err  error
	took time.Duration
}

// startFramesWrite writes blk's frames to the frame store on its own
// goroutine, or returns nil (no frame store, not a frame body).
func startFramesWrite(blk *block.Block) *framesWrite {
	if rawdb.FrameStore() == nil {
		return nil
	}
	body, ok := blk.Body().(*block.Body)
	if !ok || !body.IsFrameBody() || len(body.FrameData) == 0 {
		return nil
	}
	fw := &framesWrite{done: make(chan struct{})}
	hashes, raws := body.FrameHashes, body.FrameData
	go func() {
		t0 := time.Now()
		fw.err = rawdb.WriteFramesToStore(context.Background(), hashes, raws)
		fw.took = time.Since(t0)
		close(fw.done)
	}()
	return fw
}

func (fw *framesWrite) wait() error {
	<-fw.done
	return fw.err
}

// logsPrep is one block's rawdb.PrepareLogs on its own goroutine.
type logsPrep struct {
	done chan struct{}
	p    *rawdb.PreparedLogs
	err  error
}

func startLogsPrep(receipts []*block.Receipt) *logsPrep {
	lp := &logsPrep{done: make(chan struct{})}
	go func() {
		lp.p, lp.err = rawdb.PrepareLogs(receipts)
		close(lp.done)
	}()
	return lp
}

func (lp *logsPrep) wait() (*rawdb.PreparedLogs, error) {
	<-lp.done
	return lp.p, lp.err
}

// qmdbFlushRows captures a FlushTo for replay into a later transaction. The
// tree hands every row fresh slices, so they are kept, not copied.
type qmdbFlushRows struct {
	rows []qmdbFlushRow
}

type qmdbFlushRow struct {
	table      string
	key, value []byte
	del        bool
}

func (f *qmdbFlushRows) Put(table string, key, value []byte) error {
	f.rows = append(f.rows, qmdbFlushRow{table: table, key: key, value: value})
	return nil
}

func (f *qmdbFlushRows) Delete(table string, key []byte) error {
	f.rows = append(f.rows, qmdbFlushRow{table: table, key: key, del: true})
	return nil
}

// replay writes the captured rows into tx, in capture order.
func (f *qmdbFlushRows) replay(tx kv.RwTx) error {
	for _, r := range f.rows {
		var err error
		if r.del {
			err = tx.Delete(r.table, r.key)
		} else {
			err = tx.Put(r.table, r.key, r.value)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// lagInsert is a lag executor import's mode: whether its write may be
// handed to the ordered writer, and whether it was.
type lagInsert struct {
	handed bool
}

// lagWriteUsable reports whether blk's write can be handed off: a native
// QMDB chain whose commitment persists through the flush rows alone, under
// the commit-only canonical rule (the write moves no head), with none of
// the other commitments, caches or snapshot trees a write updates, and a
// block executed on the live tree (not a leader's isolated seal).
func (bc *BlockChain) lagWriteUsable(blk block.IBlock, ibs *state.IntraBlockState) bool {
	if !lagWriteAsync || ibs == nil || !bc.qmdbEnabled || bc.qmdbRootComputer == nil || bc.ChainDB == nil {
		return false
	}
	if _, ok := blk.(*block.Block); !ok {
		return false
	}
	if !bc.canonicalByCommitOnly() || bc.jmtEnabled || bc.bmtEnabled || bc.verkleEnabled || bc.mptEnabled ||
		bc.ltHashEnabled || bc.snapshotTree != nil || bc.zkProving {
		return false
	}
	if layered.ExtractCache(bc.ChainDB) != nil {
		return false
	}
	if rc := ibs.GetRootComputer(); rc != state.RootComputer(bc.qmdbRootComputer) {
		return false
	}
	return bc.qmdbRootComputer.FlushPersistsAll()
}

// lagWriteFastCandidate reports whether chain is a lag import of the
// in-flight write's child that may run while the write is in flight (bc.lock
// held): at deferred depth >= 2 its header reports a durable block, so
// nothing it checks needs the pending one's rows.
func (bc *BlockChain) lagWriteFastCandidate(chain []block.IBlock, lag *lagInsert) bool {
	j := bc.lagWrite.inflight
	if j == nil || lag == nil || len(chain) != 1 {
		return false
	}
	b := chain[0]
	n := b.Number64().Uint64()
	if b.ParentHash() != j.hash || n != j.number+1 {
		return false
	}
	hdr, ok := b.Header().(*block.Header)
	if !ok || bc.chainConfig == nil || !bc.chainConfig.IsDeferredExecution(hdr.Time) {
		return false
	}
	return block.DeferredDepthAt(n) >= 2
}

// lagWriteFastReady re-checks, inside the import of the in-flight write's
// child, that the live tree is exactly the parent: the same root, and no
// appends of anything else waiting to be peeled.
func (bc *BlockChain) lagWriteFastReady() bool {
	j := bc.lagWrite.inflight
	if j == nil {
		return false
	}
	rc := bc.qmdbRootComputer
	if j.capture != nil {
		// N42_LAG_HANDOFF_LEAN: the flush may still hold the tree's lock;
		// j.root was read with the mutation count j.gen, so an unchanged
		// count is an unchanged root.
		return rc.LastUndo() == nil && rc.AppliedState().Gen == j.gen
	}
	return rc.LastUndo() == nil && rc.Root() == j.root
}

// lagWritePending returns the block whose write is in flight when it is
// hash, else nil (any goroutine).
func (bc *BlockChain) lagWritePending(hash types.Hash) *lagWriteJob {
	if j := bc.lagWrite.visible.Load(); j != nil && j.hash == hash {
		return j
	}
	return nil
}

// lagAppliedIs reports whether the applied state is exactly hash at number:
// the in-flight write's block (applied in memory), or the durable marker.
func (bc *BlockChain) lagAppliedIs(hash types.Hash, number uint64) bool {
	if j := bc.lagWrite.visible.Load(); j != nil {
		return j.hash == hash && j.number == number
	}
	return bc.AppliedHeadIsExactly(hash, number)
}

// lagWriteHandOff hands blk's write to the ordered writer (bc.lock held,
// right after its root): it waits for the previous write, captures the
// block's QMDB rows and undo record from the tree, and starts the write.
//
// early is the block's state write preparation when it was started inside
// the root (lagWriteEarlyPrep), else nil.
func (bc *BlockChain) lagWriteHandOff(blk *block.Block, receipts []*block.Receipt, ibs *state.IntraBlockState,
	nopay map[types.Address]*uint256.Int, ahead *aheadExec, early *stateWritePrep) error {
	handAt := time.Now()
	// The block's CommitBlock needs nothing of the previous write: it starts
	// now (or started in the root) and overlaps the wait below.
	prep := early
	if prep == nil && lagWritePrepare && bc.chainConfig != nil {
		prep = bc.startStateWritePrep(ibs, blk.Number64().Uint64())
	}
	// So do its frames, which go to the frame store in their own durable
	// transaction (a block that ends up not written leaves them there, as
	// content-addressed rows nothing names).
	frames := startFramesWrite(blk)
	var logs *logsPrep
	if lagWritePrepare && len(receipts) > 0 {
		logs = startLogsPrep(receipts)
	}
	failed := func(err error) error {
		if prep != nil {
			// Nothing writes this block now; let the preparation finish
			// with the block state it owns.
			_, _ = prep.wait()
		}
		if frames != nil {
			_ = frames.wait()
		}
		if logs != nil {
			_, _ = logs.wait()
		}
		return err
	}
	tWait := time.Now()
	lean := lagHandoffLean
	doneWait, joined, err := bc.joinLagWriteMode(lean)
	if err != nil {
		return failed(err)
	}
	bc.lagWrite.lastWait.Store(int64(time.Since(tWait)))
	hdr, _ := blk.Header().(*block.Header)
	j := &lagWriteJob{blk: blk, hdr: hdr, hash: blk.Hash(), number: blk.Number64().Uint64(),
		receipts: receipts, ibs: ibs, nopay: nopay, future: bc.receiptsFuture, ahead: ahead,
		prep: prep, frames: frames, logs: logs, handAt: handAt, done: make(chan struct{})}
	rc := bc.qmdbRootComputer
	if lean {
		// The undo record and the root first (neither depends on the
		// flush or the eviction), then the previous block's eviction and
		// this block's flush on their own goroutine, ordered here: they
		// hold the tree's lock until done (StartFlushTo).
		j.undo = rc.TakeUndo()
		if j.undo == nil {
			j.undo = &qmdb.BlockUndo{PrevNextSlot: rc.Tree().NextSlot()}
		}
		j.root = rc.Root()
		j.gen = rc.AppliedState().Gen
		j.capture = rc.StartFlushTo(&j.flush, joined)
		lagLeanCaptures.Add(1)
	} else {
		if _, err := rc.FlushTo(&j.flush); err != nil {
			rc.AbortFlushed()
			return failed(fmt.Errorf("capturing QMDB rows of block %d: %w", j.number, err))
		}
		j.undo = rc.TakeUndo()
		if j.undo == nil {
			j.undo = &qmdb.BlockUndo{PrevNextSlot: rc.Tree().NextSlot()}
		}
		j.root = rc.Root()
	}
	j.execRoot, j.execRootOK = ibs.LastIntermediateRoot()
	p := &bc.lagPipe
	p.mu.Lock()
	if par := p.parent; par != nil && par.hash == j.hash {
		j.post = par.post
	}
	p.mu.Unlock()
	bc.lagWrite.inflight = j
	bc.lagWrite.visible.Store(j)
	bc.lagWrite.handed.Add(1)
	go bc.runLagWrite(j)
	bc.lagResultReadySoon(j)
	bc.lagWrite.lastHandoff.Store(int64(time.Since(handAt) - doneWait))
	return nil
}

// lagHandoffLean is N42_LAG_HANDOFF_LEAN (default off, r68a). With it on
// the hand-off's QMDB work leaves the importing goroutine: the previous
// write's eviction (EvictFlushed, joinLagWrite's tail) and the block's
// FlushTo into its row buffer run on their own goroutine
// (QMDBRootComputer.StartFlushTo), which holds the tree's lock from the
// hand-off until they are done, so everything that touches the tree after
// the hand-off -- the child's SetCold and root, a reader's Lookup, a join's
// CommitFlushed -- runs after them, on the tree they leave, exactly as
// when they ran in the hand-off. The writer waits for the rows before it
// replays them; the job is not done before they are captured. The rows,
// the undo record and the root are the same; a failed capture fails the
// write (the tree is reloaded and the block queued again) instead of the
// hand-off.
var lagHandoffLean = os.Getenv("N42_LAG_HANDOFF_LEAN") == "1"

// lagLeanCaptures counts the hand-offs whose rows were captured off the
// importing goroutine (since start).
var lagLeanCaptures atomic.Uint64

// SetLagHandoffLeanForTest switches the lean hand-off and returns the
// restore.
func SetLagHandoffLeanForTest(on bool) func() {
	prev := lagHandoffLean
	lagHandoffLean = on
	return func() { lagHandoffLean = prev }
}

// LagHandoffStats returns the last hand-off's serial work on the importing
// goroutine (its span less the wait for the previous write).
func (bc *BlockChain) LagHandoffStats() time.Duration {
	return time.Duration(bc.lagWrite.lastHandoff.Load())
}

// runLagWrite is the writer goroutine of one block.
func (bc *BlockChain) runLagWrite(j *lagWriteJob) {
	if fn := lagWriteBeforeCommitForTest; fn != nil {
		j.err = fn(j.number)
	}
	if j.err == nil {
		_, j.err = bc.writeBlockWithStateJob(j)
	}
	if j.capture != nil {
		// Not done before the rows are captured, whatever the write did.
		if _, cerr := j.capture.Wait(); cerr != nil && j.err == nil {
			j.err = cerr
		}
	}
	if j.ahead != nil {
		j.ahead.release()
	}
	// Durable (the block cache and the store hold it now) or failed: either
	// way readers stop seeing it as pending.
	bc.lagWrite.visible.CompareAndSwap(j, nil)
	if j.err != nil {
		bc.lagWrite.failed.Add(1)
		log.Error("lag writer: block write failed; memory is ahead of the store until the next import reloads it",
			"number", j.number, "hash", j.hash, "err", j.err)
	}
	bc.lagWriteNotify(j)
	close(j.done)
}

// joinLagWrite waits for the write in flight (bc.lock held) and settles the
// live tree: the flushed window is adopted and evicted once durable; after a
// failure the tree is reloaded from the store and the block queued again.
func (bc *BlockChain) joinLagWrite() error {
	_, _, err := bc.joinLagWriteMode(false)
	return err
}

// joinLagWriteMode is joinLagWrite; with skipEvict the eviction is left to
// the caller (the lean hand-off runs it in StartFlushTo). It also returns
// how long it waited for the write to finish and whether it joined one
// that succeeded.
func (bc *BlockChain) joinLagWriteMode(skipEvict bool) (time.Duration, bool, error) {
	j := bc.lagWrite.inflight
	if j == nil {
		return 0, false, nil
	}
	if fn := lagWriteJoinForTest; fn != nil {
		fn(j.number)
	}
	t0 := time.Now()
	<-j.done
	wait := time.Since(t0)
	bc.lagWrite.inflight = nil
	rc := bc.qmdbRootComputer
	if j.err != nil {
		bc.recoverLagWrite(j)
		return wait, false, fmt.Errorf("lag writer: block %d not written: %w", j.number, j.err)
	}
	rc.CommitFlushed()
	if !skipEvict {
		rc.EvictFlushed()
	}
	return wait, true, nil
}

// recoverLagWrite restores the memory-equals-store invariant after a failed
// write: the tree (and anything applied on it since) is reloaded from the
// store, the pipeline's snapshot and ahead run are dropped and the block is
// handed to the lag executor again.
func (bc *BlockChain) recoverLagWrite(j *lagWriteJob) {
	rc := bc.qmdbRootComputer
	rc.AbortFlushed()
	rc.TakeUndo()
	if err := bc.ChainDB.Update(bc.headWriteContext(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		lerr := rc.LoadFrom(tx)
		rc.SetCold(nil)
		return lerr
	}); err != nil {
		log.Error("lag writer: reloading the QMDB tree after a failed write FAILED -- state may diverge", "err", err)
	} else {
		log.Warn("lag writer: QMDB tree reloaded from the store after a failed write", "number", j.number)
	}
	bc.clearReadThroughCache()
	bc.lagPipeReset()
	bc.EnqueueLagImport(j.blk)
}

// waitLagWrite waits for a write in flight to finish (shutdown).
func (bc *BlockChain) waitLagWrite() {
	if j := bc.lagWrite.visible.Load(); j != nil {
		<-j.done
	}
}

// lagWriteNotify queues j for the notifier, which reports written blocks to
// consensus in write order, off the writer and off bc.lock.
func (bc *BlockChain) lagWriteNotify(j *lagWriteJob) {
	w := &bc.lagWrite
	w.notifyOnce.Do(func() {
		w.notify = make(chan *lagWriteJob, 1024)
		go bc.lagNotifyLoop(w.notify)
	})
	w.notify <- j
}

func (bc *BlockChain) lagNotifyLoop(ch chan *lagWriteJob) {
	ctx := bc.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-ch:
			if j.err != nil {
				// Recover now: the import that would join it may never come
				// (the child waits for its parent to be applied).
				bc.lock.Lock()
				if bc.lagWrite.inflight == j {
					_ = bc.joinLagWrite()
				}
				bc.lock.Unlock()
				continue
			}
			bc.forgetAvailable(j.hash)
			bc.lagReported(j.number, j.hash, j.blk.TxHash())
		}
	}
}

// lagReported counts an imported block and tells consensus.
func (bc *BlockChain) lagReported(number uint64, hash, txHash types.Hash) {
	le := &bc.lagExec
	le.imported.Add(1)
	le.lastNum.Store(number)
	if fn := le.hook.Load(); fn != nil {
		(*fn)(hash, txHash)
	}
}

// LagWriteStats returns the writes handed to the ordered writer, the ones
// that failed, and the last hand-off's wait for the previous write.
func (bc *BlockChain) LagWriteStats() (handed, failed uint64, lastWait time.Duration) {
	w := &bc.lagWrite
	return w.handed.Load(), w.failed.Load(), time.Duration(w.lastWait.Load())
}

// LagWriteOverlaps returns how many imports ran while their parent's write
// was in flight.
func (bc *BlockChain) LagWriteOverlaps() uint64 { return bc.lagWrite.overlap.Load() }

// pendingHeader returns the header of the block whose write is in flight
// when it is hash at number.
func (bc *BlockChain) pendingHeader(hash types.Hash, number uint64) *block.Header {
	if j := bc.lagWritePending(hash); j != nil && j.number == number {
		return j.hdr
	}
	return nil
}

// readHeaderOrPending reads a header from tx, or from the block whose write
// is in flight. A write that lands after tx began is read through the
// chain's header lookup (a fresh transaction).
func (bc *BlockChain) readHeaderOrPending(tx kv.Getter, hash types.Hash, number uint64) *block.Header {
	if h := bc.pendingHeader(hash, number); h != nil {
		return h
	}
	if h := rawdb.ReadHeader(tx, hash, number); h != nil {
		return h
	}
	if bc.ChainDB == nil {
		return nil
	}
	if ih := bc.GetHeader(hash, uint256.NewInt(number)); ih != nil {
		if h, ok := ih.(*block.Header); ok && h.Number.Uint64() == number {
			return h
		}
	}
	return nil
}

var errLagWriteInjected = errors.New("lag writer: injected failure (test)")
