// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// canonFoldOn is N42_CANON_FOLD (default off, r89a).
//
// On the executor the chain database's single MDBX writer was held 89% of
// the wall time in E1N26 run 68 B4, and the ordered block write waited
// 16-20 ms (mean) to begin, behind the commit-to-canonical transactions
// (12.6 a second, held 11.2 ms each with their own data sync) and the
// consensus-state persists. Two kinds of canonical commit made those
// transactions:
//
//   - a commit whose block this node had already executed (the HotStuff
//     commit loop, with the consensus-state snapshot in the transaction):
//     about 5 a second;
//   - a commit whose block was not executed yet when the CommitQC arrived
//     (vote on availability commits ahead of the lagged execution): its
//     transaction took the writer only to fail the applied-lineage guard
//     and roll back, and the commit was retried by the block's import
//     notification in a third transaction once the block was written:
//     about 8 a second.
//
// With the switch on (and QMDB, not on an E=1 light node):
//
//   - a commit with a consensus-state hook, while an ordered block write is
//     in flight whose block is at or above the committed height, is handed
//     to that write: its rows are written inside the block's transaction,
//     just before its commit, and the commit returns once that transaction
//     is durable (the canonical commit's own transaction is gone);
//   - a commit whose block is not on the applied lineage is refused on a
//     read transaction, without the writer, with the same error as before
//     (the caller defers it as before), and is remembered: the ordered write
//     that makes it applied writes its canonical rows in its own
//     transaction; the import notification's retry then finds it canonical
//     on a read transaction and needs no writer either;
//   - every other commit runs its own transaction exactly as before.
//
// What is written is unchanged: the same canonical rows, head pointers,
// committed-head marker, own-unverified mark removal, lookup rows (without
// the tail indexer) and consensus-state record, in a transaction that
// commits atomically with the block's rows. The folded commit's checks
// (the block, its applied lineage, the duplicate guard, every ancestor
// header) are all read before anything is written, so a commit that fails
// them writes nothing and never fails the block's write; it falls back to
// its own transaction. A crash leaves either both the block and its
// canonical rows or neither; the commit is then redone by the restart's
// recovered commit, as for a crash before a separate commit transaction.
// After the transaction is durable the commit's in-memory effects
// (current block, frames committed, transaction index tail, the commit
// notifications) run in canonical order on one goroutine, and only then
// does a waiting commit return: a reader never sees a head that is not
// durable, as before.
var canonFoldOn = parseOptIn(os.Getenv("N42_CANON_FOLD"))

// SetCanonFoldForTest switches N42_CANON_FOLD and returns the restore.
func SetCanonFoldForTest(on bool) func() {
	prev := canonFoldOn
	canonFoldOn = on
	return func() { canonFoldOn = prev }
}

// canonFoldAfterDrainForTest, when set, runs inside a write's transaction
// right after its fold, with how many commits it folded; an error aborts
// the transaction there (tests: a crash with the canonical rows put).
var canonFoldAfterDrainForTest func(number uint64, folded int) error

// canonFoldMaxDeferred bounds the remembered not-yet-applied commits.
const canonFoldMaxDeferred = 1024

// canonFoldReq is one canonical commit folded into an ordered write.
type canonFoldReq struct {
	hash   types.Hash
	number uint64
	blk    *block.Block
	inTx   func(kv.RwTx) error
	// done is closed once the commit is durable and its effects ran, or it
	// failed (err); nil for a remembered commit nobody waits for.
	done chan struct{}
	err  error

	queuedAt time.Time
	// Filled by the write that folds it.
	applied bool // rows written (false: a duplicate)
	rewrote bool
	indexed *block.Block
	dropped bool // not on the write's lineage: forget it
}

