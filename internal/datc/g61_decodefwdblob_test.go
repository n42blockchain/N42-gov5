package datc

import (
	"encoding/binary"
	"testing"
)

// g61EncodeFwd packs (key, val) pairs the same way decodeFwdBlob expects to
// unpack them: fixed-width key, varint value length, value bytes.
func g61EncodeFwd(keyLen int, pairs [][2][]byte) []byte {
	var out []byte
	var vlBuf [binary.MaxVarintLen64]byte
	for _, p := range pairs {
		k, v := p[0], p[1]
		if len(k) != keyLen {
			panic("g61EncodeFwd: bad key length in test fixture")
		}
		out = append(out, k...)
		n := binary.PutUvarint(vlBuf[:], uint64(len(v)))
		out = append(out, vlBuf[:n]...)
		out = append(out, v...)
	}
	return out
}

// TestG61DecodeFwdBlob_RoundTrip covers the happy path: several key/value
// pairs packed with g61EncodeFwd come back out in order and byte-identical.
func TestG61DecodeFwdBlob_RoundTrip(t *testing.T) {
	const keyLen = 20
	pairs := [][2][]byte{
		{make([]byte, keyLen), []byte("")},
		{append(make([]byte, keyLen-1), 0x01), []byte("short")},
		{append(make([]byte, keyLen-1), 0x02), make([]byte, 300)}, // forces multi-byte varint
	}
	blob := g61EncodeFwd(keyLen, pairs)

	var got [][2][]byte
	err := decodeFwdBlob(blob, keyLen, func(key, val []byte) error {
		// decodeFwdBlob hands back slices aliasing blob; copy them out.
		kk := append([]byte(nil), key...)
		vv := append([]byte(nil), val...)
		got = append(got, [2][]byte{kk, vv})
		return nil
	})
	if err != nil {
		t.Fatalf("decodeFwdBlob: %v", err)
	}
	if len(got) != len(pairs) {
		t.Fatalf("got %d pairs, want %d", len(got), len(pairs))
	}
	for i := range pairs {
		if string(got[i][0]) != string(pairs[i][0]) {
			t.Errorf("pair %d: key mismatch", i)
		}
		if string(got[i][1]) != string(pairs[i][1]) {
			t.Errorf("pair %d: val mismatch (got %d bytes, want %d)", i, len(got[i][1]), len(pairs[i][1]))
		}
	}
}

// TestG61DecodeFwdBlob_TruncatedKey covers the "truncated at key" guard: a
// blob shorter than one key-width must error, not panic via a slice OOB.
func TestG61DecodeFwdBlob_TruncatedKey(t *testing.T) {
	blob := make([]byte, 10)
	err := decodeFwdBlob(blob, 20, func(key, val []byte) error {
		t.Fatal("callback should not be invoked on a truncated-key blob")
		return nil
	})
	if err == nil {
		t.Fatal("expected an error for a blob shorter than one key")
	}
}

// TestG61DecodeFwdBlob_TruncatedValue covers the "truncated at value" guard:
// a valid key followed by a varint claiming more value bytes than remain.
func TestG61DecodeFwdBlob_TruncatedValue(t *testing.T) {
	const keyLen = 4
	blob := make([]byte, keyLen)
	var vlBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(vlBuf[:], 100) // claims 100 bytes of value
	blob = append(blob, vlBuf[:n]...)
	blob = append(blob, []byte("short")...) // far fewer than 100 bytes follow

	err := decodeFwdBlob(blob, keyLen, func(key, val []byte) error {
		t.Fatal("callback should not be invoked on a truncated-value blob")
		return nil
	})
	if err == nil {
		t.Fatal("expected an error for a value claiming more bytes than remain")
	}
}

// TestG61DecodeFwdBlob_CallbackErrorPropagates covers the fn-returns-error
// path: decodeFwdBlob must stop and surface it rather than continue.
func TestG61DecodeFwdBlob_CallbackErrorPropagates(t *testing.T) {
	const keyLen = 4
	pairs := [][2][]byte{
		{make([]byte, keyLen), []byte("a")},
		{append(make([]byte, keyLen-1), 0x01), []byte("b")},
	}
	blob := g61EncodeFwd(keyLen, pairs)

	calls := 0
	sentinel := errSentinel{}
	err := decodeFwdBlob(blob, keyLen, func(key, val []byte) error {
		calls++
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("expected the sentinel error to propagate, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected decodeFwdBlob to stop after the first callback error, got %d calls", calls)
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "g61 sentinel error" }
