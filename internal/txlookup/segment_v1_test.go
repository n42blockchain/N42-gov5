package txlookup

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
)

// buildV1Segment hand-builds a legacy (pre-Elias-Fano) .idx/.dat pair:
// dat is a flat array of 4-byte little-endian relative block numbers, one
// per transaction, in ordinal order. This exercises OpenSegment's V1
// detection path (no "EFD2" magic) and TxSegment.lookupV1.
func buildV1Segment(t *testing.T, dir string, startBlock uint64, hashesPerBlock [][]types.Hash) (idxPath, datPath string) {
	t.Helper()
	totalTx := 0
	for _, hs := range hashesPerBlock {
		totalTx += len(hs)
	}

	idxPath = filepath.Join(dir, "v1test.idx")
	datPath = filepath.Join(dir, "v1test.dat")

	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   totalTx,
		BucketSize: 2000,
		LeafSize:   8,
		IndexFile:  idxPath,
		BaseDataID: startBlock,
		TmpDir:     t.TempDir(),
	}, log2.New())
	if err != nil {
		t.Fatalf("NewRecSplit: %v", err)
	}

	dat := make([]byte, totalTx*4)
	ordinal := uint64(0)
	for relBlock, hs := range hashesPerBlock {
		for _, h := range hs {
			if err := rs.AddKey(h[:], ordinal); err != nil {
				t.Fatalf("AddKey: %v", err)
			}
			binary.LittleEndian.PutUint32(dat[ordinal*4:ordinal*4+4], uint32(relBlock))
			ordinal++
		}
	}
	if err := rs.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}

	if err := os.WriteFile(datPath, dat, 0644); err != nil {
		t.Fatalf("write dat: %v", err)
	}
	return idxPath, datPath
}

func TestOpenSegment_V1Format(t *testing.T) {
	dir := t.TempDir()
	hashesPerBlock := [][]types.Hash{
		{hashFor(100, 0), hashFor(100, 1)},
		{hashFor(101, 0)},
		{hashFor(102, 0), hashFor(102, 1), hashFor(102, 2)},
	}
	idxPath, datPath := buildV1Segment(t, dir, 100, hashesPerBlock)

	seg, err := OpenSegment(idxPath, datPath)
	if err != nil {
		t.Fatalf("OpenSegment: %v", err)
	}
	defer seg.Close()

	if seg.IsV2() {
		t.Fatal("expected V1 segment (no Elias-Fano)")
	}
	if seg.StartBlock() != 100 {
		t.Fatalf("StartBlock() = %d, want 100", seg.StartBlock())
	}
	if seg.TxCount() != 6 {
		t.Fatalf("TxCount() = %d, want 6", seg.TxCount())
	}

	got := seg.Lookup(hashFor(102, 1))
	if got == nil || *got != 102 {
		t.Fatalf("Lookup(block 102 tx 1) = %v, want 102", got)
	}
	got = seg.Lookup(hashFor(100, 0))
	if got == nil || *got != 100 {
		t.Fatalf("Lookup(block 100 tx 0) = %v, want 100", got)
	}
}

func TestOpenSegment_V1SizeMismatch(t *testing.T) {
	dir := t.TempDir()
	hashesPerBlock := [][]types.Hash{{hashFor(5, 0)}}
	idxPath, datPath := buildV1Segment(t, dir, 5, hashesPerBlock)

	// Corrupt the dat file so its size no longer matches txCount*4.
	if err := os.WriteFile(datPath, []byte{1, 2, 3}, 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenSegment(idxPath, datPath); err == nil {
		t.Fatal("expected error for V1 dat size mismatch")
	}
}
