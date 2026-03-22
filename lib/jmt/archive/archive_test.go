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
	"bytes"
	"context"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/log/v3"
)

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	return log.New()
}

// makeTestNodes generates a deterministic set of JMT nodes of mixed types
// (internal, leaf, extension) and returns their hashes and encoded forms.
func makeTestNodes(count int) (hashes [][32]byte, encoded [][]byte) {
	h := jmt.Blake3Hasher{}
	rng := rand.New(rand.NewSource(42))

	for i := 0; i < count; i++ {
		var node *jmt.Node

		switch i % 3 {
		case 0:
			// Internal node with 2-4 random children.
			node = jmt.NewInternalNode()
			numChildren := 2 + rng.Intn(3)
			used := map[int]bool{}
			for j := 0; j < numChildren; j++ {
				idx := rng.Intn(jmt.ChildCount)
				for used[idx] {
					idx = rng.Intn(jmt.ChildCount)
				}
				used[idx] = true
				var childHash jmt.Hash
				rng.Read(childHash[:])
				node.Internal.Children[idx].Hash = childHash
				node.Internal.Children[idx].Valid = true
			}

		case 1:
			// Leaf node.
			var keyHash jmt.Hash
			rng.Read(keyHash[:])
			value := make([]byte, 20+rng.Intn(100))
			rng.Read(value)
			node = jmt.NewLeafNode(keyHash, value, h)

		case 2:
			// Extension node.
			pathLen := 1 + rng.Intn(8)
			nibbles := make([]byte, pathLen)
			for j := range nibbles {
				nibbles[j] = byte(rng.Intn(16))
			}
			var childHash jmt.Hash
			rng.Read(childHash[:])
			node = jmt.NewExtensionNode(jmt.NewNibblePathFromSlice(nibbles), childHash)
		}

		enc := jmt.EncodeNode(node)
		nodeHash := jmt.NodeHash(node, h)
		hashes = append(hashes, nodeHash)
		encoded = append(encoded, enc)
	}
	return hashes, encoded
}

func TestWriterReaderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tmpDir := t.TempDir()
	logger := testLogger(t)
	ctx := context.Background()

	const nodeCount = 100
	hashes, encoded := makeTestNodes(nodeCount)

	// Write archive.
	w, err := NewWriter(ctx, dir, 0, 1000, tmpDir, logger)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	for i := 0; i < nodeCount; i++ {
		if err := w.AddNode(hashes[i], encoded[i]); err != nil {
			t.Fatalf("AddNode(%d): %v", i, err)
		}
	}

	if err := w.Build(ctx); err != nil {
		t.Fatalf("Build: %v", err)
	}
	w.Close()

	// Read archive.
	segPath := filepath.Join(dir, SegmentFileName(0, 1000))
	idxPath := filepath.Join(dir, indexFileName(0, 1000))

	r, err := OpenReader(segPath, idxPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	if got := r.Count(); got != nodeCount {
		t.Fatalf("Count: got %d, want %d", got, nodeCount)
	}

	for i := 0; i < nodeCount; i++ {
		data, err := r.GetNode(hashes[i])
		if err != nil {
			t.Fatalf("GetNode(%d): %v", i, err)
		}
		if !bytes.Equal(data, encoded[i]) {
			t.Fatalf("GetNode(%d): data mismatch\ngot  %x\nwant %x", i, data, encoded[i])
		}
	}
}

func TestReaderMissingNode(t *testing.T) {
	dir := t.TempDir()
	tmpDir := t.TempDir()
	logger := testLogger(t)
	ctx := context.Background()

	// Create an archive with a few nodes.
	hashes, encoded := makeTestNodes(10)

	w, err := NewWriter(ctx, dir, 100, 200, tmpDir, logger)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for i := 0; i < len(hashes); i++ {
		if err := w.AddNode(hashes[i], encoded[i]); err != nil {
			t.Fatalf("AddNode: %v", err)
		}
	}
	if err := w.Build(ctx); err != nil {
		t.Fatalf("Build: %v", err)
	}
	w.Close()

	segPath := filepath.Join(dir, SegmentFileName(100, 200))
	idxPath := filepath.Join(dir, indexFileName(100, 200))

	r, err := OpenReader(segPath, idxPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	// Look up a hash that was never added.
	var bogus [32]byte
	bogus[0] = 0xFF
	bogus[1] = 0xFE
	bogus[31] = 0x01
	_, err = r.GetNode(bogus)
	if err == nil {
		t.Fatal("expected error for missing node, got nil")
	}
}

func TestSegmentFileName(t *testing.T) {
	tests := []struct {
		from, to uint64
		want     string
	}{
		{0, 1000, "v1-000000-001000-jmtnodes.seg"},
		{1000, 2000, "v1-001000-002000-jmtnodes.seg"},
		{123456, 999999, "v1-123456-999999-jmtnodes.seg"},
	}
	for _, tt := range tests {
		got := SegmentFileName(tt.from, tt.to)
		if got != tt.want {
			t.Errorf("SegmentFileName(%d, %d) = %q, want %q", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestEmptyArchive(t *testing.T) {
	dir := t.TempDir()
	tmpDir := t.TempDir()
	logger := testLogger(t)
	ctx := context.Background()

	w, err := NewWriter(ctx, dir, 0, 100, tmpDir, logger)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	// Build with zero nodes.
	if err := w.Build(ctx); err != nil {
		t.Fatalf("Build empty: %v", err)
	}
	w.Close()

	segPath := filepath.Join(dir, SegmentFileName(0, 100))
	idxPath := filepath.Join(dir, indexFileName(0, 100))

	r, err := OpenReader(segPath, idxPath)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer r.Close()

	if got := r.Count(); got != 0 {
		t.Fatalf("Count: got %d, want 0", got)
	}
}
