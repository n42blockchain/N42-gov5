// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// output_batcher.go — freezer append batcher for execution outputs.
//
// outputBatcher accumulates per-block receipts, senders, changesets and
// changeset and witness entries in memory and flushes them to the underlying
// freezer.Freezer in aligned batches so that segment files stay tightly
// packed. Tables that already contain entries for the current range are
// spot-checked on startup and skipped until nextItem passes the existing
// boundary, which makes a restart idempotent without rewriting data.

package ethel

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// outputBatcher accumulates per-block entries and writes batches to freezer.
// Tables that already contain data for the current range are skipped (with
// spot-check verification); writes only begin once we pass the existing
// item boundary.
type outputBatcher struct {
	freezer *freezer.Freezer
	enc     *zstd.Encoder
	tables  map[string]*tableBatch
	order   []string

	// nextItem is the block number of the *next* entry that will be added.
	nextItem uint64
}

type tableBatch struct {
	name    string
	ext     string
	entries [][]byte
	tbl     *freezer.FreezerTable

	// existingItems is the table's item count recorded at startup.
	existingItems uint64
	verified      bool
	warnCount     int
}

func newOutputBatcher(f *freezer.Freezer) (*outputBatcher, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression))
	if err != nil {
		return nil, err
	}
	return &outputBatcher{
		freezer: f,
		enc:     enc,
		tables:  make(map[string]*tableBatch),
	}, nil
}

func (b *outputBatcher) Close() {
	if b.enc != nil {
		b.enc.Close()
	}
}

// remainder returns the unflushed entries per table as a serialized map.
// Each value is a length-prefixed blob: [count:4LE][len0:4LE][data0]...
// Used to persist remainder in MDBX so ctrl+c doesn't lose entries.
func (b *outputBatcher) remainder() map[string][]byte {
	result := make(map[string][]byte)
	for _, name := range b.order {
		tb := b.tables[name]
		if len(tb.entries) == 0 {
			continue
		}
		// Serialize: [count:4LE][len0:4LE][data0][len1:4LE][data1]...
		size := 4
		for _, e := range tb.entries {
			size += 4 + len(e)
		}
		buf := make([]byte, 0, size)
		var tmp [4]byte
		binary.LittleEndian.PutUint32(tmp[:], uint32(len(tb.entries)))
		buf = append(buf, tmp[:]...)
		for _, e := range tb.entries {
			binary.LittleEndian.PutUint32(tmp[:], uint32(len(e)))
			buf = append(buf, tmp[:]...)
			buf = append(buf, e...)
		}
		result[name] = buf
	}
	return result
}

// preloadRemainder restores entries saved by a prior run's remainder().
// Called during alignOnResume before the executor starts processing.
func (b *outputBatcher) preloadRemainder(data map[string][]byte) {
	for name, blob := range data {
		tb := b.tables[name]
		if tb == nil {
			continue
		}
		if len(blob) < 4 {
			continue
		}
		count := int(binary.LittleEndian.Uint32(blob[:4]))
		pos := 4
		for i := 0; i < count && pos+4 <= len(blob); i++ {
			entryLen := int(binary.LittleEndian.Uint32(blob[pos:]))
			pos += 4
			if pos+entryLen > len(blob) {
				break
			}
			tb.entries = append(tb.entries, blob[pos:pos+entryLen])
			pos += entryLen
		}
		if len(tb.entries) > 0 {
			// Adjust existingItems and nextItem backward to account
			// for the pre-loaded entries.
			preloaded := uint64(len(tb.entries))
			if tb.existingItems >= preloaded {
				tb.existingItems -= preloaded
			}
			if b.nextItem > preloaded {
				newNext := b.nextItem - preloaded
				if newNext < b.nextItem {
					b.nextItem = newNext
				}
			}
			log.Info("Preloaded remainder entries",
				"table", name, "count", len(tb.entries))
		}
	}
}

// sync fsyncs only the tables this batcher owns (acctcs/storcs/witness),
// skipping read-only tables like senders that may deny write access.
func (b *outputBatcher) sync() error {
	for _, tb := range b.tables {
		if tb.tbl != nil {
			if err := tb.tbl.Sync(); err != nil {
				return err
			}
		}
	}
	return nil
}

// addEntry adds one block's data for a table.
// For tables that already have data at this position, the entry is silently
// dropped to avoid accumulating memory that will never be written.
func (b *outputBatcher) addEntry(name, ext string, data []byte) error {
	tb, ok := b.tables[name]
	if !ok {
		tbl, err := b.freezer.EnsureTableCompressed(name, ext)
		if err != nil {
			return err
		}
		tb = &tableBatch{
			name:          name,
			ext:           ext,
			tbl:           tbl,
			existingItems: tbl.Items(),
		}
		b.tables[name] = tb
		b.order = append(b.order, name)
	}

	// Don't accumulate entries that will be skipped — avoids wasting memory
	// when millions of blocks are already present (e.g. receipts/senders).
	itemIdx := b.nextItem + uint64(b.pendingCount())
	if itemIdx < tb.existingItems {
		return nil
	}
	tb.entries = append(tb.entries, data)
	return nil
}

