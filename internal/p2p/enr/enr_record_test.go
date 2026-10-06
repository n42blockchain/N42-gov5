package enr

import (
	"bytes"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/rlp"
)

// fakeScheme is a minimal IdentityScheme used for testing. It "signs" by
// storing a fixed signature and "verifies" by checking that signature.
type fakeScheme struct {
	sig     []byte
	verErr  error
	addr    []byte
}

func (f fakeScheme) Verify(r *Record, sig []byte) error {
	if f.verErr != nil {
		return f.verErr
	}
	if !bytes.Equal(sig, f.sig) {
		return ErrInvalidSig
	}
	return nil
}

func (f fakeScheme) NodeAddr(r *Record) []byte { return f.addr }

func signTestRecord(t *testing.T, r *Record) fakeScheme {
	t.Helper()
	s := fakeScheme{sig: []byte("test-signature"), addr: []byte("test-addr")}
	if err := r.SetSig(s, s.sig); err != nil {
		t.Fatalf("SetSig failed: %v", err)
	}
	return s
}

func TestRecordSetLoad(t *testing.T) {
	var r Record
	r.Set(UDP(30303))
	r.Set(IP{127, 0, 0, 1})

	var udp UDP
	if err := r.Load(&udp); err != nil {
		t.Fatalf("Load(UDP) failed: %v", err)
	}
	if udp != 30303 {
		t.Fatalf("UDP = %d, want 30303", udp)
	}

	var ip IPv4
	if err := r.Load(&ip); err != nil {
		t.Fatalf("Load(IPv4) failed: %v", err)
	}
}

func TestRecordLoadMissingKey(t *testing.T) {
	var r Record
	var tcp TCP
	err := r.Load(&tcp)
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if !IsNotFound(err) {
		t.Fatalf("expected IsNotFound(err) to be true, got err=%v", err)
	}
}

func TestRecordSetOverwritesAndReorders(t *testing.T) {
	var r Record
	r.Set(UDP(1))
	r.Set(TCP(2))
	r.Set(UDP(3)) // overwrite

	var udp UDP
	if err := r.Load(&udp); err != nil {
		t.Fatal(err)
	}
	if udp != 3 {
		t.Fatalf("UDP = %d, want 3", udp)
	}
}

func TestRecordSetPanicsOnEncodeFailure(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for un-RLP-encodable entry")
		}
	}()
	var r Record
	// channels cannot be RLP encoded.
	r.Set(WithEntry("bad", make(chan int)))
}

func TestRecordSeqIncrementsOnSetAfterSign(t *testing.T) {
	var r Record
	r.Set(UDP(1))
	s := signTestRecord(t, &r)
	seqBefore := r.Seq()

	r.Set(UDP(2)) // invalidates signature, bumps seq
	if r.Seq() != seqBefore+1 {
		t.Fatalf("Seq() = %d, want %d", r.Seq(), seqBefore+1)
	}
	if r.Signature() != nil {
		t.Fatal("expected signature to be cleared after Set")
	}
	_ = s
}

func TestRecordSetSeq(t *testing.T) {
	var r Record
	r.SetSeq(42)
	if r.Seq() != 42 {
		t.Fatalf("Seq() = %d, want 42", r.Seq())
	}
}

func TestRecordEncodeUnsigned(t *testing.T) {
	var r Record
	_, err := rlp.EncodeToBytes(&r)
	if !errors.Is(err, errEncodeUnsigned) {
		t.Fatalf("expected errEncodeUnsigned, got %v", err)
	}
}

func TestRecordSignEncodeDecodeRoundTrip(t *testing.T) {
	var r Record
	r.Set(UDP(30303))
	r.Set(IPv4{192, 168, 0, 1})
	s := signTestRecord(t, &r)

	enc, err := rlp.EncodeToBytes(&r)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	var dec Record
	if err := rlp.DecodeBytes(enc, &dec); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if dec.Seq() != r.Seq() {
		t.Fatalf("decoded Seq = %d, want %d", dec.Seq(), r.Seq())
	}
	if !bytes.Equal(dec.Signature(), r.Signature()) {
		t.Fatal("decoded signature mismatch")
	}
	var udp UDP
	if err := dec.Load(&udp); err != nil || udp != 30303 {
		t.Fatalf("decoded UDP = %d, err=%v", udp, err)
	}
	if err := dec.VerifySignature(s); err != nil {
		t.Fatalf("VerifySignature failed: %v", err)
	}
	if dec.IdentityScheme() != "" {
		// No ID entry was set in this test; IdentityScheme should return "".
		t.Fatalf("expected empty IdentityScheme, got %q", dec.IdentityScheme())
	}
}

