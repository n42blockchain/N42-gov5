package rlphacks

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/rlp"
)

func TestRlpSerializableBytes(t *testing.T) {
	cases := [][]byte{
		{0x01},
		{0x80},
		{0xff},
		bytes.Repeat([]byte{0x41}, 10),
		bytes.Repeat([]byte{0x41}, 55),
		bytes.Repeat([]byte{0x41}, 56),
		bytes.Repeat([]byte{0x41}, 300),
		bytes.Repeat([]byte{0x41}, 70000),
	}
	for _, raw := range cases {
		b := RlpSerializableBytes(raw)
		if got := b.RawBytes(); !bytes.Equal(got, raw) {
			t.Fatalf("RawBytes mismatch for len %d", len(raw))
		}

		// Single RLP encoding reference, via standard rlp encoder.
		var singleBuf bytes.Buffer
		if err := rlp.Encode(&singleBuf, raw); err != nil {
			t.Fatalf("rlp.Encode: %v", err)
		}

		// Double RLP: wrap the single-encoded bytes once more.
		var wantDouble bytes.Buffer
		if err := rlp.Encode(&wantDouble, singleBuf.Bytes()); err != nil {
			t.Fatalf("rlp.Encode double: %v", err)
		}

		var out bytes.Buffer
		prefixBuf := make([]byte, 16)
		if err := b.ToDoubleRLP(&out, prefixBuf); err != nil {
			t.Fatalf("ToDoubleRLP: %v", err)
		}
		if !bytes.Equal(out.Bytes(), wantDouble.Bytes()) {
			t.Fatalf("len=%d: ToDoubleRLP = %x, want %x", len(raw), out.Bytes(), wantDouble.Bytes())
		}
		if got := b.DoubleRLPLen(); got != len(wantDouble.Bytes()) {
			t.Fatalf("len=%d: DoubleRLPLen = %d, want %d", len(raw), got, len(wantDouble.Bytes()))
		}
	}
}

func TestRlpSerializableBytesEmptyDoubleLen(t *testing.T) {
	b := RlpSerializableBytes(nil)
	if got := b.DoubleRLPLen(); got != 0 {
		t.Fatalf("DoubleRLPLen for empty = %d, want 0", got)
	}
}

func TestRlpEncodedBytes(t *testing.T) {
	// RlpEncodedBytes treats its content as already-single-RLP-encoded bytes,
	// and ToDoubleRLP wraps it once more as an RLP string.
	//
	// NOTE: a single byte >= 0x80 (e.g. {0x80}) is excluded from this table.
	// ToDoubleRLP correctly emits a length-1 prefix for it (matching the real
	// RLP encoding), but DoubleRLPLen's generateRlpPrefixLen(l) only looks at
	// the length and returns 0 for l<2, ignoring the first-byte value. That
	// makes DoubleRLPLen under-report by one byte for exactly this case; see
	// TestRlpEncodedBytesDoubleRLPLenBug below.
	cases := [][]byte{
		{0x01},
		bytes.Repeat([]byte{0x41}, 10),
		bytes.Repeat([]byte{0x41}, 56),
		bytes.Repeat([]byte{0x41}, 300),
		bytes.Repeat([]byte{0x41}, 70000),
	}
	for _, raw := range cases {
		b := RlpEncodedBytes(raw)
		if got := b.RawBytes(); !bytes.Equal(got, raw) {
			t.Fatalf("RawBytes mismatch for len %d", len(raw))
		}

		var want bytes.Buffer
		if err := rlp.Encode(&want, raw); err != nil {
			t.Fatalf("rlp.Encode: %v", err)
		}

		var out bytes.Buffer
		prefixBuf := make([]byte, 16)
		if err := b.ToDoubleRLP(&out, prefixBuf); err != nil {
			t.Fatalf("ToDoubleRLP: %v", err)
		}
		if !bytes.Equal(out.Bytes(), want.Bytes()) {
			t.Fatalf("len=%d: ToDoubleRLP = %x, want %x", len(raw), out.Bytes(), want.Bytes())
		}
		if got := b.DoubleRLPLen(); got != len(want.Bytes()) {
			t.Fatalf("len=%d: DoubleRLPLen = %d, want %d", len(raw), got, len(want.Bytes()))
		}
	}
}

// TestRlpEncodedBytesDoubleRLPLenBug pins down a pre-existing discrepancy
// (not fixed here, per task constraints): for a single byte with value
// >= 0x80, ToDoubleRLP writes a length-1 RLP prefix (the correct encoding),
// but DoubleRLPLen() reports a length that is one byte short, because
// generateRlpPrefixLen(l) decides purely from the length (l<2 -> 0) without
// inspecting the first byte. RlpSerializableBytes.DoubleRLPLen does not have
// this bug because it uses generateRlpPrefixLenDouble(l, firstByte), which
// does look at the first byte.
func TestRlpEncodedBytesDoubleRLPLenBug(t *testing.T) {
	raw := []byte{0x80}
	b := RlpEncodedBytes(raw)

	var want bytes.Buffer
	if err := rlp.Encode(&want, raw); err != nil {
		t.Fatalf("rlp.Encode: %v", err)
	}

	var out bytes.Buffer
	prefixBuf := make([]byte, 16)
	if err := b.ToDoubleRLP(&out, prefixBuf); err != nil {
		t.Fatalf("ToDoubleRLP: %v", err)
	}
	// The bytes actually written are correct and match the real RLP encoding.
	if !bytes.Equal(out.Bytes(), want.Bytes()) {
		t.Fatalf("ToDoubleRLP = %x, want %x", out.Bytes(), want.Bytes())
	}
	// But the reported length is one byte short of what was actually written.
	gotLen := b.DoubleRLPLen()
	if gotLen == len(out.Bytes()) {
		t.Fatalf("DoubleRLPLen() = %d now matches actual output length %d; the known bug may be fixed, update this test", gotLen, len(out.Bytes()))
	}
	if gotLen != len(out.Bytes())-1 {
		t.Fatalf("DoubleRLPLen() = %d, expected the known off-by-one of %d", gotLen, len(out.Bytes())-1)
	}
}

