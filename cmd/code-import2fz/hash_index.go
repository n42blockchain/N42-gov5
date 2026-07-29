// hash_index.go — emits the content-addressed index for the codes freezer.
//
// Bytecode is identified by keccak(code), which is the key of both source
// tables (reth Bytecodes, N42 Code) and what every reader verifies after the
// read. Indexing by that key instead of by address removes the reason the
// exporter ever had to join against PlainAccountState.
//
// The index stores no keys. A minimal perfect hash maps the N hashes in the
// build set onto slots [0,N) with no collisions and answers arbitrarily for
// anything outside the set; the reader's keccak(code) == codeHash check is
// what separates the two, so the keys need not be kept. That is ~1.8 bits per
// key rather than 32 bytes — for 2.5M codes, well under a megabyte instead of
// 80 MB.

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
)

const (
	hashIndexFile   = "codes.hidx"
	hashOffsetsFile = "codes.hoff"
	// hoffEntrySize is [fileNum:2 LE][offset:4 LE][length:4 LE]. The length is
	// stored rather than derived from the neighbouring entry, so this index
	// does not depend on codes.cidx existing.
	hoffEntrySize = 10
)

// hashIndexInput is one unique code blob as written to the cdat files.
type hashIndexInput struct {
	hash    [32]byte
	fileNum uint16
	offset  uint32
	length  uint32
}

// writeHashIndex builds the MPHF over the code hashes and writes the
// slot-ordered offset table beside it. items must already be deduplicated by
// hash — with the address join enabled the same code appears once per
// referencing address, and only one of those entries belongs here.
func writeHashIndex(outdir string, items []hashIndexInput, logger log2.Logger) error {
	if len(items) == 0 {
		return nil
	}
	idxPath := filepath.Join(outdir, hashIndexFile)
	tmpDir := filepath.Join(outdir, "hidx-tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", tmpDir, err)
	}
	defer os.RemoveAll(tmpDir)

	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   len(items),
		BucketSize: 2000,
		IndexFile:  idxPath,
		TmpDir:     tmpDir,
		LeafSize:   8,
		NoValues:   true, // Lookup returns the perfect-hash slot directly
	}, logger)
	if err != nil {
		return fmt.Errorf("recsplit new: %w", err)
	}
	defer rs.Close()

	// RecSplit can fail to find a perfect hash for a given seed; the documented
	// recovery is to reset and feed the same keys again.
	for {
		for i := range items {
			if err := rs.AddKey(items[i].hash[:], uint64(i)); err != nil {
				return fmt.Errorf("recsplit addkey: %w", err)
			}
		}
		err = rs.Build(context.Background())
		if err == nil {
			break
		}
		if rs.Collision() {
			rs.ResetNextSalt()
			continue
		}
		return fmt.Errorf("recsplit build: %w", err)
	}

	// Second pass: place each entry at the slot its hash maps to.
	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", idxPath, err)
	}
	defer idx.Close()
	reader := recsplit.NewIndexReader(idx)

	offs := make([]byte, len(items)*hoffEntrySize)
	placed := make([]bool, len(items))
	for i := range items {
		slot, ok := reader.Lookup(items[i].hash[:])
		if !ok || slot >= uint64(len(items)) {
			return fmt.Errorf("mphf lookup missed its own key %x (slot=%d n=%d)",
				items[i].hash[:6], slot, len(items))
		}
		if placed[slot] {
			return fmt.Errorf("mphf slot %d claimed twice — index is not perfect", slot)
		}
		placed[slot] = true
		rec := offs[slot*hoffEntrySize:]
		binary.LittleEndian.PutUint16(rec[0:2], items[i].fileNum)
		binary.LittleEndian.PutUint32(rec[2:6], items[i].offset)
		binary.LittleEndian.PutUint32(rec[6:10], items[i].length)
	}

	offPath := filepath.Join(outdir, hashOffsetsFile)
	if err := os.WriteFile(offPath, offs, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", offPath, err)
	}

	if st, serr := os.Stat(idxPath); serr == nil {
		fmt.Fprintf(os.Stderr, "  %s: %d keys, %s (%.2f bits/key) + %s offsets\n",
			hashIndexFile, len(items), humanSize(st.Size()),
			float64(st.Size()*8)/float64(len(items)), humanSize(int64(len(offs))))
	}
	return nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.2f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.2f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
