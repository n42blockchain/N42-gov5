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
//
// Random-access reader for JMT archive segments produced by compressor.go.
// NodeArchiveReader pairs a seg.Decompressor with a recsplit.Index so a
// node can be fetched by hash in O(1) without scanning the segment.
// OpenReader opens the matching .seg and .idx files, wiring the two
// handles into a single reader that also knows how to close both sides
// cleanly on error during construction.

package archive

import (
	"bytes"
	"fmt"

	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/seg"
)

// NodeArchiveReader provides O(1) random access to archived JMT nodes
// stored in a compressed segment file (.seg) with a RecSplit index (.idx).
type NodeArchiveReader struct {
	decomp  *seg.Decompressor
	index   *recsplit.Index
	segPath string
	idxPath string
}

// OpenReader opens an existing node archive for reading.
// segPath is the path to the compressed segment file (.seg),
// idxPath is the path to the RecSplit index file (.idx).
func OpenReader(segPath, idxPath string) (*NodeArchiveReader, error) {
	decomp, err := seg.NewDecompressor(segPath)
	if err != nil {
		return nil, fmt.Errorf("open segment %s: %w", segPath, err)
	}

	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		decomp.Close()
		return nil, fmt.Errorf("open index %s: %w", idxPath, err)
	}

	return &NodeArchiveReader{
		decomp:  decomp,
		index:   idx,
		segPath: segPath,
		idxPath: idxPath,
	}, nil
}

// GetNode retrieves the encoded node bytes for the given node hash.
// Returns an error if the node is not found or the stored hash does not
// match (indicating a hash collision in the perfect hash function).
func (r *NodeArchiveReader) GetNode(nodeHash [32]byte) ([]byte, error) {
	if r.index.Empty() {
		return nil, fmt.Errorf("node not found: archive is empty")
	}

	// Look up offset via the RecSplit index.
	reader := recsplit.NewIndexReader(r.index)
	offset, _ := reader.Lookup(nodeHash[:])

	// Seek to the offset and read the hash word, then the data word.
	g := r.decomp.MakeGetter()
	g.Reset(offset)

	if !g.HasNext() {
		return nil, fmt.Errorf("node not found: offset %d beyond segment data", offset)
	}

	// Read the hash word.
	var hashBuf []byte
	hashBuf, _ = g.Next(hashBuf[:0])

	// Verify the hash matches to detect false positives from the
	// perfect hash function.
	if !bytes.Equal(hashBuf, nodeHash[:]) {
		return nil, fmt.Errorf("node not found: hash mismatch at offset %d", offset)
	}

	if !g.HasNext() {
		return nil, fmt.Errorf("node not found: missing data word at offset %d", offset)
	}

	// Read the data word (the encoded node).
	var dataBuf []byte
	dataBuf, _ = g.Next(dataBuf[:0])

	return dataBuf, nil
}

// Count returns the number of node pairs stored in the archive.
// Each pair consists of a hash word and a data word, so the segment
// word count is divided by 2.
func (r *NodeArchiveReader) Count() uint64 {
	return uint64(r.decomp.Count()) / 2
}

// Close releases all resources held by the reader.
func (r *NodeArchiveReader) Close() {
	if r.index != nil {
		r.index.Close()
		r.index = nil
	}
	if r.decomp != nil {
		r.decomp.Close()
		r.decomp = nil
	}
}
