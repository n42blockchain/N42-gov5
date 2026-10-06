package rlp

import (
	"bytes"
	"errors"
	"testing"
)

func TestGenerateRlpPrefixLenDouble(t *testing.T) {
	cases := []struct {
		l         int
		firstByte byte
		want      int
	}{
		{0, 0x79, 0},
		{1, 0x79, 0},
		{1, 0x80, 2},
		{10, 0, 2},
		{54, 0, 2},
		{55, 0, 3},
		{100, 0, 4},
		{253, 0, 4},
		{254, 0, 5},
		{255, 0, 5},
		{65000, 0, 6},
		{65535, 0, 7},
		{70000, 0, 8},
	}
	for _, c := range cases {
		if got := generateRlpPrefixLenDouble(c.l, c.firstByte); got != c.want {
			t.Errorf("generateRlpPrefixLenDouble(%d,%#x) = %d, want %d", c.l, c.firstByte, got, c.want)
		}
	}
}

func TestGenerateRlpPrefixLen(t *testing.T) {
	cases := []struct {
		l    int
		want int
	}{
		{0, 0},
		{1, 0},
		{2, 1},
		{55, 1},
		{56, 2},
		{255, 2},
		{256, 3},
		{65535, 3},
		{65536, 4},
	}
	for _, c := range cases {
		if got := generateRlpPrefixLen(c.l); got != c.want {
			t.Errorf("generateRlpPrefixLen(%d) = %d, want %d", c.l, got, c.want)
		}
	}
}

func TestMultiByteHeaderPrefixOfLen(t *testing.T) {
	if got := multiByteHeaderPrefixOfLen(1); got != 0xB8 {
		t.Errorf("multiByteHeaderPrefixOfLen(1) = %#x, want 0xB8", got)
	}
}

func TestGenerateByteArrayLen(t *testing.T) {
	buf := make([]byte, 16)
	pos := generateByteArrayLen(buf, 0, 10)
	if pos != 1 || buf[0] != 0x8A {
		t.Fatalf("short form: pos=%d buf=%v", pos, buf[:pos])
	}

	pos = generateByteArrayLen(buf, 0, 200)
	if pos != 2 || buf[0] != 0xB8 || buf[1] != 200 {
		t.Fatalf("1-byte len form: pos=%d buf=%v", pos, buf[:pos])
	}

	pos = generateByteArrayLen(buf, 0, 1000)
	if pos != 3 || buf[0] != 0xB9 {
		t.Fatalf("2-byte len form: pos=%d buf=%v", pos, buf[:pos])
	}

	pos = generateByteArrayLen(buf, 0, 70000)
	if pos != 4 || buf[0] != 0xBA {
		t.Fatalf("3-byte len form: pos=%d buf=%v", pos, buf[:pos])
	}
}

func TestGenerateByteArrayLenDouble(t *testing.T) {
	buf := make([]byte, 16)
	// Exercise every branch purely for panics / monotonic output length.
	lens := []int{10, 55, 100, 255, 60000, 65535, 70000}
	for _, l := range lens {
		pos := generateByteArrayLenDouble(buf, 0, l)
		if pos <= 0 {
			t.Errorf("generateByteArrayLenDouble(%d) produced non-positive length", l)
		}
	}
}

func TestGenerateStructLen(t *testing.T) {
	buf := make([]byte, 8)

	n := GenerateStructLen(buf, 10)
	if n != 1 || buf[0] != byte(192+10) {
		t.Fatalf("short form: n=%d buf=%v", n, buf[:n])
	}

	n = GenerateStructLen(buf, 200)
	if n != 2 || buf[0] != 248 || buf[1] != 200 {
		t.Fatalf("1-byte len form: n=%d buf=%v", n, buf[:n])
	}

	n = GenerateStructLen(buf, 1000)
	if n != 3 || buf[0] != 249 {
		t.Fatalf("2-byte len form: n=%d buf=%v", n, buf[:n])
	}

	n = GenerateStructLen(buf, 70000)
	if n != 4 || buf[0] != 250 {
		t.Fatalf("3-byte len form: n=%d buf=%v", n, buf[:n])
	}
}

func TestRlpSerializableBytesRoundTrip(t *testing.T) {
	b := RlpSerializableBytes([]byte("hello world"))
	if !bytes.Equal(b.RawBytes(), []byte("hello world")) {
		t.Fatal("RawBytes mismatch")
	}
	if b.DoubleRLPLen() == 0 {
		t.Fatal("expected non-zero DoubleRLPLen")
	}

	var buf bytes.Buffer
	prefixBuf := make([]byte, 16)
	if err := b.ToDoubleRLP(&buf, prefixBuf); err != nil {
		t.Fatalf("ToDoubleRLP error: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected encoded bytes")
	}
}

func TestRlpSerializableBytesEmpty(t *testing.T) {
	var b RlpSerializableBytes
	if b.DoubleRLPLen() != 0 {
		t.Fatalf("expected 0 for empty bytes, got %d", b.DoubleRLPLen())
	}
}

func TestRlpEncodedBytesRoundTrip(t *testing.T) {
	b := RlpEncodedBytes([]byte{0x83, 'd', 'o', 'g'})
	if !bytes.Equal(b.RawBytes(), []byte(b)) {
		t.Fatal("RawBytes mismatch")
	}
	want := generateRlpPrefixLen(len(b)) + len(b)
	if got := b.DoubleRLPLen(); got != want {
		t.Fatalf("DoubleRLPLen() = %d, want %d", got, want)
	}

	var buf bytes.Buffer
	prefixBuf := make([]byte, 16)
	if err := b.ToDoubleRLP(&buf, prefixBuf); err != nil {
		t.Fatalf("ToDoubleRLP error: %v", err)
	}
}

type g41ErrWriter struct{}

func (g41ErrWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestEncodeBytesAsRlpToWriterPropagatesWriteError(t *testing.T) {
	prefixBuf := make([]byte, 16)
	source := make([]byte, 100) // forces the prefix-write branch
	err := encodeBytesAsRlpToWriter(source, g41ErrWriter{}, generateByteArrayLen, prefixBuf)
	if err == nil {
		t.Fatal("expected write error to propagate")
	}
}

func TestEncodeBytesAsRlpToWriterShortSource(t *testing.T) {
	// A single byte < 0x80 has no prefix at all.
	var buf bytes.Buffer
	prefixBuf := make([]byte, 16)
	if err := encodeBytesAsRlpToWriter([]byte{0x01}, &buf, generateByteArrayLen, prefixBuf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 1 {
		t.Fatalf("expected a single raw byte with no prefix, got %d bytes", buf.Len())
	}
}

func TestEncodeByteArrayAsRlp(t *testing.T) {
	var buf bytes.Buffer
	prefixBuf := make([]byte, 16)
	raw := []byte("the quick brown fox jumps over the lazy dog, repeated to exceed 55 bytes of length")
	n, err := EncodeByteArrayAsRlp(raw, &buf, prefixBuf)
	if err != nil {
		t.Fatalf("EncodeByteArrayAsRlp error: %v", err)
	}
	if n != buf.Len() {
		t.Fatalf("reported length %d does not match written length %d", n, buf.Len())
	}
}

func TestEncodeByteArrayAsRlpWriteError(t *testing.T) {
	prefixBuf := make([]byte, 16)
	raw := make([]byte, 100)
	if _, err := EncodeByteArrayAsRlp(raw, g41ErrWriter{}, prefixBuf); err == nil {
		t.Fatal("expected write error to propagate")
	}
}
