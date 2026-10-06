// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"bytes"
	"encoding/binary"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// copyTree copies every regular file under src into dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// treeFiles maps relative path → content for every file under dir.
func treeFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		b, _ := os.ReadFile(p)
		out[rel] = b
		return nil
	})
	return out
}

// spillRows appends rows to dir's spill: a few buckets, bulky values, and keys
// repeated across batches (the duplicate-key case a resumed build produces).
func spillRows(t *testing.T, dir string, seed uint64, n int) {
	t.Helper()
	w, err := newLeafSpillWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	rng := seed
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }
	for i := 0; i < n; i++ {
		k := make([]byte, 36)
		k[0] = byte(next() % 3)
		binary.BigEndian.PutUint64(k[1:], next()%700) // few distinct keys → many duplicates
		binary.BigEndian.PutUint32(k[32:], uint32(next()%4))
		v := make([]byte, 30+int(next()%90))
		for j := range v {
			v[j] = byte(next())
		}
		for _, table := range []int{leafTableA, leafTableS} {
			if err := w.add(table, k, v); err != nil {
				t.Fatal(err)
			}
		}
		if i%997 == 0 {
			if err := w.flushBatch(); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.close(); err != nil {
		t.Fatal(err)
	}
}

// appendKillTail appends half of a valid zstd frame to a spill file, as a hard
// kill mid-batch leaves it.
func appendKillTail(t *testing.T, path string) {
	t.Helper()
	enc, _ := zstd.NewWriter(nil)
	defer enc.Close()
	var rows []byte
	for i := 0; i < 500; i++ {
		k := bytes.Repeat([]byte{byte(i)}, 36)
		rows = binary.AppendUvarint(rows, uint64(len(k)))
		rows = append(rows, k...)
		rows = binary.AppendUvarint(rows, 8)
		rows = append(rows, bytes.Repeat([]byte{0xee}, 8)...)
	}
	frame := enc.EncodeAll(rows, nil)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(frame[:len(frame)/2]); err != nil {
		t.Fatal(err)
	}
}

func finalizeWithRuns(t *testing.T, dir string, runBytes int) {
	t.Helper()
	saved := finalizeRunBytes
	finalizeRunBytes = runBytes
	defer func() { finalizeRunBytes = saved }()
	if err := finalizeLeafSegments(dir); err != nil {
		t.Fatal(err)
	}
	if n, _ := filepath.Glob(filepath.Join(dir, leafSegDir, "*.run*.tmp")); len(n) != 0 {
		t.Fatalf("run files left behind: %v", n)
	}
}

func requireSameTrees(t *testing.T, want, got string, stage string) {
	t.Helper()
	a, b := treeFiles(t, want), treeFiles(t, got)
	if len(a) != len(b) {
		t.Fatalf("%s: %d files in-memory vs %d external", stage, len(a), len(b))
	}
	for name, content := range a {
		if !bytes.Equal(content, b[name]) {
			t.Fatalf("%s: %s differs between in-memory and external finalize", stage, name)
		}
	}
}

// TestFinalizeExternalSortMatchesInMemory finalizes the same spill once in a
// single in-memory run and once through many small runs, including a kill-tail
// frame, a resumed stream after it, duplicate keys, and a later spill merged
// into the existing segments. Both must produce byte-identical files.
func TestFinalizeExternalSortMatchesInMemory(t *testing.T) {
	base := t.TempDir()
	src := filepath.Join(base, "src")
	spillRows(t, src, 7, 12000)
	killed := filepath.Join(src, leafSpillDir, segFileName(leafTableS, 1)+".zspill")
	appendKillTail(t, killed)
	spillRows(t, src, 11, 3000) // the resumed build appends a fresh stream after the truncated frame

	mem := filepath.Join(base, "mem")
	ext := filepath.Join(base, "ext")
	copyTree(t, src, mem)
	copyTree(t, src, ext)
	finalizeWithRuns(t, mem, 1<<40)
	finalizeWithRuns(t, ext, 64<<10)
	if _, err := os.Stat(filepath.Join(ext, leafSpillDir, filepath.Base(killed))); err != nil {
		t.Fatalf("kill-tail bucket spill not retained: %v", err)
	}
	requireSameTrees(t, mem, ext, "first finalize")

	// A resumed build finalizes again on top of existing segments.
	for _, d := range []string{mem, ext} {
		_ = os.RemoveAll(filepath.Join(d, leafSpillDir))
		spillRows(t, d, 13, 5000)
	}
	finalizeWithRuns(t, mem, 1<<40)
	finalizeWithRuns(t, ext, 32<<10)
	requireSameTrees(t, mem, ext, "merge into existing segments")
}
