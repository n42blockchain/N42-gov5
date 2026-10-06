package rlp

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/holiman/uint256"
)

func TestIsRLPError(t *testing.T) {
	if !IsRLPError(ErrParse) {
		t.Fatal("expected ErrParse to be an rlp error")
	}
	if IsRLPError(errors.New("unrelated")) {
		t.Fatal("unrelated error should not match IsRLPError")
	}
}

func TestParseListHappyAndErrorPaths(t *testing.T) {
	// A one-byte list containing a single zero byte: 0xC1 0x00.
	payload := []byte{0xC1, 0x00}
	pos, dataLen, err := ParseList(payload, 0)
	if err != nil {
		t.Fatalf("ParseList error: %v", err)
	}
	if pos != 1 || dataLen != 1 {
		t.Fatalf("ParseList = (%d, %d), want (1, 1)", pos, dataLen)
	}

	// Not a list (a bare string) should error.
	if _, _, err := ParseList([]byte{0x01}, 0); err == nil {
		t.Fatal("expected error parsing a non-list as a list")
	}
}

func TestStringOfLen(t *testing.T) {
	// A 2-byte string: 0x82 'a' 'b'.
	payload := []byte{0x82, 'a', 'b'}
	pos, err := StringOfLen(payload, 0, 2)
	if err != nil {
		t.Fatalf("StringOfLen error: %v", err)
	}
	if pos != 1 {
		t.Fatalf("StringOfLen pos = %d, want 1", pos)
	}

	if _, err := StringOfLen(payload, 0, 3); err == nil {
		t.Fatal("expected length mismatch error")
	}
}

func TestU256Len(t *testing.T) {
	if got := U256Len(nil); got != 1 {
		t.Errorf("U256Len(nil) = %d, want 1", got)
	}
	if got := U256Len(uint256.NewInt(0)); got != 1 {
		t.Errorf("U256Len(0) = %d, want 1", got)
	}
	if got := U256Len(uint256.NewInt(100)); got != 1 {
		t.Errorf("U256Len(100) = %d, want 1", got)
	}
	big := uint256.NewInt(0).Lsh(uint256.NewInt(1), 200)
	if got := U256Len(big); got <= 1 {
		t.Errorf("U256Len(big) = %d, want > 1", got)
	}
}

func TestParseHash(t *testing.T) {
	hash := make([]byte, 32)
	for i := range hash {
		hash[i] = byte(i + 1)
	}
	payload := append([]byte{0x80 + 32}, hash...)

	buf := make([]byte, 32)
	pos, err := ParseHash(payload, 0, buf)
	if err != nil {
		t.Fatalf("ParseHash error: %v", err)
	}
	if pos != len(payload) {
		t.Fatalf("ParseHash pos = %d, want %d", pos, len(payload))
	}
	if !bytes.Equal(buf, hash) {
		t.Fatalf("ParseHash content mismatch")
	}

	// Wrong length string should error with the documented prefix.
	if _, err := ParseHash([]byte{0x81, 0x01}, 0, buf); err == nil {
		t.Fatal("expected error for wrong hash length")
	}
}

func TestParseAnnouncementsRoundTrip(t *testing.T) {
	types := []byte{1, 2}
	sizes := []uint32{10, 20}
	hashes := make([]byte, 64)
	for i := range hashes {
		hashes[i] = byte(i)
	}

	encLen := AnnouncementsLen(types, sizes, hashes)
	buf := make([]byte, encLen+8)
	EncodeAnnouncements(types, sizes, hashes, buf)

	gotTypes, gotSizes, gotHashes, pos, err := ParseAnnouncements(buf, 0)
	if err != nil {
		t.Fatalf("ParseAnnouncements error: %v", err)
	}
	if !bytes.Equal(gotTypes, types) {
		t.Fatalf("types mismatch: got %v want %v", gotTypes, types)
	}
	if !reflect.DeepEqual(gotSizes, sizes) {
		t.Fatalf("sizes mismatch: got %v want %v", gotSizes, sizes)
	}
	if !bytes.Equal(gotHashes, hashes) {
		t.Fatalf("hashes mismatch")
	}
	if pos != encLen {
		t.Fatalf("pos = %d, want %d", pos, encLen)
	}
}

