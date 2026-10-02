package jsonrpc

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestDecodeUint64(t *testing.T) {
	cases := []struct {
		in      string
		want    uint64
		wantErr error
	}{
		{"0x0", 0, nil},
		{"0x1a", 0x1a, nil},
		{"0xff", 0xff, nil},
		{"", 0, ErrEmptyString},
		{"1a", 0, ErrMissingPrefix},
		{"0x", 0, ErrEmptyNumber},
		{"0x01", 0, ErrLeadingZero},
		{"0xzz", 0, ErrSyntax},
		{"0xffffffffffffffffff", 0, ErrUint64Range},
	}
	for _, c := range cases {
		got, err := DecodeUint64(c.in)
		if c.wantErr != nil {
			if !errors.Is(err, c.wantErr) && err != c.wantErr {
				t.Errorf("DecodeUint64(%q) err = %v, want %v", c.in, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("DecodeUint64(%q) unexpected err %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("DecodeUint64(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestHas0xPrefix(t *testing.T) {
	if !has0xPrefix("0xabc") {
		t.Error("expected true for 0x prefix")
	}
	if !has0xPrefix("0Xabc") {
		t.Error("expected true for 0X prefix")
	}
	if has0xPrefix("abc") {
		t.Error("expected false for no prefix")
	}
	if has0xPrefix("0") {
		t.Error("expected false for short string")
	}
}

func TestBytesHave0xPrefix(t *testing.T) {
	if !bytesHave0xPrefix([]byte("0xabc")) {
		t.Error("expected true")
	}
	if bytesHave0xPrefix([]byte("abc")) {
		t.Error("expected false")
	}
}

func TestCheckText(t *testing.T) {
	// empty input allowed
	raw, err := checkText(nil, true)
	if err != nil || raw != nil {
		t.Errorf("empty input: got %v, %v", raw, err)
	}
	// with 0x prefix
	raw, err = checkText([]byte("0xabcd"), true)
	if err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if string(raw) != "abcd" {
		t.Errorf("got %q want abcd", raw)
	}
	// missing prefix when required
	if _, err = checkText([]byte("abcd"), true); err != ErrMissingPrefix {
		t.Errorf("want ErrMissingPrefix got %v", err)
	}
	// missing prefix not required
	raw, err = checkText([]byte("abcd"), false)
	if err != nil || string(raw) != "abcd" {
		t.Errorf("got %v, %v", raw, err)
	}
	// odd length
	if _, err = checkText([]byte("0xabc"), true); err != ErrOddLength {
		t.Errorf("want ErrOddLength got %v", err)
	}
}

func TestDecodeNibble(t *testing.T) {
	cases := map[byte]uint64{
		'0': 0, '9': 9, 'a': 10, 'f': 15, 'A': 10, 'F': 15,
	}
	for in, want := range cases {
		if got := decodeNibble(in); got != want {
			t.Errorf("decodeNibble(%q) = %d want %d", in, got, want)
		}
	}
	if decodeNibble('g') != badNibble {
		t.Error("expected badNibble for invalid char")
	}
}

func TestUnmarshalFixedTextAndHash(t *testing.T) {
	var out [4]byte
	if err := UnmarshalFixedText("test", []byte("0x01020304"), out[:]); err != nil {
		t.Fatalf("unexpected err %v", err)
	}
	if out != [4]byte{1, 2, 3, 4} {
		t.Errorf("got %v", out)
	}

	// wrong length
	if err := UnmarshalFixedText("test", []byte("0x0102"), out[:]); err == nil {
		t.Error("expected error for length mismatch")
	}

	// bad syntax
	if err := UnmarshalFixedText("test", []byte("0xzzzzzzzz"), out[:]); err != ErrSyntax {
		t.Errorf("want ErrSyntax got %v", err)
	}

	// via types.Hash
	var h types.Hash
	input := make([]byte, 2+len(h)*2)
	copy(input, "0x")
	for i := 2; i < len(input); i++ {
		input[i] = '0'
	}
	if err := UnmarshalText(h, input); err != nil {
		t.Errorf("UnmarshalText unexpected err %v", err)
	}
}

type tempErr struct{ temporary bool }

func (t tempErr) Error() string   { return "temp" }
func (t tempErr) Temporary() bool { return t.temporary }

func TestIsTemporaryError(t *testing.T) {
	if !IsTemporaryError(tempErr{temporary: true}) {
		t.Error("expected true for temporary error")
	}
	if IsTemporaryError(tempErr{temporary: false}) {
		t.Error("expected false for non-temporary error")
	}
	if IsTemporaryError(errors.New("plain")) {
		t.Error("expected false for plain error")
	}
}

func TestMapError(t *testing.T) {
	// odd-length hex decoding produces hex.ErrLength via strconv path indirectly;
	// exercise through DecodeUint64 already covers mapError branches.
	if _, err := DecodeUint64("0xg"); err != ErrSyntax {
		t.Errorf("want ErrSyntax got %v", err)
	}
}
