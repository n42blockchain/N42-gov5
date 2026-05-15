// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Freezer orchestrator for the geth-compatible ancient store.
// Defines DefaultFreezeThreshold (90k behind head), freezeInterval
// and freezeBatchSize plus Table* names (headers/bodies/receipts
// plus N42 extensions: senders, acctcs, storcs, leaves, witness).
// BatchSize (64) is the unified batch size for online executor,
// sender-recovery and offline compact paths. EncodeBatch builds
// the length-prefixed, zstd-compressed batch payload.

package freezer

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klauspost/compress/zstd"

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
	TableHeaders    = "headers"  // blockNum → header RLP (cidx/cdat)
	TableBodies     = "bodies"   // blockNum → body RLP (cidx/cdat)
	TableReceipts   = "receipts" // blockNum → receipts RLP (cidx/cdat)
	TableHashes     = "hashes"   // blockNum → canonical hash 32B (ridx/rdat)
	TableDifficulty = "diffs"    // blockNum → total difficulty (ridx/rdat)

	// Extended tables for ETH EL execution.
	TableSenders        = "senders" // blockNum → concatenated sender addresses 20B×N
	TableAccountChanges = "acctcs"  // blockNum → account changeset
	TableStorageChanges = "storcs"  // blockNum → storage changeset
	TableLeavesJournal  = "leaves"  // blockNum → trie leaf changes per block
	TableBlockWitness   = "witness" // blockNum → minimal state access set for replay
	TableWipes          = "wipes"   // blockNum → SELFDESTRUCT pre-wipe entries (sidecar to fill witness-replay's storcs gaps)
)

// BatchSize is the unified batch size for all freezer tables.
// Used by executor (online), sender-recovery, and compact (offline).
// 64 entries per batch ≈ 200-400KB compressed, random read ~0.3ms.
const BatchSize = 64

// EncodeBatch builds a length-prefixed batch and compresses it.
// Format: [len0:4LE][data0][len1:4LE][data1]...
// Returns compressed data if smaller, otherwise raw.
func EncodeBatch(entries [][]byte, enc *zstd.Encoder) []byte {
	rawSize := 0
	for _, e := range entries {
		rawSize += 4 + len(e)
	}
	batch := make([]byte, 0, rawSize)
	for _, e := range entries {
		var lb [4]byte
		binary.LittleEndian.PutUint32(lb[:], uint32(len(e)))
		batch = append(batch, lb[:]...)
		batch = append(batch, e...)
	}
	if len(batch) == 0 || enc == nil {
		return batch
	}
	comp := enc.EncodeAll(batch, make([]byte, 0, len(batch)/2))
	if len(comp) < len(batch) {
		return comp
	}
	return batch
}

// WriteBatch writes a batch to a FreezerTable: one blob, N cidx entries sharing same offset.
// Enables batch read mode on the table.
func WriteBatch(tbl *FreezerTable, entries [][]byte, encoded []byte) error {
	tbl.setBatchSize(BatchSize)
	return tbl.AppendBatchBlob(tbl.Items(), len(entries), encoded)
}

// tableSpec defines how a table should be opened.
type tableSpec struct {
	name string
	ext  string // "c" or "r"
}

// coreTableSpecs are the Geth-compatible tables (always opened).
// Receipts is NOT included here — it lives in extendedTableSpecs because
// the output freezer writes it in batch-compressed format. Including it
// in core would cause New() to truncate it to 0 when the other core
// tables (headers, bodies, …) are empty.
// coreTableSpecs: empty for ethexec tools.
// Headers/bodies are produced by compact tools in columnar format —
// freezer.New must NOT open/truncate them.
// Geth input freezer opens headers/bodies via extendedTableSpecs on-demand.
var coreTableSpecs = []tableSpec{}

// extendedTableSpecs are ETH EL execution tables (opened when present or on first write).
// Opened with NewFreezerTableCompressed for batch-mode auto-detection.
var extendedTableSpecs = []tableSpec{
	{TableHeaders, "c"},
	{TableBodies, "c"},
	{TableReceipts, "c"},
	{TableHashes, "r"},
	{TableDifficulty, "r"},
	{TableSenders, "c"},
	{TableAccountChanges, "c"},
	{TableStorageChanges, "c"},
	{TableLeavesJournal, "c"},
	{TableBlockWitness, "c"},
	{TableWipes, "c"},
}

