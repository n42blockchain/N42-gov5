/*
   Tests for the fixed-size byte types Bytes4/Bytes48/Bytes64/Bytes96:
   Hex/String/Format/Marshal/Unmarshal/Value/TerminalString/Generate.

   NOTE ON A DEFECT (not fixed, per task instructions): SetBytes on all four
   types copy-pastes Hash.SetBytes and hard-codes length.Hash (32) instead of
   the receiver's own length. For Bytes4 (cap 4) this means ANY input panics
   with a slice-bounds error (any length <32 panics on the initial reslice or
   on the destination copy; any length >=32 "succeeds" but copies the wrong
   window of bytes, not a left-crop of the tail). For Bytes48/64/96, inputs
   whose length lands in (length.Hash, len(b)] panic on the destination slice
   (e.g. Bytes48.SetBytes with a 40-byte input panics: b[32-40:] underflows).
   grep confirms no non-test caller anywhere in the tree invokes SetBytes on
   any of these four types, so nothing in production is affected today, but
   the method is unusable as written. TestG35BytesNSetBytesIsBroken below
   documents the panic directly instead of silently asserting a wrong value.
*/

package common

import (
	"fmt"
	"math/rand"
	"testing"
)

func TestG35Bytes4HexStringFormat(t *testing.T) {
	var b Bytes4
	copy(b[:], []byte{1, 2, 3, 4})
	if b.Hex() == "" {
		t.Fatalf("expected non-empty hex")
	}
	if b.String() != b.Hex() {
		t.Fatalf("expected String() == Hex()")
	}
	if got := fmt.Sprintf("%x", b); len(got) != 8 {
		t.Fatalf("expected 8 hex chars, got %q", got)
	}
	if got := fmt.Sprintf("%q", b); got[0] != '"' {
		t.Fatalf("expected quoted output")
	}
	if got := fmt.Sprintf("%d", b); got == "" {
		t.Fatalf("expected %%d output")
	}
	if got := fmt.Sprintf("%z", b); got == "" {
		t.Fatalf("expected default-case output")
	}
	if ts := b.TerminalString(); ts == "" {
		t.Fatalf("expected non-empty terminal string")
	}
}

func TestG35Bytes4MarshalUnmarshalValue(t *testing.T) {
	var b Bytes4
	copy(b[:], []byte{9, 8, 7, 6})
	text, err := b.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var back Bytes4
	if err := back.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if back != b {
		t.Fatalf("expected round-trip equality")
	}

	quoted := append(append([]byte{'"'}, text...), '"')
	var back2 Bytes4
	if err := back2.UnmarshalJSON(quoted); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}
	if back2 != b {
		t.Fatalf("expected JSON round-trip equality")
	}

	v, err := b.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if len(v.([]byte)) != 4 {
		t.Fatalf("expected 4-byte driver.Value")
	}
}

func TestG35Bytes4Generate(t *testing.T) {
	var b Bytes4
	r := rand.New(rand.NewSource(1))
	v := b.Generate(r, 1)
	if _, ok := v.Interface().(Bytes4); !ok {
		t.Fatalf("expected Generate to return a Bytes4 value")
	}
}

func TestG35Bytes48HexMarshalValue(t *testing.T) {
	var b Bytes48
	b[0] = 0xAB
	if b.Hex() == "" || b.String() != b.Hex() {
		t.Fatalf("expected Hex/String consistency")
	}
	text, err := b.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var back Bytes48
	if err := back.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if back != b {
		t.Fatalf("expected round-trip equality")
	}
	if _, err := b.Value(); err != nil {
		t.Fatalf("Value: %v", err)
	}
	if ts := b.TerminalString(); ts == "" {
		t.Fatalf("expected terminal string")
	}
	if got := fmt.Sprintf("%#x", b); len(got) == 0 {
		t.Fatalf("expected formatted output")
	}
}

func TestG35Bytes64HexMarshalValue(t *testing.T) {
	var b Bytes64
	b[0] = 0xCD
	text, err := b.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var back Bytes64
	if err := back.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if back != b {
		t.Fatalf("expected round-trip equality")
	}
	if _, err := b.Value(); err != nil {
		t.Fatalf("Value: %v", err)
	}
	if ts := b.TerminalString(); ts == "" {
		t.Fatalf("expected terminal string")
	}
	if got := fmt.Sprintf("%q", b); got[0] != '"' {
		t.Fatalf("expected quoted output")
	}
}

func TestG35Bytes96HexMarshalValueGenerate(t *testing.T) {
	var b Bytes96
	b[0] = 0xEF
	text, err := b.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}
	var back Bytes96
	if err := back.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if back != b {
		t.Fatalf("expected round-trip equality")
	}
	if _, err := b.Value(); err != nil {
		t.Fatalf("Value: %v", err)
	}
	if ts := b.TerminalString(); ts == "" {
		t.Fatalf("expected terminal string")
	}
	r := rand.New(rand.NewSource(2))
	v := b.Generate(r, 1)
	if _, ok := v.Interface().(Bytes96); !ok {
		t.Fatalf("expected Generate to return a Bytes96 value")
	}
	if got := fmt.Sprintf("%d", b); got == "" {
		t.Fatalf("expected %%d output")
	}
	if got := fmt.Sprintf("%z", b); got == "" {
		t.Fatalf("expected default-case output")
	}
}

// TestG35BytesNSetBytesIsBroken documents the pre-existing defect described
// in the file header comment: SetBytes on Bytes4/48/64/96 hard-codes
// length.Hash (32) instead of the receiver's own length, so it panics for
// most input lengths. This is not fixed here (non-test code is off limits);
// the test exists so the defect is pinned down rather than silently ignored.
func TestG35BytesNSetBytesIsBroken(t *testing.T) {
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if r := recover(); r == nil {
				t.Fatalf("%s: expected SetBytes to panic (known defect), but it did not", name)
			}
		}()
		f()
	}

	var b4 Bytes4
	mustPanic("Bytes4.SetBytes(4 bytes)", func() { b4.SetBytes([]byte{1, 2, 3, 4}) })

	var b48 Bytes48
	mustPanic("Bytes48.SetBytes(40 bytes)", func() { b48.SetBytes(make([]byte, 40)) })

	var b64 Bytes64
	mustPanic("Bytes64.SetBytes(40 bytes)", func() { b64.SetBytes(make([]byte, 40)) })

	var b96 Bytes96
	mustPanic("Bytes96.SetBytes(40 bytes)", func() { b96.SetBytes(make([]byte, 40)) })
}
