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

package archive

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/seg"
)

// NodeArchiveWriter collects JMT nodes and writes them to a compressed
// segment file (.seg) with a RecSplit index (.idx) for O(1) random access.
//
// Usage:
//
//	w, _ := NewWriter(ctx, outputDir, fromBlock, toBlock, tmpDir, logger)
//	w.AddNode(nodeHash, encodedNode)  // repeat for all nodes
//	w.Build(ctx)                       // compresses + builds index
//	w.Close()
type NodeArchiveWriter struct {
	compressor *seg.Compressor
	segPath    string
	idxPath    string
	tmpDir     string
	fromBlock  uint64
	toBlock    uint64
	count      uint64
	keys       [][]byte // node hashes for RecSplit index
	logger     log.Logger
}

// SegmentFileName returns the canonical segment file name for the given block range.
// Format: "v1-NNNNNN-NNNNNN-jmtnodes.seg"
func SegmentFileName(fromBlock, toBlock uint64) string {
	return fmt.Sprintf("v1-%06d-%06d-jmtnodes.seg", fromBlock, toBlock)
}

// indexFileName returns the index file name corresponding to the given segment file name.
func indexFileName(fromBlock, toBlock uint64) string {
	return fmt.Sprintf("v1-%06d-%06d-jmtnodes.idx", fromBlock, toBlock)
}

// NewWriter creates a new NodeArchiveWriter that will produce a compressed
// segment file and a RecSplit index in outputDir.
func NewWriter(ctx context.Context, outputDir string, fromBlock, toBlock uint64, tmpDir string, logger log.Logger) (*NodeArchiveWriter, error) {
	segName := SegmentFileName(fromBlock, toBlock)
	segPath := filepath.Join(outputDir, segName)

	comp, err := seg.NewCompressor(ctx, "jmtnodes", segPath, tmpDir, seg.MinPatternScore, 1, log.LvlDebug, logger)
	if err != nil {
		return nil, fmt.Errorf("create compressor: %w", err)
	}
	comp.DisableFsync()

	idxName := indexFileName(fromBlock, toBlock)
	idxPath := filepath.Join(outputDir, idxName)

	return &NodeArchiveWriter{
		compressor: comp,
		segPath:    segPath,
		idxPath:    idxPath,
		tmpDir:     tmpDir,
		fromBlock:  fromBlock,
		toBlock:    toBlock,
		keys:       make([][]byte, 0, 4096),
		logger:     logger,
	}, nil
}

// AddNode adds a JMT node to the archive. The nodeHash is the 32-byte
// content-addressed hash, and encoded is the serialized node bytes
// (produced by jmt.EncodeNode).
//
// Words are written in pairs: [nodeHash][encodedNode] so that the
// segment file stores alternating key-value entries.
func (w *NodeArchiveWriter) AddNode(nodeHash [32]byte, encoded []byte) error {
	// Write hash as first word of the pair.
	if err := w.compressor.AddWord(nodeHash[:]); err != nil {
		return fmt.Errorf("add hash word: %w", err)
	}
	// Write encoded node as second word of the pair.
	if err := w.compressor.AddWord(encoded); err != nil {
		return fmt.Errorf("add data word: %w", err)
	}
	// Keep a copy of the hash for the RecSplit index.
	keyCopy := make([]byte, 32)
	copy(keyCopy, nodeHash[:])
	w.keys = append(w.keys, keyCopy)
	w.count++
	return nil
}

// Build compresses the segment file and then constructs the RecSplit index.
// After Build returns successfully, the .seg and .idx files are ready.
func (w *NodeArchiveWriter) Build(ctx context.Context) error {
	// Step 1: Compress all words into the .seg file.
	if err := w.compressor.Compress(); err != nil {
		return fmt.Errorf("compress segment: %w", err)
	}
	w.compressor.Close()
	w.compressor = nil

	// Step 2: Open the compressed segment to read offsets for index building.
	decomp, err := seg.NewDecompressor(w.segPath)
	if err != nil {
		return fmt.Errorf("open segment for indexing: %w", err)
	}
	defer decomp.Close()

	// Step 3: Build RecSplit index mapping nodeHash -> offset of hash word.
	//
	// We iterate over all words in pairs. For each pair, we record the
	// offset of the first word (the hash) before reading it, then skip
	// the second word (the data). The RecSplit index maps each nodeHash
	// to the offset where that pair begins.
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   int(w.count),
		BucketSize: 2000,
		IndexFile:  w.idxPath,
		TmpDir:     w.tmpDir,
		LeafSize:   8,
		Enums:      false,
	}, w.logger)
	if err != nil {
		return fmt.Errorf("create recsplit: %w", err)
	}
	defer rs.Close()
	rs.DisableFsync()

	g := decomp.MakeGetter()
	var keyIdx int
	var pairOffset uint64 // offset of the current hash word (start of pair)

	for g.HasNext() {
		// Skip the hash word; the offset before it is pairOffset.
		nextOffset, _ := g.Skip() // skip hash word, returns (offset after hash, wordLen)

		// Skip the data word.
		if !g.HasNext() {
			return fmt.Errorf("unexpected end of segment: missing data word at pair %d", keyIdx)
		}
		afterData, _ := g.Skip() // skip data word

		if keyIdx >= len(w.keys) {
			return fmt.Errorf("more pairs in segment than expected: got past %d", keyIdx)
		}
		if err := rs.AddKey(w.keys[keyIdx], pairOffset); err != nil {
			return fmt.Errorf("add recsplit key %d: %w", keyIdx, err)
		}
		_ = nextOffset
		pairOffset = afterData
		keyIdx++
	}

	for {
		if err := rs.Build(ctx); err != nil {
			if rs.Collision() {
				w.logger.Info("recsplit collision, retrying with new salt")
				rs.ResetNextSalt()
				continue
			}
			return fmt.Errorf("build recsplit: %w", err)
		}
		break
	}

	return nil
}

// Close releases resources held by the writer. It is safe to call Close
// multiple times.
func (w *NodeArchiveWriter) Close() {
	if w.compressor != nil {
		w.compressor.Close()
		w.compressor = nil
	}
}
