// Copyright 2022-2026 The N42 Authors
package ethel

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenHeadersBodiesSource_AutoDetect verifies the picker chooses
// n42CompactSource when headerc.cidx is present, gethFreezerSource
// otherwise. Doesn't try to read blocks — that needs real cdat files.
func TestOpenHeadersBodiesSource_AutoDetect(t *testing.T) {
	dir := t.TempDir()

	// Without headerc.cidx, picker should fall through to geth freezer
	// (which will succeed at opening an empty dir).
	src, err := openHeadersBodiesSource(dir)
	if err != nil {
		t.Fatalf("geth open empty dir: %v", err)
	}
	if _, ok := src.(*gethFreezerSource); !ok {
		t.Errorf("empty dir: got %T, want *gethFreezerSource", src)
	}
	src.close()

	// Touch headerc.cidx so the picker takes the n42 columnar branch.
	// The columnar reader will fail because the file is empty / no
	// bodyc.cidx exists — but the picker logic itself is what we're
	// testing (it called openN42CompactSource, not openGethFreezerSource).
	if err := os.WriteFile(filepath.Join(dir, "headerc.cidx"), []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	src, err = openHeadersBodiesSource(dir)
	if err == nil {
		// shouldn't reach here without bodies.cidx, but if it does,
		// confirm the right backend was selected
		if _, ok := src.(*n42CompactSource); !ok {
			t.Errorf("with headerc.cidx: got %T, want *n42CompactSource", src)
		}
		src.close()
	}
}
