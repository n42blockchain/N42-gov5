// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/n42blockchain/N42/common/types"
)

// LeafJournal records per-block leaf changes (key+value) to a sequential file.
// This is the "output" of each block — the minimal data needed to build
// any state commitment tree (BMT/JMT/MPT/Verkle) without re-executing EVM.
//
// File format (append-only, no seeking):
//
//	[blockNum: 8B big-endian]
//	[count:    4B big-endian]  (number of leaf entries, 0 for empty blocks)
//	for each entry:
//	  [key:    32B]            (Blake3 hash of address or address+slot)
//	  [valLen: 2B big-endian]  (0 = deletion)
//	  [val:    valLen bytes]
//
// To rebuild tree at block N:
//
//	state = empty map
//	for block 0..N:
//	  for each (key, val) in block's entries:
//	    if val == nil: delete(state, key)
//	    else: state[key] = val
//	  root = Tree.BuildFromState(state)
type LeafJournal struct {
	f   *os.File
	w   *bufio.Writer
	err error // sticky error
}

// NewLeafJournal creates or appends to a journal file.
func NewLeafJournal(path string) (*LeafJournal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open leaf journal: %w", err)
	}
	return &LeafJournal{
		f: f,
		w: bufio.NewWriterSize(f, 1<<20), // 1MB buffer
	}, nil
}

// LeafEntry is a single key-value change in a block.
type LeafEntry struct {
	Key   types.Hash // Blake3(address) or Blake3(address+slot)
	Value []byte     // new value (nil or empty = deletion)
}

// WriteBlock writes all leaf changes for a block. Empty blocks write header only (count=0).
func (j *LeafJournal) WriteBlock(blockNum uint64, entries []LeafEntry) error {
	if j.err != nil {
		return j.err
	}

	// Block header: blockNum(8) + count(4)
	var hdr [12]byte
	binary.BigEndian.PutUint64(hdr[:8], blockNum)
	binary.BigEndian.PutUint32(hdr[8:12], uint32(len(entries)))
	if _, err := j.w.Write(hdr[:]); err != nil {
		j.err = err
		return err
	}

	// Entries
	for _, e := range entries {
		// key: 32 bytes
		if _, err := j.w.Write(e.Key[:]); err != nil {
			j.err = err
			return err
		}
		// valLen: 2 bytes
		var vl [2]byte
		binary.BigEndian.PutUint16(vl[:], uint16(len(e.Value)))
		if _, err := j.w.Write(vl[:]); err != nil {
			j.err = err
			return err
		}
		// val: N bytes
		if len(e.Value) > 0 {
			if _, err := j.w.Write(e.Value); err != nil {
				j.err = err
				return err
			}
		}
	}
	return nil
}

// Flush writes buffered data to disk.
func (j *LeafJournal) Flush() error {
	if j.err != nil {
		return j.err
	}
	return j.w.Flush()
}

// Close flushes and closes the journal.
func (j *LeafJournal) Close() error {
	if err := j.w.Flush(); err != nil && j.err == nil {
		j.err = err
	}
	if err := j.f.Close(); err != nil && j.err == nil {
		j.err = err
	}
	return j.err
}

// LeafJournalReader reads a journal file sequentially.
type LeafJournalReader struct {
	f *os.File
	r *bufio.Reader
}

// NewLeafJournalReader opens a journal file for reading.
func NewLeafJournalReader(path string) (*LeafJournalReader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &LeafJournalReader{f: f, r: bufio.NewReaderSize(f, 1<<20)}, nil
}

// BlockEntries holds the leaf changes for one block.
type BlockEntries struct {
	BlockNum uint64
	Entries  []LeafEntry
}

// ReadBlock reads the next block's entries. Returns io.EOF at end.
func (r *LeafJournalReader) ReadBlock() (*BlockEntries, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r.r, hdr[:]); err != nil {
		return nil, err // io.EOF at end
	}
	blockNum := binary.BigEndian.Uint64(hdr[:8])
	count := binary.BigEndian.Uint32(hdr[8:12])

	entries := make([]LeafEntry, count)
	for i := uint32(0); i < count; i++ {
		var key types.Hash
		if _, err := io.ReadFull(r.r, key[:]); err != nil {
			return nil, fmt.Errorf("read key at block %d entry %d: %w", blockNum, i, err)
		}
		var vl [2]byte
		if _, err := io.ReadFull(r.r, vl[:]); err != nil {
			return nil, fmt.Errorf("read valLen at block %d entry %d: %w", blockNum, i, err)
		}
		valLen := binary.BigEndian.Uint16(vl[:])
		var val []byte
		if valLen > 0 {
			val = make([]byte, valLen)
			if _, err := io.ReadFull(r.r, val); err != nil {
				return nil, fmt.Errorf("read val at block %d entry %d: %w", blockNum, i, err)
			}
		}
		entries[i] = LeafEntry{Key: key, Value: val}
	}

	return &BlockEntries{BlockNum: blockNum, Entries: entries}, nil
}

// Close closes the reader.
func (r *LeafJournalReader) Close() error {
	return r.f.Close()
}