var canonicalFrozenTables = []string{
	TableHeaders,
	TableBodies,
	TableReceipts,
	TableHashes,
	TableDifficulty,
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

	readonly bool // set by openFreezer; gates EnsureTable and friends

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// IsReadOnly reports whether this Freezer was opened read-only.
func (f *Freezer) IsReadOnly() bool { return f.readonly }

// New creates or opens a Freezer at the given path.
// It opens all core tables (headers, bodies, receipts, hashes, diffs).
// Extended tables (senders, changesets) are opened if their index files exist.
// New opens a Freezer for read-write access. SAFETY: if the path looks
// like a geth ancient (see IsLikelyGethAncient — typical markers: a
// `geth/chaindata/ancient` path component AND a hashes.ridx file from
// geth's per-block hash table), this function silently downgrades to
// read-only and logs a Warn. Geth's ancient is owned by geth; allowing
// outside processes to open it RW destroyed bodies.0000.cdat once
// (2026-05-05). The downgrade is the cheapest safety net — callers
// that genuinely need RW on a geth-style layout must explicitly use
// New(...).WithoutAncientGuard() (intentionally absent — there's no
// such API; the answer is "copy the data first").
func New(path string, threshold uint64) (*Freezer, error) {
	if IsLikelyGethAncient(path) {
		log.Warn("freezer.New: path looks like geth ancient — opening read-only to protect data",
			"path", path)
		return openFreezer(path, threshold, true)
	}
	return openFreezer(path, threshold, false)
}

// NewReadOnly opens an existing Freezer without modifying any files.
// All tables are opened O_RDONLY; partial-index truncation, file creation,
// and core-table alignment are skipped. Mutating operations on the returned
// Freezer (Append, TruncateHead, etc.) return errors.
func NewReadOnly(path string) (*Freezer, error) {
	return openFreezer(path, DefaultFreezeThreshold, true)
}

// IsLikelyGethAncient heuristically checks whether path is a geth-managed
// ancient directory that outside processes must NOT mutate. Two markers:
//
//  1. The path contains a `geth` component — typical layouts look like
//     `<datadir>/geth/chaindata/ancient/chain`.
//  2. The directory has `hashes.ridx`, geth's per-block canonical hash
//     index. N42 doesn't write this file; only geth does.
//
// Both must hold. The double-check guards against false positives from
// users who happen to name an N42-only freezer dir "geth" by accident.
func IsLikelyGethAncient(path string) bool {
	abs := strings.ReplaceAll(strings.ToLower(path), "\\", "/")
	if !strings.Contains(abs, "/geth/") && !strings.HasSuffix(abs, "/geth") {
		return false
	}
	_, err := os.Stat(filepath.Join(path, "hashes.ridx"))
	return err == nil
}

func openFreezer(path string, threshold uint64, readonly bool) (*Freezer, error) {
	if !readonly {
		if err := os.MkdirAll(path, 0755); err != nil {
			return nil, fmt.Errorf("freezer: mkdir: %w", err)
		}
	}

	if threshold == 0 {
		threshold = DefaultFreezeThreshold
	}

	f := &Freezer{
		path:      path,
		readonly:  readonly,
		tables:    make(map[string]*FreezerTable),
		threshold: threshold,
	}

	// Open core tables. In readonly mode, only open if the cidx exists.
	for _, spec := range coreTableSpecs {
		idxPath := filepath.Join(path, fmt.Sprintf("%s.%sidx", spec.name, spec.ext))
		if readonly {
			if _, err := os.Stat(idxPath); err != nil {
				continue // skip absent core table in RO mode
			}
			t, err := NewFreezerTableReadOnly(path, spec.name, spec.ext)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("freezer: open table %s: %w", spec.name, err)
			}
			f.tables[spec.name] = t
		} else {
			t, err := NewFreezerTable(path, spec.name, spec.ext)
			if err != nil {
				f.Close()
				return nil, fmt.Errorf("freezer: open table %s: %w", spec.name, err)
			}
			f.tables[spec.name] = t
		}
	}

	// Open extended tables (with compression support) if their index files exist.
	for _, spec := range extendedTableSpecs {
		idxPath := filepath.Join(path, fmt.Sprintf("%s.%sidx", spec.name, spec.ext))
		if _, err := os.Stat(idxPath); err != nil {
			continue
		}
		var t *FreezerTable
		var err error
		if readonly {
			t, err = NewFreezerTableCompressedReadOnly(path, spec.name, spec.ext)
		} else {
			t, err = NewFreezerTableCompressed(path, spec.name, spec.ext)
		}
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("freezer: open table %s: %w", spec.name, err)
		}
		f.tables[spec.name] = t
	}

	// Recover the canonical frozen height from the minimum count across the
	// standard block tables. Optional extended tables (senders, changesets,
	// witness, ...) may lag behind and must not reduce chain availability.
	//
	// If no canonical tables exist, fall back to the maximum count across all
	// opened tables so output-only freezers (e.g. receipts/senders generated by
	// offline tools) still report their populated height.
	var items uint64
	var initialized bool
	for _, name := range canonicalFrozenTables {
		t, ok := f.tables[name]
		if !ok {
			continue
		}
		count := t.Items()
		if count == 0 {
			continue
		}
		if !initialized || count < items {
			items = count
			initialized = true
		}
	}

	// Align tables that are AHEAD of the canonical minimum back down —
	// this indicates an interrupted truncate/append cycle that left an
	// extended table (acctcs, storcs, witness, ...) ahead of state.
	//
	// HARD RULE: only canonical tables (headers/bodies/receipts/hashes
	// /difficulty) participate. Senders, changesets, witness etc are
	// computed by separate stages and may legitimately run AHEAD of the
	// canonical height — truncating them is destructive and silently
	// destroys minutes-to-hours of pre-computed work. The user's 144 MB
	// senders.cidx was wiped to 24 KB by this loop on 2026-05-05 when
	// the canonical receipts had 4001 items but senders covered 24 M.
	//
	// Skip entirely in readonly mode: input freezers must NEVER be
	// mutated, regardless of any cross-table inconsistency.
	if initialized && !readonly {
		canonical := make(map[string]bool, len(canonicalFrozenTables))
		for _, n := range canonicalFrozenTables {
			canonical[n] = true
		}
		for name, t := range f.tables {
			if !canonical[name] {
				continue // never truncate extended tables here
			}
			if t.Items() > items {
				log.Warn("Freezer canonical table count mismatch, truncating",
					"table", name, "items", t.Items(), "target", items)
				if err := t.TruncateHead(items); err != nil {
					f.Close()
					return nil, err
				}
			}
		}
	} else if !initialized {
		for _, t := range f.tables {
			if c := t.Items(); c > items {
				items = c
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
// Read-only freezers fall through to NewFreezerTableReadOnly so the
// underlying files are not opened RW (and thus cannot be truncated).
func (f *Freezer) EnsureTable(name, ext string) (*FreezerTable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if t, ok := f.tables[name]; ok {
		return t, nil
	}
	var (
		t   *FreezerTable
		err error
	)
	if f.readonly {
		t, err = NewFreezerTableReadOnly(f.path, name, ext)
	} else {
		t, err = NewFreezerTable(f.path, name, ext)
	}
	if err != nil {
		return nil, err
	}
	f.tables[name] = t
	// Update frozen count if this table has more items.
	if items := t.Items(); items > f.frozen.Load() {
		f.frozen.Store(items)
	}
	return t, nil
}

// EnsureTableCompressed creates or returns a table with per-entry zstd compression.
// If the table already exists but was opened without compression (e.g. by New()
// as a core table), it is closed and re-opened as compressed so that batch-mode
// auto-detection and zstd decompression work correctly.
func (f *Freezer) EnsureTableCompressed(name, ext string) (*FreezerTable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if t, ok := f.tables[name]; ok {
		if t.compressed {
			return t, nil
		}
		// Table exists but was opened non-compressed. Flush buffered
		// writes, then close and reopen so that batch-mode auto-detect
		// and zstd codec are initialized.
		t.Sync()
		t.Close()
		delete(f.tables, name)
	}
	if f.readonly {
		t, err := NewFreezerTableCompressedReadOnly(f.path, name, ext)
		if err != nil {
			return nil, err
		}
		f.tables[name] = t
		if items := t.Items(); items > f.frozen.Load() {
			f.frozen.Store(items)
		}
		return t, nil
	}
	t, err := NewFreezerTableCompressed(f.path, name, ext)
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
		t := f.tables[td.name]
		if t == nil {
			// Receipts (and possibly others) may be in extendedTableSpecs,
			// not opened by New(). Lazily create the table.
			var err error
			t, err = NewFreezerTable(f.path, td.name, "c")
			if err != nil {
				return fmt.Errorf("freezer: create table %s: %w", td.name, err)
			}
			f.tables[td.name] = t
		}
		if err := t.AppendBatch(start, td.items); err != nil {
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
