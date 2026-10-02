package coldstore

import (
	"bytes"
	"os"
	"testing"
)

// ---------- format.go ----------

func TestCovHeaderMarshalRoundTrip(t *testing.T) {
	h := FileHeader{Magic: KVMagic, Version: Version, KeyLen: 52, PageSize: 64, Reserved: 0}
	b := h.Marshal()
	if len(b) != HeaderSize {
		t.Fatalf("marshal len = %d, want %d", len(b), HeaderSize)
	}
	got, err := UnmarshalHeader(b)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got != h {
		t.Fatalf("round trip mismatch: got %+v want %+v", got, h)
	}
}

func TestCovUnmarshalHeaderTruncated(t *testing.T) {
	if _, err := UnmarshalHeader(make([]byte, HeaderSize-1)); err == nil {
		t.Fatal("expected error for truncated header")
	}
}

func TestCovEncodeDecodeValueClasses(t *testing.T) {
	cases := [][]byte{
		{},                        // L==0
		{0x42},                    // L==1
		bytes.Repeat([]byte{1}, 2),
		bytes.Repeat([]byte{2}, 14),
		bytes.Repeat([]byte{3}, 15), // edge
		bytes.Repeat([]byte{4}, 16),
		bytes.Repeat([]byte{5}, 32),
	}
	for _, v := range cases {
		enc := EncodeValue(nil, v)
		dec, pos, err := DecodeValue(enc, 0)
		if err != nil {
			t.Fatalf("decode len=%d: %v", len(v), err)
		}
		if pos != len(enc) {
			t.Fatalf("decode len=%d: pos=%d want %d", len(v), pos, len(enc))
		}
		if len(v) == 0 {
			if len(dec) != 0 {
				t.Fatalf("expected empty decode, got %v", dec)
			}
			continue
		}
		if !bytes.Equal(dec, v) {
			t.Fatalf("decode mismatch: got %x want %x", dec, v)
		}
	}
}