func TestRecordSetSigResetsSignature(t *testing.T) {
	var r Record
	r.Set(UDP(1))
	signTestRecord(t, &r)
	if r.Signature() == nil {
		t.Fatal("expected signature to be set")
	}
	if err := r.SetSig(nil, nil); err != nil {
		t.Fatalf("resetting signature failed: %v", err)
	}
	if r.Signature() != nil {
		t.Fatal("expected signature to be cleared")
	}
}

func TestRecordSetSigVerifyFailure(t *testing.T) {
	var r Record
	s := fakeScheme{sig: []byte("good"), verErr: ErrInvalidSig}
	if err := r.SetSig(s, []byte("bad")); !errors.Is(err, ErrInvalidSig) {
		t.Fatalf("expected ErrInvalidSig, got %v", err)
	}
}

func TestRecordSizeTooBig(t *testing.T) {
	var r Record
	// Large payload under a key to exceed SizeLimit (300 bytes).
	big := make([]byte, SizeLimit*2)
	r.Set(WithEntry("big", big))
	s := fakeScheme{sig: []byte("sig")}
	if err := r.SetSig(s, s.sig); !errors.Is(err, errTooBig) {
		t.Fatalf("expected errTooBig, got %v", err)
	}
}

func TestDecodeRecordTooBig(t *testing.T) {
	// Construct a raw RLP list bigger than SizeLimit and attempt to decode it.
	big := make([]byte, SizeLimit*2)
	raw, err := rlp.EncodeToBytes(big)
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	err = rlp.DecodeBytes(raw, &r)
	if err == nil {
		t.Fatal("expected error decoding oversized record")
	}
}

func TestDecodeRecordIncompleteList(t *testing.T) {
	// An empty list decodes to errIncompleteList (missing signature).
	raw, err := rlp.EncodeToBytes([]interface{}{})
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := rlp.DecodeBytes(raw, &r); !errors.Is(err, errIncompleteList) {
		t.Fatalf("expected errIncompleteList, got %v", err)
	}
}

func TestDecodeRecordDuplicateKey(t *testing.T) {
	list := []interface{}{
		[]byte("sig"), uint64(1),
		"a", []byte{1},
		"a", []byte{2},
	}
	raw, err := rlp.EncodeToBytes(list)
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := rlp.DecodeBytes(raw, &r); !errors.Is(err, errDuplicateKey) {
		t.Fatalf("expected errDuplicateKey, got %v", err)
	}
}

func TestDecodeRecordNotSorted(t *testing.T) {
	list := []interface{}{
		[]byte("sig"), uint64(1),
		"b", []byte{1},
		"a", []byte{2},
	}
	raw, err := rlp.EncodeToBytes(list)
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := rlp.DecodeBytes(raw, &r); !errors.Is(err, errNotSorted) {
		t.Fatalf("expected errNotSorted, got %v", err)
	}
}

func TestDecodeRecordIncompletePair(t *testing.T) {
	list := []interface{}{
		[]byte("sig"), uint64(1),
		"a", // missing value
	}
	raw, err := rlp.EncodeToBytes(list)
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err := rlp.DecodeBytes(raw, &r); !errors.Is(err, errIncompletePair) {
		t.Fatalf("expected errIncompletePair, got %v", err)
	}
}

func TestSchemeMap(t *testing.T) {
	var r Record
	r.Set(ID("v4"))
	m := SchemeMap{"v4": fakeScheme{sig: []byte("s"), addr: []byte("addr")}}

	if err := m.Verify(&r, []byte("s")); err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if addr := m.NodeAddr(&r); !bytes.Equal(addr, []byte("addr")) {
		t.Fatalf("NodeAddr = %v, want addr", addr)
	}

	var unknown Record
	unknown.Set(ID("unknown-scheme"))
	if err := m.Verify(&unknown, []byte("s")); !errors.Is(err, ErrInvalidSig) {
		t.Fatalf("expected ErrInvalidSig for unknown scheme, got %v", err)
	}
	if addr := m.NodeAddr(&unknown); addr != nil {
		t.Fatalf("expected nil addr for unknown scheme, got %v", addr)
	}
}

func TestRecordAppendElements(t *testing.T) {
	var r Record
	r.Set(UDP(7))
	list := r.AppendElements(nil)
	if len(list) != 3 { // seq, key, value
		t.Fatalf("AppendElements returned %d elements, want 3", len(list))
	}
}