// canonFoldState is the fold's coordination with the ordered writer.
type canonFoldState struct {
	mu sync.Mutex
	// open: the ordered write accepting folds (from its start to its
	// drain, just before its commit); waiting: the commits handed to it.
	open    *lagWriteJob
	waiting []*canonFoldReq
	// deferred: commits refused as not applied yet, by hash.
	deferred map[types.Hash]uint64

	postOnce sync.Once
	post     chan []*canonFoldReq

	foldedWaited, foldedDeferred, refusedNoWriter, dupNoWriter, ownTxn atomic.Uint64
}

// commitToCanonicalFold is CommitToCanonicalWith's N42_CANON_FOLD path.
// handled false: the commit runs its own transaction as before.
func (bc *BlockChain) commitToCanonicalFold(hash types.Hash, inTx func(kv.RwTx) error) (bool, error) {
	cf := &bc.canonFold
	var blk *block.Block
	if bc.blockCache != nil {
		if b, ok := bc.blockCache.Get(hash); ok && b != nil {
			blk = b
		}
	}
	if blk == nil {
		if j := bc.lagWritePending(hash); j != nil {
			blk = j.blk
		}
	}
	var dup bool
	var lineageErr error
	if err := bc.ChainDB.View(bc.ctx, func(tx kv.Tx) error {
		if blk == nil {
			b, err := rawdb.ReadBlockByHash(tx, hash)
			if err != nil {
				return err
			}
			blk = b
		}
		if blk == nil {
			return nil
		}
		number := blk.Number64().Uint64()
		dup = bc.canonPureDuplicate(tx, hash, number)
		if !dup {
			lineageErr = requireAppliedCommitLineage(bc.ctx, tx, hash, number)
		}
		return nil
	}); err != nil {
		return true, err
	}
	if blk == nil {
		// Not stored. A block held for its lagged execution (available,
		// not written yet) cannot be applied either: the same refusal as
		// the transaction's, without the writer, remembered for its write.
		// Any other unknown block: the transaction reports it, as before.
		if ab := bc.AvailableBlock(hash); ab != nil {
			blk = ab
			lineageErr = fmt.Errorf("committed block %s not in db", hash.Hex())
		} else {
			cf.ownTxn.Add(1)
			return false, nil
		}
	}
	number := blk.Number64().Uint64()
	if dup {
		// Already canonical with nothing left to write: the duplicate guard
		// would return here without writing (the hook does not run there
		// either).
		cf.dupNoWriter.Add(1)
		return true, nil
	}
	if inTx != nil {
		// Only the consensus loop's commit waits for a fold. A hookless
		// commit (the import notification's retry, a restart's recovered
		// commit) runs on the notifier, whose report of the next write
		// would wait behind it.
		req := &canonFoldReq{hash: hash, number: number, blk: blk, inTx: inTx, done: make(chan struct{}), queuedAt: time.Now()}
		if bc.canonFoldEnqueue(req) {
			<-req.done
			if req.err == nil {
				return true, nil
			}
			// The write failed, or the commit was not on its lineage: its
			// own transaction decides, as before.
			log.Debug("commit-to-canonical: fold failed, own transaction", "number", number, "err", req.err)
			cf.ownTxn.Add(1)
			return false, nil
		}
	}
	if lineageErr != nil {
		bc.canonFoldRemember(hash, number)
		cf.refusedNoWriter.Add(1)
		return true, lineageErr
	}
	cf.ownTxn.Add(1)
	return false, nil
}

// canonPureDuplicate reports whether committing hash would write nothing:
// canonical at or below the committed head, no own-unverified mark, and
// the tail indexer owning the lookups (commitPreRead's rule).
func (bc *BlockChain) canonPureDuplicate(tx kv.Getter, hash types.Hash, number uint64) bool {
	if bc.txIndexer == nil {
		return false
	}
	hn := rawdb.ReadHeaderNumber(tx, rawdb.ReadHeadBlockHash(tx))
	if hn == nil || *hn < number {
		return false
	}
	existing, err := rawdb.ReadCanonicalHash(tx, number)
	if err != nil || existing != hash {
		return false
	}
	return !rawdb.IsOwnUnverifiedMarked(tx, hash)
}

