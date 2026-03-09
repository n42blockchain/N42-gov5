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

package freezer

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/log"
)

const (
	// Default number of blocks behind chain head to start freezing.
	DefaultFreezeThreshold uint64 = 90_000

	// Minimum interval between freeze cycles.
	freezeInterval = 30 * time.Second

	// Maximum blocks to freeze in one batch.
	freezeBatchSize = 30_000

	// ancientMetaFile stores the number of frozen blocks persistently.
	ancientMetaFile = "frozen_blocks"
)

// Table names for frozen block data.
const (
	TableHeaders   = "headers"
	TableBodies    = "bodies"
	TableReceipts  = "receipts"
	TableHashes    = "hashes"
	TableDifficulty = "difficulty"
)

// FreezeFunc is called by the background freezer to retrieve block data
// from the hot database. It receives (start, count) and returns parallel
// slices of raw data for each table plus block hashes to delete from MDBX.
type FreezeFunc func(start, count uint64) (*FreezeData, error)

// FreezeData holds raw block data for a batch of sequential blocks.
type FreezeData struct {
	Headers    [][]byte
	Bodies     [][]byte
	Receipts   [][]byte
	Hashes     [][]byte // 32-byte canonical hash per block
	Difficulty [][]byte
}

// Freezer manages the immutable ancient chain data store.
type Freezer struct {
	path   string
	tables map[string]*FreezerTable

	frozen atomic.Uint64 // number of blocks frozen (block 0..frozen-1 are in freezer)

	threshold uint64 // blocks behind head before freezing

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New creates or opens a Freezer at the given path.
func New(path string, threshold uint64) (*Freezer, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("freezer: mkdir: %w", err)
	}

	if threshold == 0 {
		threshold = DefaultFreezeThreshold
	}

	f := &Freezer{
		path:      path,
		tables:    make(map[string]*FreezerTable),
		threshold: threshold,
	}

	// Open all tables.
	for _, name := range []string{TableHeaders, TableBodies, TableReceipts, TableHashes, TableDifficulty} {
		t, err := NewFreezerTable(path, name)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("freezer: open table %s: %w", name, err)
		}
		f.tables[name] = t
	}

	// Load frozen block count from metadata file.
	f.frozen.Store(f.loadFrozenCount())

	// Verify table consistency — all tables should have same item count.
	items := f.frozen.Load()
	for name, t := range f.tables {
		if t.Items() != items {
			log.Warn("Freezer table item count mismatch, truncating to minimum",
				"table", name, "items", t.Items(), "frozen", items)
			if t.Items() > items {
				if err := t.TruncateHead(items); err != nil {
					f.Close()
					return nil, err
				}
			} else {
				// Table has fewer items, adjust frozen count down.
				items = t.Items()
			}
		}
	}
	if items != f.frozen.Load() {
		f.frozen.Store(items)
		f.saveFrozenCount(items)
	}

	log.Info("Freezer opened", "path", path, "frozen", items)
	return f, nil
}

// Frozen returns the number of frozen blocks.
func (f *Freezer) Frozen() uint64 {
	return f.frozen.Load()
}

// HasAncient checks if a block number is available in the freezer.
func (f *Freezer) HasAncient(number uint64) bool {
	return number < f.frozen.Load()
}

// Ancient retrieves an item from the specified table by block number.
func (f *Freezer) Ancient(table string, number uint64) ([]byte, error) {
	t, ok := f.tables[table]
	if !ok {
		return nil, fmt.Errorf("freezer: unknown table %q", table)
	}
	if number >= f.frozen.Load() {
		return nil, ErrOutOfBounds
	}
	return t.Retrieve(number)
}

