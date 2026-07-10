// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// buildTestCkpt writes n sorted keyLen-byte keys through the v2 writer and
// returns them.
func buildTestCkpt(t *testing.T, path string, keyLen, n int, seed int64) [][]byte {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	keys := make([][]byte, 0, n)
	seen := map[string]bool{}
	for len(keys) < n {
		k := make([]byte, keyLen)
		rng.Read(k)
		if seen[string(k)] {
			continue
		}
		seen[string(k)] = true
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })

	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	w, err := newCkptWriter(path, keyLen, enc)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if err := w.add(k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.close(); err != nil {
		t.Fatal(err)
	}
	return keys
}

func openTestCkptStore(t *testing.T, dir string) *ckptStore {
	t.Helper()
	// openCkptStore expects <dir>/ckpt; callers pass the parent. -1 = no gate.
	st := openCkptStore(dir, -1)
	t.Cleanup(st.Close)
	return st
}

func TestCkptMaxBlockGate(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ckptDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// Three checkpoints of growing size: 1K, 10K, 40K keys.
	sizes := map[uint64]int{1000: 1000, 2000: 10000, 3000: 40000}
	for b, n := range sizes {
		buildTestCkpt(t, filepath.Join(dir, ckptDir, "0."+strconvFormatUint(b)+".ckpt"), 32, n, int64(b))
	}

	// Hard cutoff: keep blocks ≤ 2000.
	st := openCkptStore(dir, 2000)
	t.Cleanup(st.Close)
	if got := st.blocks[0]; len(got) != 2 || got[0] != 1000 || got[1] != 2000 {
		t.Fatalf("hard cutoff: got %v, want [1000 2000]", got)
	}

	// No gate: all three.
	st2 := openCkptStore(dir, -1)
	t.Cleanup(st2.Close)
	if got := st2.blocks[0]; len(got) != 3 {
		t.Fatalf("no gate: got %v, want 3 blocks", got)
	}

	// Auto gate: threshold between 10K and 40K keys excludes only block 3000.
	old := autoCkptMaxKeys
	autoCkptMaxKeys = 20000
	defer func() { autoCkptMaxKeys = old }()
	st3 := openCkptStore(dir, 0)
	t.Cleanup(st3.Close)
	if got := st3.blocks[0]; len(got) != 2 || got[1] != 2000 {
		t.Fatalf("auto gate: got %v, want [1000 2000]", got)
	}
}

func strconvFormatUint(b uint64) string { return fmt.Sprintf("%d", b) }

func TestCkptRoundTripPrefixIter(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ckptDir), 0o755); err != nil {
		t.Fatal(err)
	}
	const keyLen, n = 32, 50000 // 50K × 32B = 1.5 MB raw ⇒ several 256 KiB frames
	path := filepath.Join(dir, ckptDir, "0.1000.ckpt")
	keys := buildTestCkpt(t, path, keyLen, n, 1)

	st := openTestCkptStore(t, dir)
	if !st.available(0) || st.maxBlock(0) != 1000 {
		t.Fatalf("store did not pick up the checkpoint: %v", st.blocks)
	}
	s, err := st.load(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.frames) < 2 {
		t.Fatalf("expected multiple frames, got %d", len(s.frames))
	}

	collect := func(prefix []byte) [][]byte {
		it := s.iterUnder(prefix)
		var got [][]byte
		for {
			k, ok, err := it.next()
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			got = append(got, append([]byte{}, k...))
		}
		return got
	}

	// Whole-set iteration (empty prefix) must return every key in order.
	got := collect(nil)
	if len(got) != len(keys) {
		t.Fatalf("full iter: got %d keys, want %d", len(got), len(keys))
	}
	for i := range got {
		if !bytes.Equal(got[i], keys[i]) {
			t.Fatalf("full iter: key %d mismatch", i)
		}
	}

	// Prefix queries (incl. frame-boundary keys) vs naive filtering.
	prefixes := [][]byte{{0x00}, {0x7f}, {0xff}, {0xab, 0xcd}, keys[0][:3], keys[n/2][:2], keys[n-1][:3]}
	for _, fm := range s.frames {
		prefixes = append(prefixes, fm.firstKey[:2])
	}
	for _, p := range prefixes {
		var want [][]byte
		for _, k := range keys {
			if bytes.HasPrefix(k, p) {
				want = append(want, k)
			}
		}
		got := collect(p)
		if len(got) != len(want) {
			t.Fatalf("prefix %x: got %d keys, want %d", p, len(got), len(want))
		}
		for i := range got {
			if !bytes.Equal(got[i], want[i]) {
				t.Fatalf("prefix %x: key %d mismatch", p, i)
			}
		}
	}
}

func TestCkptFrameCacheEviction(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ckptDir), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ckptDir, "0.500.ckpt")
	buildTestCkpt(t, path, 32, 50000, 2)

	st := openTestCkptStore(t, dir)
	st.fcap = 300 << 10 // ~1 frame: force eviction
	s, err := st.load(0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for i := range s.frames {
		if _, err := s.frame(i); err != nil {
			t.Fatal(err)
		}
		if st.fbytes > st.fcap+int64(ckptFrameRaw) {
			t.Fatalf("cache bytes %d exceed cap %d by more than one frame", st.fbytes, st.fcap)
		}
	}
	// Re-read after eviction must still work.
	if _, err := s.frame(0); err != nil {
		t.Fatal(err)
	}
}

func TestCkptRejectsLegacyV1(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ckptDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// v1 = one bare zstd stream of keys, no framing/footer.
	path := filepath.Join(dir, ckptDir, "1.2000.ckpt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 72*100)
	rand.New(rand.NewSource(3)).Read(raw)
	if _, err := enc.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	st := openTestCkptStore(t, dir)
	if st.available(1) {
		t.Fatalf("legacy v1 file must be skipped at open, got blocks %v", st.blocks[1])
	}
}

func TestCkptEmptySet(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ckptDir), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ckptDir, "0.50.ckpt")
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	w, err := newCkptWriter(path, 32, enc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.close(); err != nil {
		t.Fatal(err)
	}

	st := openTestCkptStore(t, dir)
	s, err := st.load(0, 50)
	if err != nil {
		t.Fatal(err)
	}
	it := s.iterUnder([]byte{0x00})
	if _, ok, err := it.next(); err != nil || ok {
		t.Fatalf("empty set must iterate nothing (ok=%v err=%v)", ok, err)
	}
}

// sanity: the footer round-trips exact frame extents (offsets add up).
func TestCkptFooterOffsets(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ckptDir), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ckptDir, "0.10.ckpt")
	buildTestCkpt(t, path, 32, 30000, 4)
	st := openTestCkptStore(t, dir)
	s, err := st.load(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := s.f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	var tail [16]byte
	if _, err := s.f.ReadAt(tail[:], fi.Size()-16); err != nil {
		t.Fatal(err)
	}
	footLen := int64(binary.BigEndian.Uint64(tail[:8]))
	last := s.frames[len(s.frames)-1]
	if last.off+int64(last.comp) != fi.Size()-16-footLen {
		t.Fatalf("frame extents (%d) do not meet the footer start (%d)",
			last.off+int64(last.comp), fi.Size()-16-footLen)
	}
}