// canonFoldOpen makes j the write that accepts folds (its runLagWrite).
func (bc *BlockChain) canonFoldOpen(j *lagWriteJob) {
	cf := &bc.canonFold
	cf.mu.Lock()
	cf.open = j
	cf.mu.Unlock()
}

// canonFoldEnqueue hands req to the open write when its block is at or
// above req's height (else false: nothing will make it applied in time).
func (bc *BlockChain) canonFoldEnqueue(req *canonFoldReq) bool {
	cf := &bc.canonFold
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if j := cf.open; j == nil || j.number < req.number {
		return false
	}
	cf.waiting = append(cf.waiting, req)
	return true
}

// canonFoldRemember records a commit refused as not applied yet.
func (bc *BlockChain) canonFoldRemember(hash types.Hash, number uint64) {
	cf := &bc.canonFold
	cf.mu.Lock()
	defer cf.mu.Unlock()
	if cf.deferred == nil || len(cf.deferred) >= canonFoldMaxDeferred {
		cf.deferred = make(map[types.Hash]uint64)
	}
	cf.deferred[hash] = number
}

// canonFoldDrain runs inside j's transaction, after its rows and just
// before its commit: it closes j to further folds and writes the canonical
// rows of every commit handed to it and of every remembered commit at or
// below its height that its block now makes applied. A commit that fails
// its checks writes nothing; only a failed write fails the transaction.
func (bc *BlockChain) canonFoldDrain(tx kv.RwTx, j *lagWriteJob) (int, error) {
	cf := &bc.canonFold
	cf.mu.Lock()
	var reqs []*canonFoldReq
	if cf.open == j {
		reqs = cf.waiting
		cf.waiting = nil
		cf.open = nil
	}
	waited := make(map[types.Hash]bool, len(reqs))
	for _, r := range reqs {
		waited[r.hash] = true
	}
	for h, n := range cf.deferred {
		if n <= j.number && !waited[h] {
			reqs = append(reqs, &canonFoldReq{hash: h, number: n})
		}
	}
	cf.mu.Unlock()
	if len(reqs) == 0 {
		return 0, nil
	}
	sort.SliceStable(reqs, func(a, b int) bool { return reqs[a].number < reqs[b].number })
	folded := 0
	for _, r := range reqs {
		if err := bc.canonFoldApply(tx, r, j); err != nil {
			return folded, err
		}
		if r.err == nil {
			folded++
		}
	}
	j.canon = reqs
	return folded, nil
}

