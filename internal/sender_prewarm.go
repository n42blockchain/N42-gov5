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

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// Per-block sender recovery ahead of the import (P7).
//
// Admission recovery (internal/frame/recover.go) works through every frame
// the store admits in arrival order; when ingest outruns it (S99: 8-14k
// frames pending, ~530 frames/s recovered against ~615 consumed) a block's
// frames reach the executor with senders missing, and the import recovered
// them as a barrier before its first wave (recov 62 ms median, 435 ms in
// the first minute of a leg). A block is known well before it executes: it
// is queued for the lag executor at its availability check (followers) or
// at its seal (the leader), and executes once its parent is rooted. So the
// block's missing senders are recovered from that moment, on a job whose
// work the import shares: both pull chunks from one cursor, and the import
// waits only for what is left. Nothing here changes a verdict: Sender
// memoises the same address the import would derive.

// senderJobChunk is how many transactions a worker takes at a time.
const senderJobChunk = 128

// senderJobCap bounds the jobs remembered (a dropped job's work is redone
// by the import's own recovery).
const senderJobCap = 64

// senderPrewarmWorkers is N42_SENDER_PREWARM_WORKERS: the background
// workers per block job (0 disables the job; default half the import's
// recovery fan-out).
var senderPrewarmWorkers = envSenderPrewarmWorkers()

