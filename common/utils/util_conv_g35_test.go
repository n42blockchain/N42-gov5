// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Round-trip tests for the proto <-> native conversion helpers in util.go,
// plus the small standalone utilities (ByteCount, MBToGB, EnsureEnoughSize,
// Copy, SplitAndTrim, NewContext/FromContext, MkdirAll).

package utils

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/proto/types_pb"
)

func TestG35ByteCount(t *testing.T) {
	cases := []struct {
		in   uint64
		want string
	}{
		{500, "500B"},
		{1024, "1.0KB"},
		{1024 * 1024, "1.0MB"},
	}
	for _, c := range cases {
		if got := ByteCount(c.in); got != c.want {
			t.Fatalf("ByteCount(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestG35MBToGB(t *testing.T) {
	v, exp := MBToGB(500)
	if v != 500 || exp != 0 {
		t.Fatalf("MBToGB(500) = (%v,%d), want (500,0)", v, exp)
	}
	v, exp = MBToGB(1024 * 1024)
	if exp != 1 {
		t.Fatalf("MBToGB(1MiB) exp = %d, want 1", exp)
	}
	if v != 1.0 {
		t.Fatalf("MBToGB(1MiB) value = %v, want 1.0", v)
	}
}

func TestG35EnsureEnoughSize(t *testing.T) {
	in := make([]byte, 2, 10)
	in[0], in[1] = 1, 2
	out := EnsureEnoughSize(in, 5)
	if len(out) != 5 {
		t.Fatalf("expected len 5, got %d", len(out))
	}
	if out[0] != 1 || out[1] != 2 {
		t.Fatalf("expected original bytes preserved")
	}

	small := make([]byte, 1, 1)
	out2 := EnsureEnoughSize(small, 10)
	if len(out2) != 10 || cap(out2) < 10 {
		t.Fatalf("expected reallocated buffer of len 10, got len=%d cap=%d", len(out2), cap(out2))
	}
}

func TestG35Copy(t *testing.T) {
	if Copy(nil) != nil {
		t.Fatalf("expected nil copy of nil input")
	}
	in := []byte{1, 2, 3}
	out := Copy(in)
	if string(out) != string(in) {
		t.Fatalf("expected equal contents")
	}
	out[0] = 9
	if in[0] == 9 {
		t.Fatalf("expected independent backing array")
	}
}

func TestG35SplitAndTrim(t *testing.T) {
	got := SplitAndTrim(" a, b ,, c")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func TestG35HexPrefix(t *testing.T) {
	a := []byte{1, 2, 3, 9}
	b := []byte{1, 2, 4}
	prefix, n := HexPrefix(a, b)
	if n != 2 || string(prefix) != string([]byte{1, 2}) {
		t.Fatalf("expected shared prefix of len 2, got %d %v", n, prefix)
	}
}

func TestG35Exists(t *testing.T) {
	dir := t.TempDir()
	if !Exists(dir) {
		t.Fatalf("expected existing dir to report true")
	}
	if Exists(filepath.Join(dir, "nope")) {
		t.Fatalf("expected missing path to report false")
	}
}

func TestG35MkdirAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("expected directory to exist after MkdirAll")
	}
}

type g35AppInfo struct{}

func (g35AppInfo) ID() string                 { return "id" }
func (g35AppInfo) Name() string               { return "name" }
func (g35AppInfo) Version() string            { return "v1" }
func (g35AppInfo) astdata() map[string]string { return nil }
func (g35AppInfo) Endpoint() []string         { return nil }

func TestG35ContextRoundTrip(t *testing.T) {
	ctx := NewContext(context.Background(), g35AppInfo{})
	s, ok := FromContext(ctx)
	if !ok {
		t.Fatalf("expected ok=true retrieving AppInfo from context")
	}
	if s.Name() != "name" {
		t.Fatalf("unexpected AppInfo retrieved")
	}

	_, ok = FromContext(context.Background())
	if ok {
		t.Fatalf("expected ok=false for context without AppInfo")
	}
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func TestG35ConvertH256RoundTrip(t *testing.T) {
	u := uint256.NewInt(0)
	u.SetBytes(randBytes(32))
	p := ConvertUint256IntToH256(u)
	back := ConvertH256ToUint256Int(p)
	if !u.Eq(back) {
		t.Fatalf("round-trip mismatch: %v != %v", u, back)
	}

	if ConvertUint256IntToH256(nil) != nil {
		t.Fatalf("expected nil for nil uint256 input")
	}
	if got := ConvertH256ToUint256Int(nil); got.Sign() != 0 {
		t.Fatalf("expected zero value for nil H256 input")
	}

	var hash [32]byte
	copy(hash[:], randBytes(32))
	h256 := ConvertHashToH256(hash)
	backHash := ConvertH256ToHash(h256)
	if backHash != hash {
		t.Fatalf("hash round-trip mismatch")
	}
	if ConvertH256ToHash(nil) != ([32]byte{}) {
		t.Fatalf("expected zero hash for nil H256")
	}
}

func TestG35ConvertH512RoundTrip(t *testing.T) {
	var b [64]byte
	copy(b[:], randBytes(64))
	h512 := ConvertHashToH512(b)
	back := ConvertH512ToHash(h512)
	if back != b {
		t.Fatalf("H512 round-trip mismatch")
	}

	backBytes := ConvertH512ToBytes(h512)
	if string(backBytes) != string(b[:]) {
		t.Fatalf("ConvertH512ToBytes mismatch")
	}

	// short input gets zero-padded.
	short := ConvertBytesToH512([]byte{1, 2, 3})
	if short.Lo.Lo.Lo != 0 {
		t.Fatalf("expected padded zero for short input")
	}
}

func TestG35ConvertHashesToH256AndBack(t *testing.T) {
	hashes := []types.Hash{types.BytesToHash(randBytes(32)), types.BytesToHash(randBytes(32))}
	p := ConvertHashesToH256(hashes)
	back := H256sToHashes(p)
	if len(back) != 2 {
		t.Fatalf("expected 2 hashes back")
	}
	for i := range hashes {
		if back[i] != hashes[i] {
			t.Fatalf("hash %d mismatch: %x != %x", i, back[i], hashes[i])
		}
	}
}

func TestG35Uint256sToH256RoundTrip(t *testing.T) {
	u1 := uint256.NewInt(123)
	u2 := uint256.NewInt(456)
	p := Uint256sToH256([]uint256.Int{*u1, *u2})
	if len(p) != 2 {
		t.Fatalf("expected 2 entries")
	}
	back := ConvertH256ToUint256Int(p[0])
	if !back.Eq(u1) {
		t.Fatalf("expected %v got %v", u1, back)
	}
}

func TestG35ConvertH160RoundTrip(t *testing.T) {
	var addr [20]byte
	copy(addr[:], randBytes(20))
	h160 := ConvertAddressToH160(addr)
	back := ConvertH160toAddress(h160)
	if back != addr {
		t.Fatalf("H160 round-trip mismatch")
	}

	paddr := ConvertH160ToPAddress(h160)
	if paddr.Bytes()[0] != addr[0] {
		t.Fatalf("ConvertH160ToPAddress mismatch")
	}

	addrs := []types.Address{types.BytesToAddress(randBytes(20)), types.BytesToAddress(randBytes(20))}
	hs := ConvertAddrsToH160(addrs)
	backAddrs := H160sToAddress(hs)
	for i := range addrs {
		if backAddrs[i] != addrs[i] {
			t.Fatalf("addr %d round-trip mismatch", i)
		}
	}
}

func TestG35ConvertH384RoundTrip(t *testing.T) {
	var pub [48]byte
	copy(pub[:], randBytes(48))
	h384 := ConvertPublicKeyToH384(pub)
	back := ConvertH384ToPublicKey(h384)
	if back != pub {
		t.Fatalf("H384 round-trip mismatch")
	}

	pubs := []types.PublicKey{types.PublicKey(pub)}
	hs := ConvertPubsToH384(pubs)
	backPubs := H384sToPubs(hs)
	if backPubs[0] != pubs[0] {
		t.Fatalf("pubkey slice round-trip mismatch")
	}
}

func TestG35ConvertH768RoundTrip(t *testing.T) {
	var sig [96]byte
	copy(sig[:], randBytes(96))
	h768 := ConvertSignatureToH768(sig)
	back := ConvertH768ToSignature(h768)
	if back != sig {
		t.Fatalf("H768 round-trip mismatch")
	}
}

func TestG35ConvertH2048RoundTrip(t *testing.T) {
	data := randBytes(256)
	h2048 := ConvertBytesToH2048(data)
	bloom := ConvertH2048ToBloom(h2048)
	if string(bloom[:]) != string(data) {
		t.Fatalf("H2048/bloom round-trip mismatch")
	}
}

// sanity: ensure types_pb is actually imported (used above), silencing any
// accidental unused-import drift if the round trips above get trimmed later.
var _ = types_pb.H256{}
