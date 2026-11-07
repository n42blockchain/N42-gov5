// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestPortableCrossClient is gov5's half of n42-rs's cross-client check
// (audit-notes/ci/k-cross.sh), skipped unless K_CROSS_DIR is set. It imports
// v2-tombstones.bin, persists it through a leaf store and reloads it, as a
// node that started from the snapshot would, and writes that tree's
// whole-dead-twig tombstone export (what n42-qmdb-export --format v2
// --whole-dead-twigs writes, the same call) to gov5-whole.bin for n42-rs to
// import. When rust-v2.bin (n42-rs's v2 export of its import of v2.bin) is
// there, it imports that too, persists and reloads it, and checks the root.
func TestPortableCrossClient(t *testing.T) {
	dir := os.Getenv("K_CROSS_DIR")
	if dir == "" {
		t.Skip("K_CROSS_DIR not set")
	}
	root := fixtureRoot(t)
	persisted := func(name string, encoded []byte) (*Tree, mapStore) {
		tree, v, err := ImportPortableSnapshot(encoded, fixtureChainID, fixtureGenesis)
		if err != nil {
			t.Fatalf("%s: import: %v", name, err)
		}
		t.Logf("gov5 imported %s (%d bytes, %s, %d tombstone records): root %x", name, len(encoded), v.Format, v.Tombstones, tree.Root())
		store := newMapStore()
		tree.SetLeafStore(LeafStoreFromGetter(store))
		if _, _, err := tree.FlushTo(store, 0); err != nil {
			t.Fatalf("%s: flush: %v", name, err)
		}
		tree.CommitFlush()
		reloaded := New()
		if err := reloaded.LoadFrom(store); err != nil {
			t.Fatalf("%s: reload: %v", name, err)
		}
		if reloaded.Root() != root {
			t.Fatalf("%s: reloaded root %x, want %x", name, reloaded.Root(), root)
		}
		t.Logf("gov5 flushed %s through a leaf store and reloaded it: root %x", name, reloaded.Root())
		return reloaded, store
	}

	tree, store := persisted("v2-tombstones.bin", readPortableV2Fixture(t, "v2-tombstones.bin"))
	whole := exportTombstoneFrom(t, tree, store, true)
	if err := os.WriteFile(filepath.Join(dir, "gov5-whole.bin"), whole, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("gov5 wrote gov5-whole.bin (%d bytes, equal to v2.bin with dead payload rows reclaimed: %v)",
		len(whole), bytes.Equal(whole, reclaimedRows(t, readPortableV2Fixture(t, "v2.bin"))))

	rust, err := os.ReadFile(filepath.Join(dir, "rust-v2.bin"))
	if errors.Is(err, os.ErrNotExist) {
		t.Log("no rust-v2.bin yet")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rust-v2.bin equals v2.bin: %v", bytes.Equal(rust, readPortableV2Fixture(t, "v2.bin")))
	reloaded, _ := persisted("rust-v2.bin", rust)
	if v, ok := reloaded.Get(fixtureKey(2049)); !ok || !bytes.Equal(v, []byte{1, 2049 % 251}) {
		t.Fatalf("rust-v2.bin: key 2049 reads %x %v", v, ok)
	}
}