// Freeze appends a batch of blocks to the freezer. All tables must receive
// the same number of items. The start number must equal Frozen().
func (f *Freezer) Freeze(start uint64, data *FreezeData) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if start != f.frozen.Load() {
		return fmt.Errorf("freezer: freeze out of order: want %d, got %d", f.frozen.Load(), start)
	}

	count := uint64(len(data.Headers))
	if uint64(len(data.Bodies)) != count ||
		uint64(len(data.Receipts)) != count ||
		uint64(len(data.Hashes)) != count ||
		uint64(len(data.Difficulty)) != count {
		return errors.New("freezer: mismatched data lengths")
	}

	// Append to each table.
	type tableData struct {
		name  string
		items [][]byte
	}
	batch := []tableData{
		{TableHeaders, data.Headers},
		{TableBodies, data.Bodies},
		{TableReceipts, data.Receipts},
		{TableHashes, data.Hashes},
		{TableDifficulty, data.Difficulty},
	}
	for _, td := range batch {
		if err := f.tables[td.name].AppendBatch(start, td.items); err != nil {
			return fmt.Errorf("freezer: append %s: %w", td.name, err)
		}
	}

	// Sync all tables.
	for _, t := range f.tables {
		if err := t.Sync(); err != nil {
			return fmt.Errorf("freezer: sync: %w", err)
		}
	}

	f.frozen.Add(count)
	f.saveFrozenCount(f.frozen.Load())

	return nil
}

// TruncateHead removes frozen blocks from `from` onwards (for chain reorgs).
func (f *Freezer) TruncateHead(from uint64) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if from >= f.frozen.Load() {
		return nil
	}

	for name, t := range f.tables {
		if err := t.TruncateHead(from); err != nil {
			return fmt.Errorf("freezer: truncate %s: %w", name, err)
		}
	}
	f.frozen.Store(from)
	f.saveFrozenCount(from)

	log.Warn("Freezer truncated", "from", from)
	return nil
}

// StartFreeze starts the background goroutine that periodically freezes
// blocks from the hot database.
func (f *Freezer) StartFreeze(ctx context.Context, headFn func() uint64, freezeFn FreezeFunc, cleanupFn func(start, count uint64) error) {
	ctx, f.cancel = context.WithCancel(ctx)

	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		f.freezeLoop(ctx, headFn, freezeFn, cleanupFn)
	}()
}

func (f *Freezer) freezeLoop(ctx context.Context, headFn func() uint64, freezeFn FreezeFunc, cleanupFn func(start, count uint64) error) {
	timer := time.NewTimer(freezeInterval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			f.doFreeze(headFn, freezeFn, cleanupFn)
			timer.Reset(freezeInterval)
		}
	}
}

func (f *Freezer) doFreeze(headFn func() uint64, freezeFn FreezeFunc, cleanupFn func(start, count uint64) error) {
	head := headFn()
	frozen := f.frozen.Load()

	// Only freeze blocks that are sufficiently behind head.
	if head <= f.threshold || head-f.threshold <= frozen {
		return
	}

	limit := head - f.threshold
	count := limit - frozen
	if count > freezeBatchSize {
		count = freezeBatchSize
	}

	data, err := freezeFn(frozen, count)
	if err != nil {
		log.Warn("Freezer: failed to read blocks for freezing", "start", frozen, "count", count, "err", err)
		return
	}

	if err := f.Freeze(frozen, data); err != nil {
		log.Warn("Freezer: failed to freeze blocks", "start", frozen, "count", count, "err", err)
		return
	}

	// Clean up frozen data from hot database.
	if cleanupFn != nil {
		if err := cleanupFn(frozen, count); err != nil {
			log.Warn("Freezer: failed to cleanup hot DB", "start", frozen, "count", count, "err", err)
		}
	}

	log.Info("Froze ancient blocks", "start", frozen, "count", count, "total", f.frozen.Load())
}

// Close stops the background freezer and closes all tables.
func (f *Freezer) Close() error {
	if f.cancel != nil {
		f.cancel()
	}
	f.wg.Wait()

	var errs []error
	for _, t := range f.tables {
		if err := t.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Sync flushes all tables to disk.
func (f *Freezer) Sync() error {
	for _, t := range f.tables {
		if err := t.Sync(); err != nil {
			return err
		}
	}
	return nil
}

func (f *Freezer) loadFrozenCount() uint64 {
	data, err := os.ReadFile(filepath.Join(f.path, ancientMetaFile))
	if err != nil {
		return 0
	}
	if len(data) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(data)
}

func (f *Freezer) saveFrozenCount(count uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], count)
	_ = os.WriteFile(filepath.Join(f.path, ancientMetaFile), buf[:], 0644)
}