// canonFoldApply writes r's canonical commit into j's transaction. A
// failed check sets r.err and writes nothing; the returned error is a
// failed write.
func (bc *BlockChain) canonFoldApply(tx kv.RwTx, r *canonFoldReq, j *lagWriteJob) error {
	blk := r.blk
	if blk == nil && r.hash == j.hash {
		blk = j.blk
	}
	if blk == nil {
		b, err := rawdb.ReadBlockByHash(tx, r.hash)
		if err != nil {
			r.err = err
			return nil
		}
		blk = b
	}
	if blk == nil {
		r.err = fmt.Errorf("committed block %s not in db", r.hash.Hex())
		return nil
	}
	r.blk = blk
	number := blk.Number64().Uint64()
	if err := requireAppliedCommitLineage(bc.ctx, tx, r.hash, number); err != nil {
		r.err = err
		// At or below the write's height and not on its lineage: a sibling,
		// left to the consensus layer's own retry.
		r.dropped = true
		return nil
	}
	// The duplicate guard (CommitToCanonicalWith): already canonical at or
	// below the committed head.
	if hn := rawdb.ReadHeaderNumber(tx, rawdb.ReadHeadBlockHash(tx)); hn != nil && *hn >= number {
		if existing, rerr := rawdb.ReadCanonicalHash(tx, number); rerr == nil && existing == r.hash {
			if rawdb.IsOwnUnverifiedMarked(tx, r.hash) {
				if err := rawdb.ClearOwnUnverifiedMark(tx, r.hash); err != nil {
					return err
				}
			}
			if bc.txIndexer == nil {
				rawdb.WriteTxLookupEntries(tx, blk)
			}
			return nil
		}
	}
	// The walk to the canonical chain, read in full before anything is
	// written (it only ever reads heights below the one it would write).
	type canonRow struct {
		n uint64
		h types.Hash
	}
	var rows []canonRow
	curNum, curHash, curParent := number, r.hash, blk.ParentHash()
	for {
		existing, rerr := rawdb.ReadCanonicalHash(tx, curNum)
		if rerr != nil {
			r.err = rerr
			return nil
		}
		if existing != curHash {
			rows = append(rows, canonRow{curNum, curHash})
		}
		if curNum == 0 {
			break
		}
		below, berr := rawdb.ReadCanonicalHash(tx, curNum-1)
		if berr != nil {
			r.err = berr
			return nil
		}
		if existing == curHash && below == curParent {
			break
		}
		hdr := rawdb.ReadHeader(tx, curParent, curNum-1)
		if hdr == nil {
			r.err = fmt.Errorf("commit-to-canonical: ancestor header %d/%x missing, canonical rewrite incomplete", curNum-1, curParent[:8])
			return nil
		}
		curNum--
		curHash = curParent
		curParent = hdr.ParentHash
	}
	above, aerr := rawdb.ReadCanonicalHash(tx, number+1)
	if aerr != nil {
		r.err = aerr
		return nil
	}
	// Writes.
	if err := rawdb.ClearOwnUnverifiedMark(tx, r.hash); err != nil {
		return err
	}
	for _, row := range rows {
		if err := rawdb.WriteCanonicalHash(tx, row.h, row.n); err != nil {
			return err
		}
	}
	r.rewrote = len(rows) > 0
	if above != (types.Hash{}) {
		if err := rawdb.TruncateCanonicalHash(tx, number+1, false); err != nil {
			return err
		}
		r.rewrote = true
	}
	rawdb.WriteHeadBlockHash(tx, r.hash)
	if err := rawdb.WriteHeadHeaderHash(tx, r.hash); err != nil {
		return err
	}
	if err := rawdb.WriteHotStuffCommittedHead(tx, r.hash); err != nil {
		return err
	}
	if bc.txIndexer == nil {
		rawdb.WriteTxLookupEntries(tx, blk)
	} else {
		r.indexed = blk
	}
	if r.inTx != nil {
		if herr := r.inTx(tx); herr != nil {
			log.Warn("commit-to-canonical: in-transaction hook failed", "number", number, "err", herr)
		}
	}
	r.applied = true
	return nil
}

// canonFoldSettle runs once j's write returned (err: it failed). The
// commits folded into a durable write have their effects run in order on
// the fold's goroutine; on a failure every waiting commit is released with
// the error (it retries in its own transaction) and the remembered ones
// stay remembered.
func (bc *BlockChain) canonFoldSettle(j *lagWriteJob, err error) {
	cf := &bc.canonFold
	cf.mu.Lock()
	var unfolded []*canonFoldReq
	if cf.open == j {
		// Failed before its drain: nothing was folded.
		unfolded = cf.waiting
		cf.waiting = nil
		cf.open = nil
	}
	if err == nil {
		for _, r := range j.canon {
			if r.err == nil || r.dropped {
				delete(cf.deferred, r.hash)
			}
		}
	}
	cf.mu.Unlock()
	if err == nil {
		err = fmt.Errorf("lag writer: block %d write closed before the fold", j.number)
		for _, r := range unfolded {
			r.err = err
			close(r.done)
		}
		if len(j.canon) > 0 {
			bc.canonFoldPostQueue() <- j.canon
		}
		return
	}
	for _, r := range append(unfolded, j.canon...) {
		if r.done != nil {
			if r.err == nil {
				r.err = err
			}
			close(r.done)
		}
	}
	j.canon = nil
}

