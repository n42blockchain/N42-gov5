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

	notifyOnce sync.Once
	notify     chan *lagWriteJob

	handed   atomic.Uint64
	failed   atomic.Uint64
	overlap  atomic.Uint64 // imports that ran while their parent's write was in flight
	lastWait atomic.Int64  // the last hand-off's wait for the previous write
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
	ahead    *aheadExec // its read transaction is released once written
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
}

// startStateWritePrep runs state.PrepareStateWrite on its own goroutine. It
// owns ibs from here on, as the write would.
func (bc *BlockChain) startStateWritePrep(ibs *state.IntraBlockState, number uint64) *stateWritePrep {
	sp := &stateWritePrep{done: make(chan struct{})}
	rules := bc.chainConfig.Rules(number)
	go func() {
		t0 := time.Now()
		sp.p, sp.err = state.PrepareStateWrite(ibs, rules, number)
		sp.took = time.Since(t0)
		close(sp.done)
	}()
	return sp
}

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
func (bc *BlockChain) lagWriteHandOff(blk *block.Block, receipts []*block.Receipt, ibs *state.IntraBlockState,
	nopay map[types.Address]*uint256.Int, ahead *aheadExec) error {
	// The block's CommitBlock needs nothing of the previous write: it starts
	// now and overlaps the wait below.
	var prep *stateWritePrep
	if lagWritePrepare && bc.chainConfig != nil {
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
	if err := bc.joinLagWrite(); err != nil {
		return failed(err)
	}
	bc.lagWrite.lastWait.Store(int64(time.Since(tWait)))
	hdr, _ := blk.Header().(*block.Header)
	j := &lagWriteJob{blk: blk, hdr: hdr, hash: blk.Hash(), number: blk.Number64().Uint64(),
		receipts: receipts, ibs: ibs, nopay: nopay, future: bc.receiptsFuture, ahead: ahead,
		prep: prep, frames: frames, logs: logs, done: make(chan struct{})}
	rc := bc.qmdbRootComputer
	if _, err := rc.FlushTo(&j.flush); err != nil {
		rc.AbortFlushed()
		return failed(fmt.Errorf("capturing QMDB rows of block %d: %w", j.number, err))
	}
	j.undo = rc.TakeUndo()
	if j.undo == nil {
		j.undo = &qmdb.BlockUndo{PrevNextSlot: rc.Tree().NextSlot()}
	}
	j.root = rc.Root()
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
	return nil
}

// runLagWrite is the writer goroutine of one block.
func (bc *BlockChain) runLagWrite(j *lagWriteJob) {
	if fn := lagWriteBeforeCommitForTest; fn != nil {
		j.err = fn(j.number)
	}
	if j.err == nil {
		_, j.err = bc.writeBlockWithStateJob(j)
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
	j := bc.lagWrite.inflight
	if j == nil {
		return nil
	}
	if fn := lagWriteJoinForTest; fn != nil {
		fn(j.number)
	}
	<-j.done
	bc.lagWrite.inflight = nil
	rc := bc.qmdbRootComputer
	if j.err != nil {
		bc.recoverLagWrite(j)
		return fmt.Errorf("lag writer: block %d not written: %w", j.number, j.err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	return nil
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
	bc.lagPipeForget(j.hash)
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
