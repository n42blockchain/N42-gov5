package ed2k

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestHashEmpty(t *testing.T) {
	h := Hash(nil)
	// Empty => MD4 of empty
	expected := "31d6cfe0d16ae931b73c59d7e0c089c0"
	got := hex.EncodeToString(h[:])
	if got != expected {
		t.Fatalf("Hash(nil) = %s, want %s", got, expected)
	}
}

func TestHashSmallData(t *testing.T) {
	data := []byte("hello ed2k")
	h := Hash(data)
	// Small data fits in one chunk, so ed2k hash = MD4(data)
	md4 := NewMD4()
	md4.Write(data)
	expected := hex.EncodeToString(md4.Sum(nil))
	got := hex.EncodeToString(h[:])
	if got != expected {
		t.Fatalf("Hash(small) = %s, want %s", got, expected)
	}
}

func TestHashExactChunk(t *testing.T) {
	// Exactly one chunk (9728000 bytes) — should be single MD4
	data := make([]byte, ChunkSize)
	for i := range data {
		data[i] = byte(i % 256)
	}
	h := Hash(data)
	md4 := NewMD4()
	md4.Write(data)
	expected := hex.EncodeToString(md4.Sum(nil))
	got := hex.EncodeToString(h[:])
	if got != expected {
		t.Fatalf("Hash(exact chunk) = %s, want %s", got, expected)
	}
}

func TestHashTwoChunks(t *testing.T) {
	// ChunkSize + 1 byte = 2 chunks
	data := make([]byte, ChunkSize+1)
	for i := range data {
		data[i] = byte(i % 256)
	}
	h := Hash(data)

	// Manual computation
	h1 := NewMD4()
	h1.Write(data[:ChunkSize])
	hash1 := h1.Sum(nil)

	h2 := NewMD4()
	h2.Write(data[ChunkSize:])
	hash2 := h2.Sum(nil)

	combined := append(hash1, hash2...)
	hFinal := NewMD4()
	hFinal.Write(combined)
	expected := hex.EncodeToString(hFinal.Sum(nil))
	got := hex.EncodeToString(h[:])
	if got != expected {
		t.Fatalf("Hash(2 chunks) = %s, want %s", got, expected)
	}
}

func TestHashReader(t *testing.T) {
	data := []byte("test data for hash reader")
	h1 := Hash(data)

	r := bytes.NewReader(data)
	h2, size, err := HashReader(r)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(data)) {
		t.Fatalf("size = %d, want %d", size, len(data))
	}
	if h1 != h2 {
		t.Fatalf("HashReader mismatch: %s vs %s",
			hex.EncodeToString(h1[:]), hex.EncodeToString(h2[:]))
	}
}

func TestHashReaderEmpty(t *testing.T) {
	r := bytes.NewReader(nil)
	h, size, err := HashReader(r)
	if err != nil {
		t.Fatal(err)
	}
	if size != 0 {
		t.Fatalf("size = %d, want 0", size)
	}
	hEmpty := Hash(nil)
	if h != hEmpty {
		t.Fatal("empty reader hash mismatch")
	}
}

func TestHashDeterministic(t *testing.T) {
	data := []byte("deterministic test data")
	h1 := Hash(data)
	h2 := Hash(data)
	if h1 != h2 {
		t.Fatal("same data produces different hashes")
	}
}
