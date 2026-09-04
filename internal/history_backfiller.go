// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"sync"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/changeset"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
)

// HistoryBackfiller builds the AccountHistory/StorageHistory inverted index
// OUTSIDE the block-commit transaction, by reading the changesets back.
//
// Why this can exist at all: the index is a pure function of the changesets,
// and the changesets are already durable when the block commits. So nothing has
// to be handed out of the commit path — the backfiller re-reads rows that are
// on disk. That also makes it crash-safe by construction: on restart it resumes
// from the marker, and the marker is written in the same transaction as the
// index rows it describes.
//
// Why it might be worth it: measured on this fleet, writing the index inline
// costs 130 ms of its own phase AND 266 ms of mdbx_txn_commit paying for the
// pages those scattered rows dirty — 391 ms of a 665 ms write path. Off the
// commit path that cost leaves the block's critical section. Whether it leaves
// the CHAIN's critical path is the question this component exists to measure:
// the work still has to happen, and a backfiller competing for the same cores
// and the same MDBX write lock may give back what it saved.
//
// The interlock: while the marker is behind the head, historical queries above
// it must be REFUSED. An index that is known to be behind is safe; one that is
// silently behind is the failure mode this whole design exists to prevent,
// because HistoricalStateReader reads a missing entry as "untouched" and falls
// back to the CURRENT value.
type HistoryBackfiller struct {
	db       kv.RwDB
	head     func() uint64
	batch    uint64
	interval time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// started guards Stop: waiting on `done` when loop() was never launched
	// blocks for the full timeout. A node that builds a backfiller and returns
	// on an earlier startup error would pay that on every shutdown, and the
	// tests paid 10 s each until this existed.
	started bool
	mu      sync.Mutex
}

// NewHistoryBackfiller builds a backfiller. head must return the current chain
// height; batch caps how many blocks one flush covers, so a single transaction
// cannot grow without bound on a node that has fallen far behind.
func NewHistoryBackfiller(db kv.RwDB, head func() uint64, batch uint64, interval time.Duration) *HistoryBackfiller {
	if batch == 0 {
		batch = 256
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &HistoryBackfiller{db: db, head: head, batch: batch, interval: interval,
		ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (b *HistoryBackfiller) Start() {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return
	}
	b.started = true
	b.mu.Unlock()
	go b.loop()
}

func (b *HistoryBackfiller) Stop() {
	b.mu.Lock()
	started := b.started
	b.mu.Unlock()
	b.cancel()
	if !started {
		return // nothing to wait for; see the comment on `started`
	}
	select {
	case <-b.done:
	case <-time.After(10 * time.Second):
		log.Warn("history backfiller did not stop within 10s")
	}
}

// IndexedThrough reports the highest block covered by the index, and whether
// the marker exists at all. A missing marker means nothing is covered — the
// caller must not read that as "everything".
func (b *HistoryBackfiller) IndexedThrough() (uint64, bool) {
	var n uint64
	var ok bool
	if err := b.db.View(b.ctx, func(tx kv.Tx) error {
		var err error
		n, ok, err = rawdb.ReadHistoryIndexedThrough(tx)
		return err
	}); err != nil {
		return 0, false
	}
	return n, ok
}

func (b *HistoryBackfiller) loop() {
	defer close(b.done)
	t := time.NewTicker(b.interval)
	defer t.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-t.C:
			if err := b.step(); err != nil {
				log.Warn("history backfill step failed; will retry", "err", err)
			}
		}
	}
}

// step folds one batch of changesets into the index. Returns nil when there is
// nothing to do.
func (b *HistoryBackfiller) step() error {
	from, _, err := b.readMarker()
	if err != nil {
		return err
	}
	head := b.head()
	if head == 0 || head <= from {
		return nil
	}
	to := from + b.batch
	if to > head {
		to = head
	}

	agg := state.NewHistoryAggregator()
	if err := b.db.View(b.ctx, func(tx kv.Tx) error {
		for _, spec := range []struct{ cs, hist string }{
			{modules.AccountChangeSet, modules.AccountsHistory},
			{modules.StorageChangeSet, modules.StorageHistory},
		} {
			start := modules.EncodeBlockNumber(from + 1)
			if err := changeset.ForEach(tx, spec.cs, start, func(blockN uint64, k, _ []byte) error {
				// ForEach walks to the end of the table; stop at the batch edge
				// rather than folding blocks the marker will not cover.
				if blockN > to {
					return errStopWalk
				}
				if blockN <= from {
					return nil
				}
				agg.AddKey(spec.hist, k, blockN)
				return nil
			}); err != nil && err != errStopWalk {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}

	// Index rows and the marker go in ONE transaction. A crash may lose both
	// (the range is simply rebuilt) but can never leave the marker ahead of the
	// rows, which is the case that would let a query read a gap as untouched.
	return b.db.Update(b.ctx, func(tx kv.RwTx) error {
		if err := agg.Flush(tx); err != nil {
			return err
		}
		return rawdb.WriteHistoryIndexedThrough(tx, to)
	})
}

func (b *HistoryBackfiller) readMarker() (uint64, bool, error) {
	var n uint64
	var ok bool
	err := b.db.View(b.ctx, func(tx kv.Tx) error {
		var e error
		n, ok, e = rawdb.ReadHistoryIndexedThrough(tx)
		return e
	})
	return n, ok, err
}

// errStopWalk ends a ForEach early without being an error to the caller.
var errStopWalk = stopWalk{}

type stopWalk struct{}

func (stopWalk) Error() string { return "stop walk" }
