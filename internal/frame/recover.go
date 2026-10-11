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

package frame

import (
	"context"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/store"
)

// Sender recovery at admission (docs/QS_MULTI_MILLION_DESIGN.md, P2a item
// a). A frame waits in the store for seconds before a block takes it; S90
// measured sender recovery at 344 ms of a 1,082 ms follower import. The
// recoverer moves that work to admission: every frame the store admits
// (FRM1 push, FRQ1 pull, the leader's local frames) is queued, a bounded
// worker pool decodes it and recovers each transaction's sender, and the
// result is cached with the frame (store.SetSenders). Build and import
// attach the cached senders to the decoded transactions (ApplySenders) and
// recover only what is missing. The cache is a per-object memo keyed by the
// signer it was recovered under, so a block whose signer differs simply
// re-derives: nothing here can change an execution verdict.

// EnvRecoverWorkers sizes the admission recovery pool (default 8).
const EnvRecoverWorkers = "N42_FRAME_RECOVER_WORKERS"

// DefaultRecoverWorkers is the default admission recovery pool size.
const DefaultRecoverWorkers = 8

// recoverQueueCap bounds the frames waiting for recovery. A frame that does
// not fit is not recovered here; its block recovers it at import.
const recoverQueueCap = 1 << 16

// RecoverWorkersFromEnv reads N42_FRAME_RECOVER_WORKERS.
func RecoverWorkersFromEnv() int {
	if v, err := strconv.Atoi(os.Getenv(EnvRecoverWorkers)); err == nil && v > 0 {
		return v
	}
	return DefaultRecoverWorkers
}

type recoverJob struct {
	h   types.Hash
	raw []byte
}

// SenderRecoverer is the admission recovery pool. It implements
// store.Recoverer.
type SenderRecoverer struct {
	st     *store.Store
	signer func() transaction.Signer
	jobs   chan recoverJob

	pending                 atomic.Int64
	frames, txs, failed     atomic.Uint64
	dropped, gone, decodeKO atomic.Uint64
	skipped                 atomic.Uint64

	startOnce, stopOnce sync.Once
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	workers             int
}

// RecoverStats is a snapshot of the recoverer's counters.
type RecoverStats struct {
	Pending int64  // queued or being recovered
	Frames  uint64 // frames recovered and cached
	Txs     uint64 // senders recovered
	Failed  uint64 // transactions whose signature did not recover
	Dropped uint64 // frames not queued (queue full); recovered at import
	Gone    uint64 // frames that left the store before their recovery finished
	Skipped uint64 // frames a block claimed (or already cached) before their turn
}

// NewSenderRecoverer returns a pool of workers (<= 0 selects the default)
// recovering senders under signer() (called per frame; nil result skips the
// frame) into st.
func NewSenderRecoverer(st *store.Store, signer func() transaction.Signer, workers int) *SenderRecoverer {
	if workers <= 0 {
		workers = DefaultRecoverWorkers
	}
	return &SenderRecoverer{st: st, signer: signer, jobs: make(chan recoverJob, recoverQueueCap), workers: workers}
}

// Start launches the workers and attaches the recoverer to the store.
func (r *SenderRecoverer) Start(ctx context.Context) {
	r.startOnce.Do(func() {
		ctx, r.cancel = context.WithCancel(ctx)
		for i := 0; i < r.workers; i++ {
			r.wg.Add(1)
			go r.run(ctx)
		}
		r.st.SetRecoverer(r)
	})
}

// Stop detaches the recoverer and waits for the workers.
func (r *SenderRecoverer) Stop() {
	r.stopOnce.Do(func() {
		r.st.SetRecoverer(nil)
		if r.cancel != nil {
			r.cancel()
		}
		r.wg.Wait()
	})
}

// Submit queues frame h for recovery. It never blocks.
func (r *SenderRecoverer) Submit(h types.Hash, raw []byte) {
	r.pending.Add(1)
	select {
	case r.jobs <- recoverJob{h: h, raw: raw}:
	default:
		r.pending.Add(-1)
		r.dropped.Add(1)
	}
}

// Pending returns the frames queued or being recovered.
func (r *SenderRecoverer) Pending() int64 { return r.pending.Load() }

// Recovered returns the frames recovered and cached since start.
func (r *SenderRecoverer) Recovered() uint64 { return r.frames.Load() }

// Stats returns a snapshot of the counters.
func (r *SenderRecoverer) Stats() RecoverStats {
	return RecoverStats{Pending: r.pending.Load(), Frames: r.frames.Load(), Txs: r.txs.Load(),
		Failed: r.failed.Load(), Dropped: r.dropped.Load(), Gone: r.gone.Load(), Skipped: r.skipped.Load()}
}

func (r *SenderRecoverer) run(ctx context.Context) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-r.jobs:
			r.recoverOne(j)
			r.pending.Add(-1)
		}
	}
}

func (r *SenderRecoverer) recoverOne(j recoverJob) {
	defer func() { _ = recover() }() // a bad frame costs its own recovery, never the worker
	if !r.st.Has(j.h) {
		r.gone.Add(1)
		return
	}
	// A block already resolved it (its import's sender job recovers it), or
	// its senders are cached: recovering it here would be the same work twice
	// (P7: on S99 the queue ran 8-14k frames behind and kept re-recovering
	// frames the imports had already recovered).
	if !r.st.NeedsSenders(j.h) {
		r.skipped.Add(1)
		return
	}
	signer := r.signer()
	if signer == nil {
		return
	}
	ss, failed, err := RecoverSenders(signer, j.raw)
	if err != nil {
		r.decodeKO.Add(1)
		return
	}
	r.failed.Add(uint64(failed))
	if !r.st.SetSenders(j.h, ss) {
		r.gone.Add(1)
		return
	}
	r.frames.Add(1)
	r.txs.Add(uint64(len(ss.Addrs) - failed))
}

// RecoverSenders decodes one frame and recovers its transactions' senders
// under signer, aligned with DecodeTxs. It returns how many did not recover
// (zero address in the result).
func RecoverSenders(signer transaction.Signer, raw []byte) (*store.Senders, int, error) {
	txs, _, err := DecodeTxs(raw)
	if err != nil {
		return nil, 0, err
	}
	ss := &store.Senders{Signer: signer, Addrs: make([]types.Address, len(txs))}
	failed := 0
	for i, tx := range txs {
		if tx == nil {
			failed++
			continue
		}
		addr, err := signer.Sender(tx)
		if err != nil {
			failed++
			continue
		}
		ss.Addrs[i] = addr
	}
	return ss, failed, nil
}

// ApplySenders attaches cached senders to a frame's decoded transactions
// (txs must be that frame's DecodeTxs output) and returns how many it
// attached. A nil or misaligned cache attaches nothing.
func ApplySenders(txs []*transaction.Transaction, ss *store.Senders) int {
	if ss == nil || ss.Signer == nil || len(ss.Addrs) != len(txs) {
		return 0
	}
	n := 0
	for i, tx := range txs {
		if tx == nil || ss.Addrs[i] == (types.Address{}) {
			continue
		}
		tx.SetSenderMemo(ss.Signer, ss.Addrs[i])
		n++
	}
	return n
}
