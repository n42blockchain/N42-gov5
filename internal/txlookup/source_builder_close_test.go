package txlookup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// A canceled build reaches RecSplit after it has opened its fingerprint
// scratch file. The source builder must release that file on every error.
func TestBuildSegmentSourceClosesRecSplitOnCanceledBuild(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("N42_ETL_TMPDIR", tmp)
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	_, err := BuildSegmentFileAnchored(ctx, dir, 1, 2, func(uint64) ([]types.Hash, error) {
		cancel()
		return []types.Hash{{1}}, nil
	}, SegmentAnchor{Identity: types.Hash{1}, EndCanonicalHash: types.Hash{2}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build error = %v", err)
	}
	files, err := filepath.Glob(filepath.Join(tmp, "erigon-lfp-buf-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("RecSplit scratch files survived canceled build: %v", files)
	}
	if _, err := os.Stat(filepath.Join(dir, "txindex.cidx")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled segment was published: %v", err)
	}
}
