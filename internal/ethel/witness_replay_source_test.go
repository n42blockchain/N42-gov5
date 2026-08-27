// Copyright 2022-2026 The N42 Authors
package ethel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
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

func TestN42CompactSourceHeaderRestoresParentHash(t *testing.T) {
	parent := makeTestHeader(1)
	child := makeTestHeader(2)
	parentHash := parent.Hash()
	childHash := child.Hash()

	// Model decoded compact headers: lossy fields are zero, while Hash()
	// remains authoritative because it came from the segment trailer.
	parent.ParentHash = types.Hash{}
	parent.SetHash(parentHash)
	child.ParentHash = types.Hash{}
	child.SetHash(childHash)

	src := &n42CompactSource{hr: &HeaderCompactReader{
		cachedSeg:     0,
		cachedHeaders: []*block.Header{parent, child},
	}}
	got, err := src.header(1)
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	if got.ParentHash != parentHash {
		t.Fatalf("ParentHash: got %x, want %x", got.ParentHash, parentHash)
	}
	if got.Hash() != childHash {
		t.Fatalf("Hash changed: got %x, want %x", got.Hash(), childHash)
	}
}
