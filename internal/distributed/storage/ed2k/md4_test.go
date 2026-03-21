package ed2k

import (
	"encoding/hex"
	"testing"
)

func TestMD4Empty(t *testing.T) {
	h := NewMD4()
	sum := h.Sum(nil)
	// MD4 of empty string = 31d6cfe0d16ae931b73c59d7e0c089c0
	expected := "31d6cfe0d16ae931b73c59d7e0c089c0"
	got := hex.EncodeToString(sum)
	if got != expected {
		t.Fatalf("MD4('') = %s, want %s", got, expected)
	}
}

func TestMD4ABC(t *testing.T) {
	h := NewMD4()
	h.Write([]byte("abc"))
	sum := h.Sum(nil)
	// MD4("abc") = a448017aaf21d8525fc10ae87aa6729d
	expected := "a448017aaf21d8525fc10ae87aa6729d"
	got := hex.EncodeToString(sum)
	if got != expected {
		t.Fatalf("MD4('abc') = %s, want %s", got, expected)
	}
}

func TestMD4MessageDigest(t *testing.T) {
	h := NewMD4()
	h.Write([]byte("message digest"))
	sum := h.Sum(nil)
	// MD4("message digest") = d9130a8164549fe818874806e1c7014b
	expected := "d9130a8164549fe818874806e1c7014b"
	got := hex.EncodeToString(sum)
	if got != expected {
		t.Fatalf("MD4('message digest') = %s, want %s", got, expected)
	}
}

func TestMD4Alphabet(t *testing.T) {
	h := NewMD4()
	h.Write([]byte("abcdefghijklmnopqrstuvwxyz"))
	sum := h.Sum(nil)
	// MD4("abcdefghijklmnopqrstuvwxyz") = d79e1c308aa5bbcdeea8ed63df412da9
	expected := "d79e1c308aa5bbcdeea8ed63df412da9"
	got := hex.EncodeToString(sum)
	if got != expected {
		t.Fatalf("MD4(alphabet) = %s, want %s", got, expected)
	}
}

func TestMD4IncrementalWrite(t *testing.T) {
	// Same result whether data is written in one call or multiple
	data := []byte("abcdefghijklmnopqrstuvwxyz")
	h1 := NewMD4()
	h1.Write(data)
	sum1 := h1.Sum(nil)

	h2 := NewMD4()
	h2.Write(data[:10])
	h2.Write(data[10:20])
	h2.Write(data[20:])
	sum2 := h2.Sum(nil)

	if hex.EncodeToString(sum1) != hex.EncodeToString(sum2) {
		t.Fatal("incremental write gives different result")
	}
}

func TestMD4SizeAndBlockSize(t *testing.T) {
	h := NewMD4()
	if h.Size() != 16 {
		t.Fatalf("Size() = %d, want 16", h.Size())
	}
	if h.BlockSize() != 64 {
		t.Fatalf("BlockSize() = %d, want 64", h.BlockSize())
	}
}

func TestMD4Reset(t *testing.T) {
	h := NewMD4()
	h.Write([]byte("some data"))
	h.Reset()
	sum := h.Sum(nil)
	expected := "31d6cfe0d16ae931b73c59d7e0c089c0" // MD4 of empty
	got := hex.EncodeToString(sum)
	if got != expected {
		t.Fatalf("after Reset, MD4 = %s, want %s", got, expected)
	}
}
