// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package freezer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RetrimIndexToItem rewrites <dir>/<name>.<ext>idx so the table logically
// begins at (about) wantStart: index entries below it are dropped and the new
// cidx header records the absolute start (see cidxHeader.start). Data files
// are NOT touched — the caller deletes cold .cdat files separately (reads of
// dropped items return ErrPruned regardless).
//
// wantStart is snapped BACK to the containing batch boundary (entries in one
// compressed batch share the same fileNum+offset, and retrieveBatch's
// backward scan must never cross the start), so the returned newStart may be
// smaller than requested. The previous cidx is kept as <idx>.bak-retrim; the
// rewrite is write-tmp + rename, so a crash leaves either the old or the new
// index, never a torn one.
//
// The table must be closed. Returns the snapped start and the number of
// entries dropped.
func RetrimIndexToItem(dir, name, ext string, wantStart uint64) (newStart, dropped uint64, err error) {
	if ext == "" {
		ext = "c"
	}
	idxPath := filepath.Join(dir, fmt.Sprintf("%s.%sidx", name, ext))
	raw, err := os.ReadFile(idxPath)
	if err != nil {
		return 0, 0, fmt.Errorf("retrim: read %s: %w", idxPath, err)
	}

	hdrSize := 0
	var hdr cidxHeader
	if h, ok := decodeCidxHeader(raw); ok {
		hdr = h
		hdrSize = cidxHeaderSize
	}
	if hdr.entrySize != 0 && hdr.entrySize != indexEntrySize {
		return 0, 0, fmt.Errorf("retrim: %s has entrySize %d, only %d supported",
			name, hdr.entrySize, indexEntrySize)
	}
	entries := (len(raw) - hdrSize) / indexEntrySize
	if entries == 0 {
		return hdr.start, 0, nil
	}
	entryAt := func(i int) indexEntry {
		off := hdrSize + i*indexEntrySize
		return decodeIndex(raw[off : off+indexEntrySize])
	}

	if wantStart <= hdr.start {
		return hdr.start, 0, nil // already trimmed at least this far
	}
	if wantStart >= hdr.start+uint64(entries) {
		return 0, 0, fmt.Errorf("retrim: start %d beyond table head %d",
			wantStart, hdr.start+uint64(entries))
	}
	lo := int(wantStart - hdr.start)

	// Snap back to the batch boundary: all entries of one compressed batch
	// share (fileNum, offset). For non-batched tables adjacent entries only
	// collide on zero-length items, where snapping is equally harmless.
	target := entryAt(lo)
	for lo > 0 && entryAt(lo-1) == target {
		lo--
	}
	if lo == 0 {
		return hdr.start, 0, nil
	}
	newStart = hdr.start + uint64(lo)

	// New header: mark version 2 (start != 0 misaddresses on pre-start-field
	// binaries — the bump is a downgrade tripwire, not read-gated today).
	hdr.version = 2
	hdr.start = newStart
	if hdr.entrySize == 0 {
		hdr.entrySize = indexEntrySize
	}

	tmpPath := idxPath + ".retrim-tmp"
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return 0, 0, fmt.Errorf("retrim: create tmp: %w", err)
	}
	if _, err = tmp.Write(encodeCidxHeader(hdr)); err == nil {
		_, err = tmp.Write(raw[hdrSize+lo*indexEntrySize:])
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmpPath)
		return 0, 0, fmt.Errorf("retrim: write tmp: %w", err)
	}

	bakPath := idxPath + ".bak-retrim"
	if _, err := os.Stat(bakPath); err == nil {
		os.Remove(tmpPath)
		return 0, 0, fmt.Errorf("retrim: backup %s already exists — remove it first", bakPath)
	}
	if err := os.Rename(idxPath, bakPath); err != nil {
		os.Remove(tmpPath)
		return 0, 0, fmt.Errorf("retrim: backup rename: %w", err)
	}
	if err := os.Rename(tmpPath, idxPath); err != nil {
		// Try to roll back so the table stays openable.
		os.Rename(bakPath, idxPath)
		return 0, 0, fmt.Errorf("retrim: install rename: %w", err)
	}
	return newStart, uint64(lo), nil
}

// RetrimIndexToFile retrims so the first retained entry is the first item
// stored in data file keepFromFile (fileNums are monotonic across entries).
// Convenience wrapper for "I kept .cdat files N.. and want the cidx to match".
func RetrimIndexToFile(dir, name, ext string, keepFromFile uint16) (newStart, dropped uint64, err error) {
	if ext == "" {
		ext = "c"
	}
	idxPath := filepath.Join(dir, fmt.Sprintf("%s.%sidx", name, ext))
	raw, err := os.ReadFile(idxPath)
	if err != nil {
		return 0, 0, fmt.Errorf("retrim: read %s: %w", idxPath, err)
	}
	hdrSize := 0
	var hdr cidxHeader
	if h, ok := decodeCidxHeader(raw); ok {
		hdr = h
		hdrSize = cidxHeaderSize
	}
	entries := (len(raw) - hdrSize) / indexEntrySize
	if entries == 0 {
		return hdr.start, 0, nil
	}
	lo := sort.Search(entries, func(i int) bool {
		off := hdrSize + i*indexEntrySize
		return decodeIndex(raw[off:off+indexEntrySize]).fileNum >= keepFromFile
	})
	if lo == entries {
		return 0, 0, fmt.Errorf("retrim: no entries in files >= %04d", keepFromFile)
	}
	return RetrimIndexToItem(dir, name, ext, hdr.start+uint64(lo))
}