// canonFoldPostQueue returns the queue of the fold's effects goroutine.
func (bc *BlockChain) canonFoldPostQueue() chan []*canonFoldReq {
	cf := &bc.canonFold
	cf.postOnce.Do(func() {
		cf.post = make(chan []*canonFoldReq, 1024)
		go bc.canonFoldPostLoop(cf.post)
	})
	return cf.post
}

// canonFoldPostLoop runs the in-memory effects of durable folded commits,
// in the order of their writes, then releases their waiters. It never
// stops while a waiter could be blocked: a waiter is only ever queued on a
// write, and every write's batch comes here.
func (bc *BlockChain) canonFoldPostLoop(ch chan []*canonFoldReq) {
	for batch := range ch {
		for _, r := range batch {
			if r.err == nil {
				bc.canonFoldEffects(r)
			}
			if r.done != nil {
				close(r.done)
			}
		}
	}
}

// canonFoldEffects is CommitToCanonicalWith's work after its transaction,
// for a folded commit.
func (bc *BlockChain) canonFoldEffects(r *canonFoldReq) {
	cf := &bc.canonFold
	if r.done != nil {
		cf.foldedWaited.Add(1)
	} else {
		cf.foldedDeferred.Add(1)
	}
	if !r.applied {
		return // a duplicate: nothing written, nothing notified (as before)
	}
	blk := r.blk
	number := blk.Number64().Uint64()
	// A newer head may already be published by a commit that ran its own
	// transaction: never move the head back.
	stale := false
	if cur := bc.currentBlock.Load(); cur != nil && cur.Number64().Uint64() > number {
		stale = true
	}
	if !stale {
		bc.currentBlock.Store(blk)
		bc.framesCommitted(blk)
	}
	if r.rewrote {
		if cache := layered.ExtractCache(bc.ChainDB); cache != nil {
			cache.Clear()
		}
	}
	if r.indexed != nil {
		bc.indexCommittedTxs(r.indexed)
	}
	var waitedUs int64
	if !r.queuedAt.IsZero() {
		waitedUs = time.Since(r.queuedAt).Microseconds()
	}
	log.Info("commit-to-canonical phases", "number", number, "folded", 1, "waited", r.done != nil,
		"foldWaitUs", waitedUs, "stale", stale,
		"foldedWaited", cf.foldedWaited.Load(), "foldedDeferred", cf.foldedDeferred.Load(),
		"refusedNoWriter", cf.refusedNoWriter.Load(), "dupNoWriter", cf.dupNoWriter.Load(), "ownTxn", cf.ownTxn.Load())
	if !stale {
		bc.notifyBlockCommitted(number)
		event.GlobalEvent.Send(common.ChainHighestBlock{Block: *blk, Inserted: true})
	}
}

// indexCommittedTxs hands a committed block's transaction hashes to the
// tail indexer (CommitToCanonicalWith's post-commit step).
func (bc *BlockChain) indexCommittedTxs(indexed *block.Block) {
	txs := indexed.Transactions()
	hashes := make([]types.Hash, len(txs))
	workers := runtime.GOMAXPROCS(0)
	if workers > 32 {
		workers = 32
	}
	if len(txs) < 4096 || workers < 2 {
		for i, tx := range txs {
			hashes[i] = tx.Hash()
		}
	} else {
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func(start int) {
				defer wg.Done()
				for i := start; i < len(txs); i += workers {
					hashes[i] = txs[i].Hash()
				}
			}(w)
		}
		wg.Wait()
	}
	bc.txIndexer.Add(indexed.Number64().Uint64(), hashes)
}