// errWriter always fails, to exercise the error paths of ToDoubleRLP/EncodeByteArrayAsRlp.
type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) { return 0, bytes.ErrTooLarge }

func TestToDoubleRLPWriteErrors(t *testing.T) {
	prefixBuf := make([]byte, 16)

	// Prefix write fails (long enough payload to require a prefix).
	long := RlpSerializableBytes(bytes.Repeat([]byte{0x41}, 100))
	if err := long.ToDoubleRLP(errWriter{}, prefixBuf); err == nil {
		t.Fatal("expected error from prefix write")
	}

	// Payload write fails for a single byte below 0x80 (no prefix emitted,
	// so the failure comes from the final body write).
	short := RlpSerializableBytes([]byte{0x01})
	if err := short.ToDoubleRLP(errWriter{}, prefixBuf); err == nil {
		t.Fatal("expected error from body write")
	}

	encoded := RlpEncodedBytes([]byte{0x01})
	if err := encoded.ToDoubleRLP(errWriter{}, prefixBuf); err == nil {
		t.Fatal("expected error from body write")
	}
}

func TestEncodeByteArrayAsRlp(t *testing.T) {
	// {0x80} is deliberately excluded: see TestEncodeByteArrayAsRlpLenBug,
	// the same off-by-one as TestRlpEncodedBytesDoubleRLPLenBug.
	cases := [][]byte{
		{0x01},
		bytes.Repeat([]byte{0x41}, 10),
		bytes.Repeat([]byte{0x41}, 300),
	}
	for _, raw := range cases {
		var want bytes.Buffer
		if err := rlp.Encode(&want, raw); err != nil {
			t.Fatalf("rlp.Encode: %v", err)
		}

		var out bytes.Buffer
		prefixBuf := make([]byte, 16)
		n, err := EncodeByteArrayAsRlp(raw, &out, prefixBuf)
		if err != nil {
			t.Fatalf("EncodeByteArrayAsRlp: %v", err)
		}
		if n != len(want.Bytes()) {
			t.Fatalf("len=%d: returned n=%d, want %d", len(raw), n, len(want.Bytes()))
		}
		if !bytes.Equal(out.Bytes(), want.Bytes()) {
			t.Fatalf("len=%d: got %x, want %x", len(raw), out.Bytes(), want.Bytes())
		}
	}

	if _, err := EncodeByteArrayAsRlp([]byte{0x01}, errWriter{}, make([]byte, 16)); err == nil {
		t.Fatal("expected error propagated from writer")
	}
}

// TestEncodeByteArrayAsRlpLenBug: like TestRlpEncodedBytesDoubleRLPLenBug,
// EncodeByteArrayAsRlp's reported length uses generateRlpPrefixLen(l), which
// only looks at len(raw) and misses that a single byte >= 0x80 still needs a
// 1-byte RLP prefix. The bytes actually written are correct; the returned
// count is one short. Documented, not fixed, per task constraints.
func TestEncodeByteArrayAsRlpLenBug(t *testing.T) {
	raw := []byte{0x80}
	var want bytes.Buffer
	if err := rlp.Encode(&want, raw); err != nil {
		t.Fatalf("rlp.Encode: %v", err)
	}

	var out bytes.Buffer
	prefixBuf := make([]byte, 16)
	n, err := EncodeByteArrayAsRlp(raw, &out, prefixBuf)
	if err != nil {
		t.Fatalf("EncodeByteArrayAsRlp: %v", err)
	}
	if !bytes.Equal(out.Bytes(), want.Bytes()) {
		t.Fatalf("got %x, want %x", out.Bytes(), want.Bytes())
	}
	if n == len(out.Bytes()) {
		t.Fatalf("n = %d now matches actual output length %d; the known bug may be fixed, update this test", n, len(out.Bytes()))
	}
	if n != len(out.Bytes())-1 {
		t.Fatalf("n = %d, expected the known off-by-one of %d", n, len(out.Bytes())-1)
	}
}

func TestGenerateStructLen(t *testing.T) {
	cases := []struct {
		l       int
		wantN   int
		wantHdr byte
	}{
		{0, 1, 192},
		{10, 1, 202},
		{55, 1, 247},
		{56, 2, 248},
		{255, 2, 248},
		{256, 3, 249},
		{65535, 3, 249},
		{65536, 4, 250},
	}
	buf := make([]byte, 8)
	for _, c := range cases {
		n := GenerateStructLen(buf, c.l)
		if n != c.wantN {
			t.Fatalf("l=%d: GenerateStructLen returned %d, want %d", c.l, n, c.wantN)
		}
		if buf[0] != c.wantHdr {
			t.Fatalf("l=%d: header byte = %d, want %d", c.l, buf[0], c.wantHdr)
		}
	}
}
