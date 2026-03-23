// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package store

import (
	"context"
	"sync"
	"time"

	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/kv"
)

// PooledDBStore implements jmt.NodeStore by holding a long-lived read-only
// MDBX transaction, avoiding the overhead of opening a new transaction per
// Get() call (as LazyDBStore does).
//
// After each block's persistence stage writes new JMT nodes via FlushTo(MDBXStore),
// call RefreshTx() to close the stale RO tx and open a fresh one that sees
// the latest data.
//
// Writes (Put/Delete) are no-ops — mutations go through tree.FlushTo(MDBXStore).
//
// Thread safety: Get/Has are called from the JMT tree goroutine. RefreshTx/Close
// may be called from a different goroutine (e.g., after block persistence).
// The mutex protects against this cross-goroutine access.
type PooledDBStore struct {
	db     kv.RoDB
	table  string
	ctx    context.Context
	mu     sync.Mutex
	tx     kv.Tx
	txAge  time.Time
	maxAge time.Duration
}

// NewPooledDBStore creates a store with a long-lived RO transaction.
func NewPooledDBStore(ctx context.Context, db kv.RoDB, table string) *PooledDBStore {
	s := &PooledDBStore{
		db:     db,
		table:  table,
		ctx:    ctx,
		maxAge: time.Second,
	}
	if err := s.ensureTx(); err != nil {
		// Log but don't fail — ensureTx will be retried on first Get/Has.
		// This allows the store to be created before the DB is fully ready.
		_ = err
	}
	return s
}

func (s *PooledDBStore) ensureTx() error {
	if s.tx != nil {
		return nil
	}
	tx, err := s.db.BeginRo(s.ctx)
	if err != nil {
		return err
	}
	s.tx = tx
	s.txAge = time.Now()
	return nil
}

// refreshIfStale closes a stale RO tx and opens a fresh one.
// Must be called with mu held.
func (s *PooledDBStore) refreshIfStale() error {
	if s.tx != nil && time.Since(s.txAge) > s.maxAge {
		s.tx.Rollback()
		s.tx = nil
	}
	return s.ensureTx()
}

func (s *PooledDBStore) Get(hash jmt.Hash) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.refreshIfStale(); err != nil {
		return nil, err
	}

	data, err := s.tx.GetOne(s.table, hash[:])
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, jmt.ErrNotFound
	}
	// Copy out of MDBX mmap buffer.
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (s *PooledDBStore) Put(jmt.Hash, []byte) error {
	return nil // writes go through FlushTo(MDBXStore)
}

func (s *PooledDBStore) Delete(jmt.Hash) error {
	return nil // deletes go through FlushTo(MDBXStore)
}

func (s *PooledDBStore) Has(hash jmt.Hash) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.refreshIfStale(); err != nil {
		return false, err
	}

	data, err := s.tx.GetOne(s.table, hash[:])
	if err != nil {
		return false, err
	}
	return data != nil, nil
}

// RefreshTx closes the current RO transaction and opens a fresh one.
// Call this after each block's persistence stage commits new JMT nodes.
func (s *PooledDBStore) RefreshTx() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
	_ = s.ensureTx()
}

// Close releases the held transaction.
func (s *PooledDBStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.tx != nil {
		s.tx.Rollback()
		s.tx = nil
	}
}

// Compile-time check.
var _ jmt.NodeStore = (*PooledDBStore)(nil)