func TestParseAnnouncementsEmpty(t *testing.T) {
	buf := make([]byte, 8)
	EncodeAnnouncements(nil, nil, nil, buf)
	types, sizes, hashes, _, err := ParseAnnouncements(buf, 0)
	if err != nil {
		t.Fatalf("ParseAnnouncements(empty) error: %v", err)
	}
	if len(types) != 0 || len(sizes) != 0 || len(hashes) != 0 {
		t.Fatalf("expected all empty, got types=%v sizes=%v hashes=%v", types, sizes, hashes)
	}
}

func TestListSizeAndIntSize(t *testing.T) {
	if got := ListSize(10); got == 0 {
		t.Fatal("expected non-zero ListSize")
	}
	if got := IntSize(0x7f); got != 1 {
		t.Errorf("IntSize(0x7f) = %d, want 1", got)
	}
	if got := IntSize(0x80); got != 2 {
		t.Errorf("IntSize(0x80) = %d, want 2", got)
	}
}

func TestSplitList(t *testing.T) {
	// List containing a single zero-length string: 0xC1 0x80.
	b := []byte{0xC1, 0x80}
	content, rest, err := SplitList(b)
	if err != nil {
		t.Fatalf("SplitList error: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("expected no remaining bytes, got %v", rest)
	}
	if len(content) != 1 || content[0] != 0x80 {
		t.Fatalf("content mismatch: %v", content)
	}

	// Splitting a string as a list should error.
	if _, _, err := SplitList([]byte{0x01}); err == nil {
		t.Fatal("expected error splitting a string as a list")
	}
}

func TestAppendUint64RoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 127, 128, 1 << 20, ^uint64(0)}
	for _, v := range cases {
		out := AppendUint64(nil, v)
		x, rest, err := SplitUint64(out)
		if err != nil {
			t.Fatalf("SplitUint64 round trip error for %d: %v", v, err)
		}
		if x != v {
			t.Fatalf("round trip mismatch: got %d want %d", x, v)
		}
		if len(rest) != 0 {
			t.Fatalf("expected no remaining bytes, got %v", rest)
		}
	}
}

func TestNewListIteratorAndErr(t *testing.T) {
	// Build a list of two single-byte strings.
	var buf bytes.Buffer
	if err := Encode(&buf, []interface{}{[]byte{1}, []byte{2}}); err != nil {
		t.Fatalf("Encode error: %v", err)
	}

	it, err := NewListIterator(RawValue(buf.Bytes()))
	if err != nil {
		t.Fatalf("NewListIterator error: %v", err)
	}
	count := 0
	for it.Next() {
		count++
		if it.Value() == nil {
			t.Fatal("expected non-nil value")
		}
	}
	if it.Err() != nil {
		t.Fatalf("unexpected iterator error: %v", it.Err())
	}
	if count != 2 {
		t.Fatalf("expected 2 items, got %d", count)
	}

	// Not a list should error.
	if _, err := NewListIterator(RawValue([]byte{0x01})); err == nil {
		t.Fatal("expected error for non-list input")
	}
}

func TestIsInvalidRLPError(t *testing.T) {
	if !IsInvalidRLPError(ErrExpectedString) {
		t.Fatal("ErrExpectedString should be considered invalid RLP")
	}
	if !IsInvalidRLPError(ErrExpectedList) {
		t.Fatal("ErrExpectedList should be considered invalid RLP")
	}
	if IsInvalidRLPError(errors.New("unrelated")) {
		t.Fatal("unrelated error should not be considered invalid RLP")
	}
}

func TestWrapStreamError(t *testing.T) {
	err := WrapStreamError(ErrExpectedString, reflect.TypeOf(int(0)))
	if err == nil {
		t.Fatal("expected wrapped error")
	}
}

func TestStreamRemaining(t *testing.T) {
	s := NewStream(bytes.NewReader([]byte{0x83, 'a', 'b', 'c'}), 0)
	if _, _, err := s.Kind(); err != nil {
		t.Fatalf("Kind error: %v", err)
	}
	if got := s.Remaining(); got == 0 {
		t.Fatal("expected non-zero Remaining before reading content")
	}
}
