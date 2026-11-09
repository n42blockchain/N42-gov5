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
// (audit-notes/ci/k-cross.sh), skipped unless K_CROSS_DIR is set. The
// tombstone export is canonical, so every export below must be v2.bin byte
// for byte. It imports each fixture (v1.bin, v2.bin and the pre-canonical
// v2-tombstones.bin), exports it, persists it through a leaf store (which
// reclaims dead rows), reloads it and exports it again, and writes the
// reloaded v2-tombstones.bin import's export (what n42-qmdb-export --format
// v2 writes, the same call) to gov5-v2.bin for n42-rs to import. When
// rust-v2.bin (n42-rs's v2 export of its import of gov5-v2.bin) is there, it
// checks it is v2.bin, imports it, persists, reloads and re-exports it.
func TestPortableCrossClient(t *testing.T) {
	dir := os.Getenv("K_CROSS_DIR")
	if dir == "" {
		t.Skip("K_CROSS_DIR not set")
	}
	root := fixtureRoot(t)
	want := readPortableV2Fixture(t, "v2.bin")
	same := func(name, what string, got []byte) {
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: %s (%d bytes) differs from v2.bin (%d bytes)", name, what, len(got), len(want))
		}
		t.Logf("gov5 %s of %s equals v2.bin (%d bytes)", what, name, len(got))
	}
	persisted := func(name string, encoded []byte) []byte {
		tree, v, err := ImportPortableSnapshot(encoded, fixtureChainID, fixtureGenesis)
		if err != nil {
			t.Fatalf("%s: import: %v", name, err)
		}
		t.Logf("gov5 imported %s (%d bytes, %s, %d tombstone records): root %x", name, len(encoded), v.Format, v.Tombstones, tree.Root())
		same(name, "export before the flush", exportTombstone(t, tree))
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
		exported := exportTombstoneFrom(t, reloaded, store)
		same(name, "export after a leaf-store flush and reload", exported)
		if v, ok := reloaded.Get(fixtureKey(2049)); !ok || !bytes.Equal(v, []byte{1, 2049 % 251}) {
			t.Fatalf("%s: key 2049 reads %x %v", name, v, ok)
		}
		return exported
	}

	var gov5 []byte
	for _, name := range []string{"v1.bin", "v2.bin", "v2-tombstones.bin"} {
		gov5 = persisted(name, readPortableV2Fixture(t, name))
	}
	if err := os.WriteFile(filepath.Join(dir, "gov5-v2.bin"), gov5, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("gov5 wrote gov5-v2.bin (%d bytes)", len(gov5))

	rust, err := os.ReadFile(filepath.Join(dir, "rust-v2.bin"))
	if errors.Is(err, os.ErrNotExist) {
		t.Log("no rust-v2.bin yet")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	same("rust-v2.bin", "the n42-rs export", rust)
	persisted("rust-v2.bin", rust)
}