// pendingCount returns the maximum pending count across tables that are
// actively accumulating (i.e. past their existingItems boundary).
func (b *outputBatcher) pendingCount() int {
	maxPending := 0
	for _, tb := range b.tables {
		if len(tb.entries) > maxPending {
			maxPending = len(tb.entries)
		}
	}
	return maxPending
}

// blocksSinceFlush returns the maximum pending entry count across all tables.
func (b *outputBatcher) blocksSinceFlush() int { return b.pendingCount() }

// flushFullBatches writes all complete batches (multiples of freezer.BatchSize).
func (b *outputBatcher) flushFullBatches() (int, error) {
	pending := b.blocksSinceFlush()
	if pending < freezer.BatchSize {
		return 0, nil
	}
	flushed := 0
	for pending >= freezer.BatchSize {
		if err := b.flushOneBatch(freezer.BatchSize); err != nil {
			return flushed, err
		}
		flushed += freezer.BatchSize
		pending -= freezer.BatchSize
	}
	return flushed, nil
}

// flushAll writes everything including partial last batch.
func (b *outputBatcher) flushAll() error {
	for b.blocksSinceFlush() > 0 {
		n := b.blocksSinceFlush()
		if n > freezer.BatchSize {
			n = freezer.BatchSize
		}
		if err := b.flushOneBatch(n); err != nil {
			return err
		}
	}
	return nil
}

// flushOneBatch processes n blocks across all tables.
// Tables with existing data are verified and skipped; tables that need
// data are written normally.
func (b *outputBatcher) flushOneBatch(n int) error {
	batchStart := b.nextItem
	batchEnd := batchStart + uint64(n)

	for _, name := range b.order {
		tb := b.tables[name]

		if tb.existingItems >= batchEnd {
			// Fully covered — spot-check then skip.
			b.verifyExisting(tb, batchStart, n)
			continue
		}

		if tb.existingItems > batchStart {
			// Partial overlap — skip existing portion, write the rest.
			skip := int(tb.existingItems - batchStart)
			b.verifyExisting(tb, batchStart, skip)
			writeCount := n - skip
			if writeCount > len(tb.entries) {
				writeCount = len(tb.entries)
			}
			if writeCount > 0 {
				entries := tb.entries[:writeCount]
				encoded := freezer.EncodeBatch(entries, b.enc)
				if err := freezer.WriteBatch(tb.tbl, entries, encoded); err != nil {
					return fmt.Errorf("output batcher: %s partial: %w", name, err)
				}
			}
			tb.entries = tb.entries[writeCount:]
			continue
		}

		// Normal write — table needs all n entries.
		// Skip tables with 0 entries (e.g. --no-outputs mode).
		if len(tb.entries) == 0 {
			continue
		}
		if len(tb.entries) < n {
			return fmt.Errorf("output batcher: table %s has %d entries, need %d", name, len(tb.entries), n)
		}
		entries := tb.entries[:n]
		encoded := freezer.EncodeBatch(entries, b.enc)
		if err := freezer.WriteBatch(tb.tbl, entries, encoded); err != nil {
			return fmt.Errorf("output batcher: %s: %w", name, err)
		}
		tb.entries = tb.entries[n:]
	}

	b.nextItem = batchEnd
	return nil
}

const maxMismatchWarns = 10

// verifyExisting spot-checks one entry against the existing table data.
func (b *outputBatcher) verifyExisting(tb *tableBatch, batchStart uint64, count int) {
	if tb.verified || count == 0 {
		return
	}

	item := batchStart + uint64(count/2)
	existing, err := tb.tbl.Retrieve(item)
	if err != nil {
		return
	}

	// For fully-skipped tables, entries aren't accumulated so we can't
	// compare content — just confirm the existing data is non-empty or
	// consistently empty.
	if len(tb.entries) == 0 {
		// No local data to compare; mark verified on first successful read.
		tb.verified = true
		log.Info("Output table has existing data, skipping writes",
			"table", tb.name, "existingItems", tb.existingItems,
			"sampleItem", item, "sampleLen", len(existing))
		return
	}

	sampleIdx := count / 2
	if sampleIdx >= len(tb.entries) {
		tb.verified = true
		return
	}
	newData := tb.entries[sampleIdx]

	if bytes.Equal(existing, newData) {
		tb.verified = true
		log.Info("Output table verified — existing data matches, skipping writes",
			"table", tb.name, "item", item, "existingItems", tb.existingItems)
		return
	}

	tb.warnCount++
	if tb.warnCount <= maxMismatchWarns {
		log.Warn("Output table mismatch — existing data differs, keeping existing",
			"table", tb.name, "item", item,
			"existingLen", len(existing), "newLen", len(newData),
			"existingItems", tb.existingItems)
	}
	if tb.warnCount >= 3 {
		tb.verified = true
	}
}

