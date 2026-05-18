package history

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestWriterReaderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	prefix := "test"
	keyLen := 20
	pageSize := 4

	// Build a small history dataset
	keys := make([][]byte, 0)
	histories := make(map[string][]Change)
	for i := 0; i < 10; i++ {
		k := make([]byte, keyLen)
		k[0] = byte(i)
		k[19] = 0xaa
		keys = append(keys, k)
		entries := []Change{
			{Block: uint64(100 + i*10), Value: []byte{byte(i), 0x42}},
			{Block: uint64(5000 + i*100), Value: bytes.Repeat([]byte{byte(i)}, 10+i)},
		}
		histories[string(k)] = entries
	}

	w, err := NewWriter(dir, prefix, keyLen, pageSize)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	for _, k := range keys {
		packed := PackHistory(nil, histories[string(k)])
		if err := w.Append(k, packed); err != nil {
			t.Fatalf("Append %x: %v", k, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	stats := w.Stats()
	t.Logf("Stats: %d keys, %d pages, kv=%d B, val=%d B", stats.KeyCount, stats.PageCount, stats.TotalKvSize, stats.TotalValSize)

	r, err := Open(dir, prefix)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	if r.PageCount() != 3 { // 10 keys / 4 per page = 3 pages (2 full + 1 partial)
		t.Errorf("PageCount: got %d, want 3", r.PageCount())
	}

	for _, k := range keys {
		blob, ok, err := r.Get(k)
		if err != nil {
			t.Errorf("Get %x: %v", k, err)
			continue
		}
		if !ok {
			t.Errorf("Get %x: not found", k)
			continue
		}
		unpacked, err := UnpackHistory(blob)
		if err != nil {
			t.Errorf("Unpack %x: %v", k, err)
			continue
		}
		want := histories[string(k)]
		if len(unpacked) != len(want) {
			t.Errorf("Get %x: got %d entries, want %d", k, len(unpacked), len(want))
			continue
		}
		for i := range want {
			if unpacked[i].Block != want[i].Block {
				t.Errorf("Get %x entry %d block: got %d, want %d", k, i, unpacked[i].Block, want[i].Block)
			}
			if !bytes.Equal(unpacked[i].Value, want[i].Value) {
				t.Errorf("Get %x entry %d value: got %x, want %x", k, i, unpacked[i].Value, want[i].Value)
			}
		}
	}

	// Negative lookup
	missing := make([]byte, keyLen)
	missing[0] = 0xff
	_, ok, err := r.Get(missing)
	if err != nil {
		t.Errorf("Get missing: err %v", err)
	}
	if ok {
		t.Errorf("Get missing: unexpectedly found")
	}
}

func TestWriterRejectsUnsorted(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, "test", 4, 16)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append([]byte{0x02, 0, 0, 0}, []byte{1}); err != nil {
		t.Fatal(err)
	}
	if err := w.Append([]byte{0x01, 0, 0, 0}, []byte{2}); err == nil {
		t.Errorf("Append unsorted: expected error, got nil")
	}
	w.Close()
}

func TestWriterRejectsBadKeyLen(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, "test", 4, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.Append([]byte{0x01, 0, 0}, []byte{1}); err == nil {
		t.Errorf("Append short key: expected error")
	}
}

func TestFileLayout(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, "layout", 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append([]byte{0, 0, 0, 1}, []byte{42}); err != nil {
		t.Fatal(err)
	}
	w.Close()

	for _, ext := range []string{".kv", ".idx"} {
		p := filepath.Join(dir, "layout"+ext)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if info.Size() <= int64(HeaderLen) {
			t.Errorf("%s smaller than header: %d", ext, info.Size())
		}
	}

	// Reader rejects swapped files (kv magic in idx position).
	bogusDir := t.TempDir()
	src, _ := os.ReadFile(filepath.Join(dir, "layout.kv"))
	os.WriteFile(filepath.Join(bogusDir, "swap.idx"), src, 0644)
	os.WriteFile(filepath.Join(bogusDir, "swap.kv"), src, 0644)
	if _, err := Open(bogusDir, "swap"); err == nil {
		t.Errorf("expected magic-mismatch error")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

func TestLargeValueBlob(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, "big", 20, 4)
	if err != nil {
		t.Fatal(err)
	}
	// One key, 100 changes — packed blob ~1.5 KB
	var entries []Change
	for i := 0; i < 100; i++ {
		entries = append(entries, Change{
			Block: uint64(i) * 100_000,
			Value: bytes.Repeat([]byte{byte(i)}, 10),
		})
	}
	k := make([]byte, 20)
	k[0] = 0xaa
	packed := PackHistory(nil, entries)
	if err := w.Append(k, packed); err != nil {
		t.Fatal(err)
	}
	w.Close()

	r, err := Open(dir, "big")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, ok, err := r.Get(k)
	if err != nil || !ok {
		t.Fatalf("Get big: err=%v ok=%v", err, ok)
	}
	if !bytes.Equal(got, packed) {
		t.Errorf("blob roundtrip failed")
	}
	t.Logf("packed %d bytes, on-disk page compressed", len(packed))
	fmt.Println(len(packed))
}