func envSenderPrewarmWorkers() int {
	if v := os.Getenv("N42_SENDER_PREWARM_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return -1
}

func prewarmWorkers() int {
	if senderPrewarmWorkers >= 0 {
		return senderPrewarmWorkers
	}
	n := senderRecoveryFanout() / 2
	if n < 1 {
		n = 1
	}
	return n
}

// SetSenderPrewarmWorkersForTest sets the background workers per job and
// returns the restore.
func SetSenderPrewarmWorkersForTest(n int) func() {
	prev := senderPrewarmWorkers
	senderPrewarmWorkers = n
	return func() { senderPrewarmWorkers = prev }
}

// FrameSpan is one frame of a frame block's transaction list: txs[Lo:Hi]
// decode from frame H.
type FrameSpan struct {
	H      types.Hash
	Lo, Hi int
}

// FrameSpansOf returns the spans of a frame block's n transactions from its
// frames' headers, or nil when the body is not a resolved frame body or an
// undecodable entry was dropped (the counts no longer line up): such a
// block recovers per transaction, as before P8a.
func FrameSpansOf(body *block.Body, n int) []FrameSpan {
	if body == nil || !body.IsFrameBody() || len(body.FrameData) != len(body.FrameHashes) {
		return nil
	}
	spans := make([]FrameSpan, len(body.FrameHashes))
	lo := 0
	for i, raw := range body.FrameData {
		h, err := frame.ParseHeader(raw)
		if err != nil {
			return nil
		}
		hi := lo + int(h.TxCount)
		spans[i] = FrameSpan{H: body.FrameHashes[i], Lo: lo, Hi: hi}
		lo = hi
	}
	if lo != n {
		return nil
	}
	return spans
}

// FrameSpansFor returns the spans of frames whose decoded transactions are
// per[i] (concatenated in order), named by hashes.
func FrameSpansFor(hashes []types.Hash, per [][]*transaction.Transaction) []FrameSpan {
	if len(hashes) != len(per) {
		return nil
	}
	spans := make([]FrameSpan, len(per))
	lo := 0
	for i, p := range per {
		spans[i] = FrameSpan{H: hashes[i], Lo: lo, Hi: lo + len(p)}
		lo += len(p)
	}
	return spans
}

// senderJob recovers one block's senders. With spans (a frame block) the
// unit of work is a frame, and each frame's recovery is claimed in the
// frame store (P8a): admission, this job and any other block holding the
// frame recover it once between them, and the others take the result.
type senderJob struct {
	signer transaction.Signer
	txs    []*transaction.Transaction
	next   atomic.Int64
	start  time.Time

	spans []FrameSpan
	st    *store.Store
	// src (optional) is consulted before recovering a frame the store has
	// no senders for (P8b attestations).
	src FrameSenderSource

	// The background workers: bgDone closes when the last one exits.
	bgLeft atomic.Int32
	bgDone chan struct{}

	// framesDone counts the spans settled (frame returned true): once it
	// reaches len(spans) every transaction holds its sender, or failed to
	// recover one, and the import has nothing left to do (P10).
	framesDone atomic.Int64
}

func (j *senderJob) run() {
	// A panic costs only this worker's remaining chunks: execution recovers
	// whatever is left inline, as it would without the job.
	defer func() { _ = recover() }()
	if j.spans != nil {
		j.runFrames()
		return
	}
	n := int64(len(j.txs))
	for {
		s := j.next.Add(senderJobChunk) - senderJobChunk
		if s >= n {
			return
		}
		e := s + senderJobChunk
		if e > n {
			e = n
		}
		for _, tx := range j.txs[s:e] {
			if tx != nil {
				_, _ = transaction.Sender(j.signer, tx)
			}
		}
	}
}

// runFrames is run over frames: a first pass takes every frame nobody else
// is on, a second waits for (or then recovers) the frames another path was
// recovering or another source may still answer for.
func (j *senderJob) runFrames() {
	var later []int
	n := int64(len(j.spans))
	for {
		i := j.next.Add(1) - 1
		if i >= n {
			break
		}
		if !j.frame(int(i), false) {
			later = append(later, int(i))
			continue
		}
		j.framesDone.Add(1)
	}
	for _, i := range later {
		j.frame(i, true)
		j.framesDone.Add(1)
	}
}

// complete reports whether every span of a frame job was settled.
func (j *senderJob) complete() bool {
	return j.spans != nil && j.framesDone.Load() == int64(len(j.spans))
}

// frame recovers the senders of span i, or takes them from the store or
// the source. Outside the final pass it returns false instead of waiting.
func (j *senderJob) frame(i int, final bool) bool {
	sp := j.spans[i]
	txs := j.txs[sp.Lo:sp.Hi]
	if j.st != nil {
		for {
			t := j.st.BeginRecover(sp.H, false)
			if t.Senders != nil {
				if frame.ApplySenders(txs, t.Senders) == len(txs) {
					// Every sender attached: nothing left to derive.
					return true
				}
				break
			}
			if t.Wait != nil {
				if !final {
					return false
				}
				<-t.Wait
				continue
			}
			if !t.Own {
				// Not stored (pulled past the budget and evicted, or read
				// from the Frames table): attested senders still apply.
				if ss := j.fromSource(sp.H, len(txs), final); ss != nil {
					frame.ApplySenders(txs, ss)
				}
				break
			}
			return j.owned(t, txs, final)
		}
	} else if ss := j.fromSource(sp.H, len(txs), final); ss != nil {
		frame.ApplySenders(txs, ss)
	}
	// Senders taken from elsewhere leave only failed entries (zero
	// addresses) to derive; with no store this recovers the frame.
	for _, tx := range txs {
		if tx != nil {
			_, _ = transaction.Sender(j.signer, tx)
		}
	}
	return true
}

// owned settles a frame this job claimed: from the source, deferred to the
// last pass while attestations may still arrive, or recovered here. The
// claim is released on every path, a panic included, so no waiter hangs.
func (j *senderJob) owned(t store.RecoverTicket, txs []*transaction.Transaction, final bool) (done bool) {
	var ss *store.Senders
	defer func() { j.st.FinishRecover(t, ss) }()
	if ss = j.fromSource(t.H, len(txs), final); ss != nil {
		frame.ApplySenders(txs, ss)
		return true
	}
	if !final && j.src != nil && j.src.Pending(t.H) {
		return false
	}
	ss = j.recoverTxs(t.H, txs)
	return true
}

// fromSource asks the attestation source for frame h's senders.
func (j *senderJob) fromSource(h types.Hash, n int, final bool) *store.Senders {
	if j.src == nil {
		return nil
	}
	addrs := j.src.Senders(h, n)
	if addrs == nil {
		return nil
	}
	return &store.Senders{Signer: j.signer, Addrs: addrs}
}

// recoverTxs recovers txs (an owned frame h) and returns its senders for
// the store.
func (j *senderJob) recoverTxs(h types.Hash, txs []*transaction.Transaction) *store.Senders {
	addrs := make([]types.Address, len(txs))
	for k, tx := range txs {
		if tx == nil {
			continue
		}
		if a, err := transaction.Sender(j.signer, tx); err == nil {
			addrs[k] = a
		}
	}
	ss := &store.Senders{Signer: j.signer, Addrs: addrs}
	if j.src != nil {
		j.src.Recovered(h, ss)
	}
	return ss
}

// spawn starts the background workers; done runs once after the last one.
func (j *senderJob) spawn(workers int, done func()) {
	if workers < 1 {
		workers = 1
	}
	j.bgDone = make(chan struct{})
	j.bgLeft.Store(int32(workers))
	for i := 0; i < workers; i++ {
		go func() {
			j.run()
			if j.bgLeft.Add(-1) == 0 {
				close(j.bgDone)
				if done != nil {
					done()
				}
			}
		}()
	}
}

// join adds workers to the job and waits until every transaction is done.
func (j *senderJob) join(workers int) {
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j.run()
		}()
	}
	wg.Wait()
	if j.bgDone != nil {
		<-j.bgDone
	}
}