func TestCovEncodeValueOversize(t *testing.T) {
	v := bytes.Repeat([]byte{9}, 40)
	enc := EncodeValue(nil, v)
	dec, pos, err := DecodeValue(enc, 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pos != len(enc) {
		t.Fatalf("pos mismatch")
	}
	if !bytes.Equal(dec, v[:32]) {
		t.Fatalf("expected truncated to 32B")
	}
}

func TestCovDecodeValueErrors(t *testing.T) {
	// truncated tag (empty data)
	if _, _, err := DecodeValue([]byte{}, 0); err == nil {
		t.Fatal("expected truncated tag error")
	}
	// tag 0x01 but no data byte following
	if _, _, err := DecodeValue([]byte{0x01}, 0); err == nil {
		t.Fatal("expected truncated 1B value error")
	}
	// class-1 invalid len 0 (tag 0x40 | 0)
	if _, _, err := DecodeValue([]byte{0x40}, 0); err == nil {
		t.Fatal("expected invalid class-1 len error")
	}
	// class-1 truncated: tag says L=5 but no data
	if _, _, err := DecodeValue([]byte{0x45}, 0); err == nil {
		t.Fatal("expected truncated class-1 value error")
	}
	// class-2 truncated: tag 0x80 says L=16 but no data
	if _, _, err := DecodeValue([]byte{0x80}, 0); err == nil {
		t.Fatal("expected truncated class-2 value error")
	}
	// unknown tag: class 3 (0xC0) is "default" branch -> unknown tag
	if _, _, err := DecodeValue([]byte{0xC0}, 0); err == nil {
		t.Fatal("expected unknown tag error")
	}
}

// ---------- writer.go / reader.go ----------

func TestCovWriterNewBadParams(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewWriter(dir, "p", 0, 64); err == nil {
		t.Fatal("expected error for keyLen 0")
	}
	if _, err := NewWriter(dir, "p", 300, 64); err == nil {
		t.Fatal("expected error for keyLen too large")
	}
	if _, err := NewWriter(dir, "p", 52, 0); err == nil {
		t.Fatal("expected error for pageSize 0")
	}
	if _, err := NewWriter(dir, "p", 52, 70000); err == nil {
		t.Fatal("expected error for pageSize too large")
	}
}

func TestCovWriterNewBadDir(t *testing.T) {
	if _, err := NewWriter("/nonexistent/dir/for/coldstore/test", "p", 52, 64); err == nil {
		t.Fatal("expected error for bad dir")
	}
}

func mkKey(i int, keyLen int) []byte {
	k := make([]byte, keyLen)
	// big-endian encode i into the last bytes to keep ascending order
	for b := 0; b < 8 && b < keyLen; b++ {
		k[keyLen-1-b] = byte(i >> (8 * b))
	}
	return k
}

func TestCovWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	const keyLen = 12
	const pageSize = 4
	w, err := NewWriter(dir, "dom", keyLen, pageSize)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	const n = 23 // not a multiple of pageSize, exercises partial trailing page
	keys := make([][]byte, n)
	vals := make([][]byte, n)
	for i := 0; i < n; i++ {
		keys[i] = mkKey(i, keyLen)
		switch i % 4 {
		case 0:
			vals[i] = nil // empty value
		case 1:
			vals[i] = []byte{byte(i)}
		case 2:
			vals[i] = bytes.Repeat([]byte{byte(i)}, 10)
		case 3:
			vals[i] = bytes.Repeat([]byte{byte(i)}, 32)
		}
		if err := w.Append(keys[i], vals[i]); err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
	}

	stats := w.Stats()
	if stats.KeyCount != n {
		t.Fatalf("KeyCount = %d, want %d", stats.KeyCount, n)
	}
	if stats.PageCount == 0 {
		t.Fatalf("expected at least one page flushed before Close")
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := Open(dir, "dom")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	if r.KeyLen() != keyLen {
		t.Fatalf("KeyLen = %d, want %d", r.KeyLen(), keyLen)
	}
	wantPages := (n + pageSize - 1) / pageSize
	if r.PageCount() != wantPages {
		t.Fatalf("PageCount = %d, want %d", r.PageCount(), wantPages)
	}

	for i := 0; i < n; i++ {
		got, ok, err := r.Get(keys[i])
		if err != nil {
			t.Fatalf("Get(%d): %v", i, err)
		}
		if !ok {
			t.Fatalf("Get(%d): not found", i)
		}
		if !bytes.Equal(got, vals[i]) {
			t.Fatalf("Get(%d) = %x, want %x", i, got, vals[i])
		}
	}

	// Lookup of an absent key smaller than all keys.
	if _, ok, err := r.Get(mkKey(-1, keyLen)); err != nil || ok {
		t.Fatalf("Get(below-range) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// Lookup of an absent key between two present keys (odd placement).
	mid := mkKey(n*1000, keyLen) // way above range
	if _, ok, err := r.Get(mid); err != nil || ok {
		t.Fatalf("Get(above-range) = ok=%v err=%v, want ok=false err=nil", ok, err)
	}

	// Wrong key length.
	if _, _, err := r.Get(make([]byte, keyLen+1)); err == nil {
		t.Fatal("expected error for wrong key length on Get")
	}
}

func TestCovAppendWrongKeyLen(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, "dom", 8, 4)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	if err := w.Append(make([]byte, 7), []byte{1}); err == nil {
		t.Fatal("expected error for wrong key length")
	}
}

func TestCovAppendNotAscending(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, "dom", 4, 4)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()
	k1 := []byte{0, 0, 0, 5}
	k2 := []byte{0, 0, 0, 3}
	if err := w.Append(k1, []byte{1}); err != nil {
		t.Fatalf("Append k1: %v", err)
	}
	if err := w.Append(k2, []byte{2}); err == nil {
		t.Fatal("expected error for non-ascending key")
	}
	// equal key also rejected
	if err := w.Append(k1, []byte{3}); err == nil {
		t.Fatal("expected error for duplicate key")
	}
}

func TestCovEmptyWriterCloseAndOpen(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, "empty", 8, 4)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	r, err := Open(dir, "empty")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	if r.PageCount() != 0 {
		t.Fatalf("PageCount = %d, want 0", r.PageCount())
	}
	_, ok, err := r.Get(make([]byte, 8))
	if err != nil {
		t.Fatalf("Get on empty store: %v", err)
	}
	if ok {
		t.Fatal("expected not found on empty store")
	}
}

func TestCovOpenMissingFiles(t *testing.T) {
	dir := t.TempDir()
	if _, err := Open(dir, "nope"); err == nil {
		t.Fatal("expected error opening missing idx file")
	}
}

func TestCovOpenCorruptIdx(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, "dom", 8, 4)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.Append(make([]byte, 8), []byte{1}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Truncate idx file below header size.
	idxPath := dir + "/dom.idx"
	if err := writeFile(idxPath, make([]byte, HeaderSize-1)); err != nil {
		t.Fatalf("truncate idx: %v", err)
	}
	if _, err := Open(dir, "dom"); err == nil {
		t.Fatal("expected error for too-short idx")
	}
}

func writeFile(path string, b []byte) error {
	return os.WriteFile(path, b, 0o644)
}
