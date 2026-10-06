package txlookup

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// fixedHashBody encodes a small list of tx hashes as a body blob:
// [uint32 count][32-byte hash]*count. decodeFixedHashBody is its decoder,
// injected as the builder's BodyTxHashes.
func fixedHashBody(hashes []types.Hash) []byte {
	buf := make([]byte, 4+32*len(hashes))
	binary.LittleEndian.PutUint32(buf[:4], uint32(len(hashes)))
	for i, h := range hashes {
		copy(buf[4+32*i:4+32*(i+1)], h[:])
	}
	return buf
}

func decodeFixedHashBody(data []byte) ([]types.Hash, error) {
	if len(data) < 4 {
		return nil, nil
	}
	n := binary.LittleEndian.Uint32(data[:4])
	out := make([]types.Hash, 0, n)
	for i := uint32(0); i < n; i++ {
		var h types.Hash
		copy(h[:], data[4+32*i:4+32*(i+1)])
		out = append(out, h)
	}
	return out, nil
}

// buildTestFreezer creates a freezer in a temp dir and appends `n` blocks,
// each with `txPerBlock` synthetic transaction hashes, starting at block 0.
func buildTestFreezer(t *testing.T, n int, txPerBlock int) (*freezer.Freezer, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := freezer.New(dir, 0)
	if err != nil {
		t.Fatalf("freezer.New: %v", err)
	}
	t.Cleanup(func() { f.Close() })

	data := &freezer.FreezeData{}
	for b := 0; b < n; b++ {
		hashes := make([]types.Hash, txPerBlock)
		for i := range hashes {
			hashes[i] = hashFor(uint64(b), i)
		}
		data.Headers = append(data.Headers, []byte{byte(b)})
		data.Bodies = append(data.Bodies, fixedHashBody(hashes))
		data.Receipts = append(data.Receipts, []byte{})
		var h types.Hash
		h[0] = byte(b)
		data.Hashes = append(data.Hashes, h[:])
		data.Difficulty = append(data.Difficulty, []byte{1})
	}
	if err := f.Freeze(0, data); err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	return f, dir
}

func TestSegmentBuilder_BuildRangeAndLookup(t *testing.T) {
	const blocks = 20
	const txPerBlock = 3
	f, _ := buildTestFreezer(t, blocks, txPerBlock)

	outDir := t.TempDir()
	b := NewSegmentBuilder(f, outDir, decodeFixedHashBody)

	if err := b.BuildRange(context.Background(), 0, blocks); err != nil {
		t.Fatalf("BuildRange: %v", err)
	}

	svc, err := NewService(outDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	// Spot check a few hashes resolve to the right block.
	for _, blk := range []int{0, 5, blocks - 1} {
		h := hashFor(uint64(blk), 0)
		got, err := svc.Lookup(nil, h)
		if err != nil {
			t.Fatalf("Lookup(block %d): %v", blk, err)
		}
		if got == nil || *got != uint64(blk) {
			t.Fatalf("Lookup(block %d) = %v, want %d", blk, got, blk)
		}
	}

	if svc.SegmentCount() == 0 {
		t.Fatal("expected at least one segment to be built")
	}
}

func TestSegmentBuilder_BuildRangeEmptyBlocks(t *testing.T) {
	// Blocks with zero transactions exercise the "empty segment" path in
	// buildOne (totalTx == 0).
	f, _ := buildTestFreezer(t, 5, 0)
	outDir := t.TempDir()
	b := NewSegmentBuilder(f, outDir, decodeFixedHashBody)

	if err := b.BuildRange(context.Background(), 0, 5); err != nil {
		t.Fatalf("BuildRange: %v", err)
	}

	svc, err := NewService(outDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	// An empty segment has zero keys in its RecSplit index; looking up any
	// hash must not panic or hang, whatever it reports.
	if _, err := svc.Lookup(nil, hashFor(999, 0)); err != nil {
		t.Logf("Lookup on empty segment returned error (acceptable): %v", err)
	}
}

func TestSegmentBuilder_SetRecSplitTuning(t *testing.T) {
	f, _ := buildTestFreezer(t, 10, 2)
	outDir := t.TempDir()
	b := NewSegmentBuilder(f, outDir, decodeFixedHashBody)
	b.SetRecSplitTuning(true, false)

	if err := b.BuildRange(context.Background(), 0, 10); err != nil {
		t.Fatalf("BuildRange with enums/no-LFP tuning: %v", err)
	}

	svc, err := NewService(outDir)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	defer svc.Close()

	got, err := svc.Lookup(nil, hashFor(3, 1))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil || *got != 3 {
		t.Fatalf("Lookup = %v, want 3", got)
	}
}

func TestEtlTmpDir(t *testing.T) {
	old, hadOld := os.LookupEnv("N42_ETL_TMPDIR")
	defer func() {
		if hadOld {
			os.Setenv("N42_ETL_TMPDIR", old)
		} else {
			os.Unsetenv("N42_ETL_TMPDIR")
		}
	}()

	os.Unsetenv("N42_ETL_TMPDIR")
	if got := etlTmpDir(); got != os.TempDir() {
		t.Fatalf("etlTmpDir() = %q, want OS temp dir %q", got, os.TempDir())
	}

	custom := filepath.Join(t.TempDir(), "custom-etl")
	os.Setenv("N42_ETL_TMPDIR", custom)
	if got := etlTmpDir(); got != custom {
		t.Fatalf("etlTmpDir() = %q, want %q", got, custom)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatalf("expected etlTmpDir to create the directory: %v", err)
	}
}

func TestSegmentFileName(t *testing.T) {
	got := SegmentFileName(1_000_000, 2_000_000)
	want := "txlookup-001000-002000"
	if got != want {
		t.Fatalf("SegmentFileName = %q, want %q", got, want)
	}
}