type senderJobs struct {
	mu   sync.Mutex
	m    map[types.Hash]*senderJob
	hits atomic.Uint64

	// done: blocks whose transactions all hold their senders already
	// (block resolution applied every frame's cached senders, or the
	// block's job settled every frame). The import's recovery step is
	// then a lookup, not a walk over every frame and transaction (P10).
	done     map[types.Hash]sendersDone
	doneFIFO []types.Hash
	doneHits atomic.Uint64
}

// sendersDone identifies the transaction list and signer a block's
// senders were completed for.
type sendersDone struct {
	first  *transaction.Transaction
	n      int
	signer transaction.Signer
	// declared: some transaction may declare its own sender (unknown
	// counts as declared), so the import's sender gate still runs.
	declared bool
}

// sendersDoneCap bounds the completed blocks remembered.
const sendersDoneCap = 64

// markSendersDone records that every transaction of block h (txs, under
// signer) holds its sender; declared as in sendersDone. A record of the
// same transaction list is kept (the first one knows at least as much).
func (bc *BlockChain) markSendersDone(h types.Hash, txs []*transaction.Transaction, signer transaction.Signer, declared bool) {
	if bc == nil || len(txs) == 0 || signer == nil {
		return
	}
	js := &bc.senderJobs
	js.mu.Lock()
	defer js.mu.Unlock()
	if js.done == nil {
		js.done = make(map[types.Hash]sendersDone, sendersDoneCap)
	}
	if d, ok := js.done[h]; ok {
		if d.first == txs[0] && d.n == len(txs) {
			return
		}
	} else {
		js.doneFIFO = append(js.doneFIFO, h)
		for len(js.doneFIFO) > sendersDoneCap {
			delete(js.done, js.doneFIFO[0])
			js.doneFIFO = js.doneFIFO[1:]
		}
	}
	js.done[h] = sendersDone{first: txs[0], n: len(txs), signer: signer, declared: declared}
}

// sendersDoneFor reports whether block h's senders are recorded complete.
func (bc *BlockChain) sendersDoneFor(h types.Hash) bool {
	js := &bc.senderJobs
	js.mu.Lock()
	defer js.mu.Unlock()
	_, ok := js.done[h]
	return ok
}

// takeSendersDone reports (and forgets) whether block h's senders were
// completed for exactly txs under signer, and whether a transaction may
// declare its own sender.
func (bc *BlockChain) takeSendersDone(h types.Hash, signer transaction.Signer, txs []*transaction.Transaction) (done, declared bool) {
	if bc == nil || len(txs) == 0 || signer == nil {
		return false, true
	}
	js := &bc.senderJobs
	js.mu.Lock()
	d, ok := js.done[h]
	if ok {
		delete(js.done, h)
	}
	js.mu.Unlock()
	if !ok || d.first != txs[0] || d.n != len(txs) || !d.signer.Equal(signer) {
		return false, true
	}
	js.doneHits.Add(1)
	return true, d.declared
}

// SendersDoneHits returns how many imports found their senders complete.
func (bc *BlockChain) SendersDoneHits() uint64 { return bc.senderJobs.doneHits.Load() }

// startSenderJob resolves blk's frames and starts recovering its senders in
// the background (any goroutine). A block already resolved costs nothing to
// resolve again.
func (bc *BlockChain) startSenderJob(b *block.Block) {
	workers := prewarmWorkers()
	if workers == 0 || b == nil || bc.chainConfig == nil {
		return
	}
	body, ok := b.Body().(*block.Body)
	if !ok || !body.IsFrameBody() {
		return
	}
	hdr, ok := b.Header().(*block.Header)
	if !ok || hdr == nil || hdr.Number == nil {
		return
	}
	h := b.Hash()
	js := &bc.senderJobs
	js.mu.Lock()
	if _, dup := js.m[h]; dup {
		js.mu.Unlock()
		return
	}
	js.mu.Unlock()
	go func() {
		if err := bc.ResolveFrames(b); err != nil {
			return
		}
		txs := b.Transactions()
		if len(txs) >= senderRecoveryMinTxs {
			// The execution's first pass hashes every transaction (its
			// message and receipt); hashed now, off the import, that pass
			// reads the memo (P10).
			prewarmTxHashes(txs, workers)
		}
		if len(txs) < senderRecoveryMinTxs || bc.sendersDoneFor(h) {
			// Resolution attached every sender: nothing to recover.
			return
		}
		j := bc.newSenderJob(transaction.MakeSignerWithTimestamp(bc.chainConfig, hdr.Number.ToBig(), hdr.Time),
			txs, FrameSpansOf(body, len(txs)))
		js.mu.Lock()
		if js.m == nil {
			js.m = make(map[types.Hash]*senderJob)
		}
		if _, dup := js.m[h]; dup {
			js.mu.Unlock()
			return
		}
		if len(js.m) >= senderJobCap {
			for k := range js.m {
				delete(js.m, k)
				break
			}
		}
		js.m[h] = j
		js.mu.Unlock()
		// Once the background workers are done the job holds nothing the
		// import needs (the memos are on the transactions): forget it, so a
		// block imported without joining it is not retained here.
		j.spawn(workers, func() {
			if j.complete() {
				bc.markSendersDone(h, j.txs, j.signer, true)
			}
			bc.forgetSenderJob(h, j)
		})
	}()
}

