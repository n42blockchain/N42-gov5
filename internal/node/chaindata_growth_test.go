// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/c2h5oh/datasize"
)

func TestChaindataGrowthStep(t *testing.T) {
	for _, c := range []struct {
		size, want datasize.ByteSize
	}{
		{0, 128 * datasize.MB},                 // fresh datadir
		{500 * datasize.MB, 128 * datasize.MB}, // small: the floor
		{4 * datasize.GB, 512 * datasize.MB},   // an eighth
		{3*datasize.GB + 7, 384 * datasize.MB}, // rounded down to whole MiB
		{64 * datasize.GB, 2 * datasize.GB},    // large: the old 2 GiB step
	} {
		if got := growthStepFor(c.size); got != c.want {
			t.Errorf("growthStepFor(%v) = %v, want %v", c.size, got, c.want)
		}
	}

	dir := t.TempDir()
	if got := chaindataGrowthStep(dir); got != 128*datasize.MB {
		t.Fatalf("missing file: %v, want 128 MiB", got)
	}
	f, err := os.Create(filepath.Join(dir, "mdbx.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(4 * datasize.GB)); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := chaindataGrowthStep(dir); got != 512*datasize.MB {
		t.Fatalf("4 GiB file: %v, want 512 MiB", got)
	}
	t.Setenv("N42_MDBX_GROWTH_MB", "64")
	if got := chaindataGrowthStep(dir); got != 64*datasize.MB {
		t.Fatalf("override: %v, want 64 MiB", got)
	}
}
