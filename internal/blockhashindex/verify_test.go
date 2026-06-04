package blockhashindex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
)

// tinySeg builds a 1-key RecSplit segment (LessFalsePositives off → any
// out-of-set hash phantom-hits the single slot) with `key` at relBlock 0,
// BaseDataID=base, and returns its loaded segCached.
func tinySeg(t *testing.T, dir string, base uint64, key types.Hash) *segCached {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(dir, "seg.idx")
	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:           1,
		BucketSize:         2000,
		LeafSize:           8,
		Enums:              false,
		LessFalsePositives: false,
		IndexFile:          idxPath,
		BaseDataID:         base,
		TmpDir:             dir,
	}, log2.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.AddKey(key[:], 0); err != nil {
		t.Fatal(err)
	}
	if err := rs.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	rs.Close()
	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	return &segCached{
		startBlock: base,
		idx:        idx,
		reader:     recsplit.NewIndexReader(idx),
		keyCount:   idx.KeyCount(),
		loaded:     true,
	}
}

// TestBlockHashVerifyAndContinue proves the self-verifying lookup: with no
// fingerprint, a newer segment phantom-hits a hash that belongs to an older
// segment; the header-hash verifier rejects the phantom and the probe continues
// to the right (older) block.
func TestBlockHashVerifyAndContinue(t *testing.T) {
	dir := t.TempDir()
	hOld := types.Hash{0x01, 0x02} // truly block 5 (segment base 0)
	hNew := types.Hash{0xaa, 0xbb} // truly block 1,000,000 (segment base 1M)
	const oldBlock = uint64(5)
	const newBlock = SegmentSize

	segOld := tinySeg(t, filepath.Join(dir, "a"), oldBlock, hOld)
	segNew := tinySeg(t, filepath.Join(dir, "b"), newBlock, hNew)
	defer segOld.idx.Close()
	defer segNew.idx.Close()

	// Newest-first: segNew probed before segOld.
	svc := &Service{segments: []*segCached{segNew, segOld}}

	// Truth table for the verifier (the only blocks that hold each hash).
	truth := map[types.Hash]uint64{hOld: oldBlock, hNew: newBlock}
	svc.SetVerifier(func(blockNum uint64, h types.Hash) bool {
		want, ok := truth[h]
		return ok && want == blockNum
	})

	if got := svc.Lookup(hOld); got == nil || *got != oldBlock {
		t.Fatalf("hOld: expected block %d, got %v", oldBlock, got)
	}
	if got := svc.Lookup(hNew); got == nil || *got != newBlock {
		t.Fatalf("hNew: expected block %d, got %v", newBlock, got)
	}
	// Absent hash: every segment phantoms, verifier rejects all → nil.
	if got := svc.Lookup(types.Hash{0xde, 0xad}); got != nil {
		t.Fatalf("absent: expected nil, got %d", *got)
	}

	// Without a verifier the newest segment's phantom wins for hOld (the hazard).
	svc.SetVerifier(nil)
	if got := svc.Lookup(hOld); got == nil || *got == oldBlock {
		t.Fatalf("no-verifier: expected phantom (wrong) block, got %v", got)
	}
}
