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

// senderJob recovers one block's senders.
type senderJob struct {
	signer transaction.Signer
	txs    []*transaction.Transaction
	next   atomic.Int64
	start  time.Time

	// The background workers: bgDone closes when the last one exits.
	bgLeft atomic.Int32
	bgDone chan struct{}
}

func (j *senderJob) run() {
	// A panic costs only this worker's remaining chunks: execution recovers
	// whatever is left inline, as it would without the job.
	defer func() { _ = recover() }()
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
}

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
		if len(txs) < senderRecoveryMinTxs {
			return
		}
		j := &senderJob{signer: transaction.MakeSignerWithTimestamp(bc.chainConfig, hdr.Number.ToBig(), hdr.Time),
			txs: txs, start: time.Now()}
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
		j.spawn(workers, func() { bc.forgetSenderJob(h, j) })
	}()
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

// recoverBlockSendersFor recovers txs' senders for the import of block h:
// it joins the block's job when one runs, else recovers them itself.
func (bc *BlockChain) recoverBlockSendersFor(h types.Hash, signer transaction.Signer, txs []*transaction.Transaction) {
	if bc != nil {
		if j := bc.takeSenderJob(h, signer, txs); j != nil {
			workers := senderRecoveryFanout()
			if workers > len(txs)/senderJobChunk+1 {
				workers = len(txs)/senderJobChunk + 1
			}
			j.join(workers)
			return
		}
	}
	recoverBlockSenders(signer, txs)
}