// txHashChunk is how many transactions a hash prewarm worker takes at a
// time.
const txHashChunk = 512

// prewarmTxHashes computes (memoises) every transaction's hash on workers
// background goroutines and returns at once.
func prewarmTxHashes(txs []*transaction.Transaction, workers int) {
	if workers < 1 {
		workers = 1
	}
	var next atomic.Int64
	n := int64(len(txs))
	for i := 0; i < workers; i++ {
		go func() {
			for {
				s := next.Add(txHashChunk) - txHashChunk
				if s >= n {
					return
				}
				e := s + txHashChunk
				if e > n {
					e = n
				}
				for _, tx := range txs[s:e] {
					if tx != nil {
						tx.Hash()
					}
				}
			}
		}()
	}
}

// takeSenderJob removes and returns blk's job when it recovers txs under
// signer (the import's own transactions and rules), else nil.
func (bc *BlockChain) takeSenderJob(h types.Hash, signer transaction.Signer, txs []*transaction.Transaction) *senderJob {
	js := &bc.senderJobs
	js.mu.Lock()
	j := js.m[h]
	delete(js.m, h)
	js.mu.Unlock()
	if j == nil || signer == nil || !j.signer.Equal(signer) || len(j.txs) != len(txs) ||
		(len(txs) > 0 && j.txs[0] != txs[0]) {
		return nil
	}
	js.hits.Add(1)
	return j
}

// dropSenderJob forgets h's job (the block left the executor unimported).
func (bc *BlockChain) dropSenderJob(h types.Hash) {
	js := &bc.senderJobs
	js.mu.Lock()
	delete(js.m, h)
	js.mu.Unlock()
}

// forgetSenderJob drops h's job if it is still j.
func (bc *BlockChain) forgetSenderJob(h types.Hash, j *senderJob) {
	js := &bc.senderJobs
	js.mu.Lock()
	if js.m[h] == j {
		delete(js.m, h)
	}
	js.mu.Unlock()
}

// SenderJobHits returns how many imports joined a block's sender job.
func (bc *BlockChain) SenderJobHits() uint64 { return bc.senderJobs.hits.Load() }

// newSenderJob returns a job over txs; with spans it works frame by frame
// against the frame store and the attestation source.
func (bc *BlockChain) newSenderJob(signer transaction.Signer, txs []*transaction.Transaction, spans []FrameSpan) *senderJob {
	j := &senderJob{signer: signer, txs: txs, start: time.Now(), spans: spans}
	if spans != nil && bc != nil {
		j.st = bc.frames.store
		j.src = bc.frameSenderSource()
	}
	return j
}

// recoverBlockSendersFor recovers txs' senders for the import of block h:
// it joins the block's job when one runs, else recovers them itself --
// frame by frame through the store's claims when spans are given (P8a), so
// a frame admission or another block is recovering is waited for, not
// recovered twice.
func (bc *BlockChain) recoverBlockSendersFor(h types.Hash, signer transaction.Signer, txs []*transaction.Transaction, spans []FrameSpan) {
	if bc != nil {
		if done, _ := bc.takeSendersDone(h, signer, txs); spans != nil && done {
			// Resolution or the block's job already settled every frame.
			bc.dropSenderJob(h)
			return
		}
		if j := bc.takeSenderJob(h, signer, txs); j != nil {
			workers := senderRecoveryFanout()
			if workers > len(txs)/senderJobChunk+1 {
				workers = len(txs)/senderJobChunk + 1
			}
			j.join(workers)
			return
		}
		if spans != nil && signer != nil && len(txs) >= senderRecoveryMinTxs {
			workers := senderRecoveryFanout()
			if workers > len(spans) {
				workers = len(spans)
			}
			bc.newSenderJob(signer, txs, spans).join(workers)
			return
		}
	}
	recoverBlockSenders(signer, txs)
}