// padTableTo pads a freezer table to targetItems using batch-format
// empty entries.  Shared by alignOnResume and padSendersTable.
func padTableTo(tbl *freezer.FreezerTable, targetItems uint64, enc *zstd.Encoder) error {
	items := tbl.Items()
	if items >= targetItems {
		return nil
	}
	for items < targetItems {
		n := int(targetItems - items)
		if n > freezer.BatchSize {
			n = freezer.BatchSize
		}
		empty := make([][]byte, n)
		encoded := freezer.EncodeBatch(empty, enc)
		if err := freezer.WriteBatch(tbl, empty, encoded); err != nil {
			return err
		}
		items += uint64(n)
	}
	return nil
}

// alignOnResume prepares output tables for resumed execution.
// Truncates tables that ran ahead of MDBX and recovers partial batches.
// If hasRemainder is true, MDBX has authoritative remainder entries —
// skip freezer partial-batch recovery to avoid double-loading.
func (b *outputBatcher) alignOnResume(startBlock uint64, hasRemainder bool) error {
	b.nextItem = startBlock
	log.Info("alignOnResume starting",
		"startBlock", startBlock)

	tables := []string{
		freezer.TableAccountChanges,
		freezer.TableStorageChanges,
		freezer.TableBlockWitness,
	}
	for _, name := range tables {
		tbl, err := b.freezer.EnsureTableCompressed(name, "c")
		if err != nil {
			return err
		}
		items := tbl.Items()

		// Align table to startBlock.
		if items > startBlock {
			log.Info("Truncating table to startBlock",
				"table", name, "items", items, "startBlock", startBlock)
			if err := tbl.TruncateHead(startBlock); err != nil {
				return fmt.Errorf("truncate %s: %w", name, err)
			}
			items = startBlock
		}
		if items < startBlock {
			gap := startBlock - items
			// Output freezer is behind MDBX — changesets for blocks
			// [items, startBlock) were lost (hard kill / unclean shutdown).
			// NEVER pad with empty entries: that silently corrupts the
			// changeset stream, causing rebuild-state to produce wrong
			// state roots. Error out so the operator deletes the output
			// freezer and lets the next run regenerate all changesets.
			return fmt.Errorf("output table %s has %d items but MDBX starts at block %d (gap %d); "+
				"changesets lost during shutdown — delete output freezer and re-run",
				name, items, startBlock, gap)
		}

		tb := &tableBatch{
			name:          name,
			ext:           "c",
			tbl:           tbl,
			existingItems: items,
		}

		tail := items % uint64(freezer.BatchSize)
		if tail > 0 {
			batchStart := items - tail
			if hasRemainder {
				// MDBX has authoritative remainder — just truncate to
				// batch boundary. preloadRemainder will restore entries.
				log.Info("Truncating to batch boundary (MDBX remainder available)",
					"table", name, "batchStart", batchStart, "tail", tail)
				if err := tbl.TruncateHead(batchStart); err != nil {
					return fmt.Errorf("truncate partial %s: %w", name, err)
				}
				tb.existingItems = batchStart
				if batchStart < b.nextItem {
					b.nextItem = batchStart
				}
			} else {
				// No MDBX remainder — recover from freezer.
				var recovered [][]byte
				for i := batchStart; i < items; i++ {
					d, err := tbl.Retrieve(i)
					if err != nil {
						log.Warn("Partial batch entry unreadable, discarding",
							"table", name, "item", i, "err", err)
						recovered = nil
						break
					}
					recovered = append(recovered, d)
				}
				if err := tbl.TruncateHead(batchStart); err != nil {
					return fmt.Errorf("truncate partial %s: %w", name, err)
				}
				if recovered != nil && uint64(len(recovered)) == tail {
					log.Info("Recovering partial batch",
						"table", name, "batchStart", batchStart, "tail", tail)
					tb.entries = recovered
					tb.existingItems = batchStart
					if batchStart < b.nextItem {
						b.nextItem = batchStart
					}
				} else {
					return fmt.Errorf("output table %s: partial batch at %d unrecoverable (%d of %d entries readable); "+
						"delete output freezer and re-run",
						name, batchStart, len(recovered), tail)
				}
			}
		}

		b.tables[name] = tb
		b.order = append(b.order, name)
	}
	return nil
}
