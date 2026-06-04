package txlookup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
)

// buildTinySegment writes a 1-key RecSplit segment (LessFalsePositives OFF, so
// any out-of-set hash phantom-hits the single slot) with `key` living at
// startBlock+relBlock, and opens it. Data-free — no geth freezer needed.
func buildTinySegment(t *testing.T, dir string, key types.Hash, base, relBlock uint64) *TxSegment {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(dir, "seg.idx")
	datPath := filepath.Join(dir, "seg.dat")

	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:           1,
		BucketSize:         2000,
		LeafSize:           8,
		Enums:              false,
		LessFalsePositives: false, // no existence fp → out-of-set hashes phantom-hit
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

	blockCount := relBlock + 1
	txPerBlock := make([]uint32, blockCount)
	txPerBlock[relBlock] = 1
	if err := writeDatV2(datPath, blockCount, 1, txPerBlock); err != nil {
		t.Fatal(err)
	}
	seg, err := OpenSegment(idxPath, datPath)
	if err != nil {
		t.Fatal(err)
	}
	return seg
}

// TestVerifyAndContinueRejectsPhantom proves that, with LessFalsePositives off,
// the newest-first probe phantom-hits a tx that lives in an older segment, and
// that the verifier makes the lookup skip that phantom and return the correct
// older block.
func TestVerifyAndContinueRejectsPhantom(t *testing.T) {
	dir := t.TempDir()
	hashOld := types.Hash{0x01, 0x02, 0x03}              // truly in segment A (base 0)
	hashNew := types.Hash{0xaa, 0xbb, 0xcc}              // truly in segment B (base 1M)
	const oldBlock = uint64(5)
	const newBlock = SegmentSize + 5

	segA := buildTinySegment(t, filepath.Join(dir, "a"), hashOld, 0, 5)
	segB := buildTinySegment(t, filepath.Join(dir, "b"), hashNew, SegmentSize, 5)
	defer segA.Close()
	defer segB.Close()

	// Service: segment B (newest) probed before A — same order NewService builds.
	svc := &Service{segments: []*txSegmentCached{
		{segNum: 1, startBlock: SegmentSize, seg: segB},
		{segNum: 0, startBlock: 0, seg: segA},
	}}

	// Sanity: B really does phantom-hit hashOld (returns its own block, not 5).
	if r := segB.Lookup(hashOld); r == nil || *r == oldBlock {
		t.Fatalf("expected segment B to phantom-hit hashOld with a wrong block, got %v", r)
	}

	// Without a verifier (legacy): the newest-first probe returns B's phantom
	// block for hashOld — the exact hazard of an LFP-off index.
	got, err := svc.Lookup(nil, hashOld)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got == oldBlock {
		t.Fatalf("no-verifier: expected the wrong (phantom) block, got %v — phantom not reproduced", got)
	}

	// With the verifier installed, the phantom is rejected and the probe
	// continues to segment A, returning the correct block.
	truth := map[types.Hash]uint64{hashOld: oldBlock, hashNew: newBlock}
	svc.SetVerifier(func(blk uint64, h types.Hash) (uint64, bool) {
		want, ok := truth[h]
		return 0, ok && want == blk
	})

	got, err = svc.Lookup(nil, hashOld)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != oldBlock {
		t.Fatalf("verify-and-continue: hashOld expected block %d, got %v", oldBlock, got)
	}

	got, err = svc.Lookup(nil, hashNew)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != newBlock {
		t.Fatalf("verify-and-continue: hashNew expected block %d, got %v", newBlock, got)
	}

	// A genuinely absent hash: every segment phantoms, the verifier rejects all,
	// Lookup returns nil (not found) instead of a wrong block.
	absent := types.Hash{0xde, 0xad, 0xbe, 0xef}
	got, err = svc.Lookup(nil, absent)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("absent hash: expected nil, got block %d", *got)
	}
}
