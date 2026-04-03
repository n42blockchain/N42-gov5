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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	prometheus "github.com/n42blockchain/N42/common/metrics"
	"github.com/n42blockchain/N42/log"
)

var freezerFrozenBlocks = prometheus.GetOrCreateCounter("freezer_frozen_blocks", true)

const (
	// Default number of blocks behind chain head to start freezing.
	DefaultFreezeThreshold uint64 = 90_000

	// Minimum interval between freeze cycles.
	freezeInterval = 30 * time.Second

	// Maximum blocks to freeze in one batch.
	freezeBatchSize = 30_000
)

// Table names for frozen block data.
// Geth-compatible tables use .cidx/.cdat for variable-size data
// and .ridx/.rdat for fixed-size data.
const (
	TableHeaders    = "headers"    // blockNum → header RLP (cidx/cdat)
	TableBodies     = "bodies"     // blockNum → body RLP (cidx/cdat)
	TableReceipts   = "receipts"   // blockNum → receipts RLP (cidx/cdat)
	TableHashes     = "hashes"     // blockNum → canonical hash 32B (ridx/rdat)
	TableDifficulty = "diffs"      // blockNum → total difficulty (ridx/rdat)

	// Extended tables for ETH EL execution.
	TableSenders        = "senders"         // blockNum → concatenated sender addresses 20B×N (cidx/cdat)
	TableAccountChanges = "account_changes" // blockNum → account changeset (cidx/cdat)
	TableStorageChanges = "storage_changes" // blockNum → storage changeset (cidx/cdat)
)

// tableSpec defines how a table should be opened.
type tableSpec struct {
	name string
	ext  string // "c" or "r"
}

// coreTableSpecs are the Geth-compatible tables (always opened).
var coreTableSpecs = []tableSpec{
	{TableHeaders, "c"},
	{TableBodies, "c"},
	{TableReceipts, "c"},
	{TableHashes, "r"},
	{TableDifficulty, "r"},
}

// extendedTableSpecs are ETH EL execution tables (opened when present or on first write).
var extendedTableSpecs = []tableSpec{
	{TableSenders, "c"},
	{TableAccountChanges, "c"},
	{TableStorageChanges, "c"},
}

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
// All files live in a single flat directory (Geth-compatible layout).
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
// It opens all core tables (headers, bodies, receipts, hashes, diffs).
// Extended tables (senders, changesets) are opened if their index files exist.
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

	// Open core tables (must exist or be created).
	for _, spec := range coreTableSpecs {
		t, err := NewFreezerTable(path, spec.name, spec.ext)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("freezer: open table %s: %w", spec.name, err)
		}
		f.tables[spec.name] = t
	}

	// Open extended tables only if their index files already exist.
	for _, spec := range extendedTableSpecs {
		idxPath := filepath.Join(path, fmt.Sprintf("%s.%sidx", spec.name, spec.ext))
		if _, err := os.Stat(idxPath); err == nil {
			t, err := NewFreezerTable(path, spec.name, spec.ext)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("freezer: open table %s: %w", spec.name, err)
			}
			f.tables[spec.name] = t
		}
	}

	// Recover frozen count from the minimum of core table item counts.
	var items uint64
	var initialized bool
	for _, spec := range coreTableSpecs {
		t := f.tables[spec.name]
		count := t.Items()
		if !initialized || count < items {
			items = count
			initialized = true
		}
	}
	if !initialized {
		items = 0
	}

	// Truncate core tables to the minimum count for consistency.
	for _, spec := range coreTableSpecs {
		t := f.tables[spec.name]
		if t.Items() > items {
			log.Warn("Freezer table count mismatch, truncating",
				"table", spec.name, "items", t.Items(), "target", items)
			if err := t.TruncateHead(items); err != nil {
				f.Close()
				return nil, err
			}
		}
	}
	f.frozen.Store(items)
	freezerFrozenBlocks.Set(items)

	log.Info("Freezer opened", "path", path, "frozen", items)
	return f, nil
}

// Table returns a named table for direct access. Returns nil if not found.
func (f *Freezer) Table(name string) *FreezerTable {
	return f.tables[name]
}

// EnsureTable opens or creates an extended table that may not exist yet.
func (f *Freezer) EnsureTable(name, ext string) (*FreezerTable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if t, ok := f.tables[name]; ok {
		return t, nil
	}
	t, err := NewFreezerTable(f.path, name, ext)
	if err != nil {
		return nil, err
	}
	f.tables[name] = t
	return t, nil
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
// Returns ErrPruned if the data has been pruned (not an error, expected).
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

	for _, t := range f.tables {
		if err := t.Sync(); err != nil {
			return fmt.Errorf("freezer: sync: %w", err)
		}
	}

	f.frozen.Add(count)
	freezerFrozenBlocks.Set(f.frozen.Load())
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
