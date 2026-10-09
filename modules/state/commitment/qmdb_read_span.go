// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"context"

	"github.com/n42blockchain/N42/lib/kv"
)

// ReadSpan is a LockReaders span whose read transactions are opened under a
// read-transaction limiter permit taken BEFORE the tree's reader lock.
//
// The order matters. Out-of-band readers (the txpool's batch reads, RPC
// `latest`) hold a limiter permit through their own read transaction and
// then take the reader lock in Lookup. Once the owner queues for the writer
// side, every new reader lock waits behind it, so those readers sit on their
// permits. A span holder that takes the reader lock first and then waits for
// a permit (a Block-STM worker's BeginRo) closes the cycle: it waits for the
// readers' permits, the readers wait for the writer, the writer waits for the
// span. With the limiter full of ingest reads that is a fleet wedge (S86).
// Taking the permit first, and opening every transaction of the span with
// kv.BeginRoNested on it, leaves the span holder nothing to wait for while it
// holds the lock.
type ReadSpan struct {
	db      kv.RoDB
	release func()
	unlock  func()
}

// BeginReadSpan takes one read-transaction permit from db, then the tree's
// reader lock. Call End to release both.
func (r *QMDBRootComputer) BeginReadSpan(ctx context.Context, db kv.RoDB) (*ReadSpan, error) {
	release, err := kv.AcquireRoPermit(ctx, db)
	if err != nil {
		return nil, err
	}
	return &ReadSpan{db: db, release: release, unlock: r.LockReaders()}, nil
}

// BeginRo opens a read transaction for the span (any goroutine). It never
// waits for a limiter permit: the span's permit accounts for its readers.
func (s *ReadSpan) BeginRo(ctx context.Context) (kv.Tx, error) {
	return kv.BeginRoNested(ctx, s.db)
}

// End releases the reader lock, then the permit. The span's transactions
// must be closed first.
func (s *ReadSpan) End() {
	if s.unlock != nil {
		s.unlock()
		s.unlock = nil
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
}
