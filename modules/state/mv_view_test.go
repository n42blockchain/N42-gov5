// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"testing"
)

func TestMVStateView_BaseOnly(t *testing.T) {
	// No MVHashMap writes; all reads hit the base.
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(map[string][]byte{
		"a": []byte("A"),
		"b": []byte("B"),
	})
	v := NewMVStateView(mv, base, 5, 0)

	got, err := v.Get([]byte("a"))
	if err != nil {
		t.Fatalf("Get(a): %v", err)
	}
	if !bytes.Equal(got, []byte("A")) {
		t.Errorf("val: got %s want A", got)
	}
	rs := v.ReadSet()
	if len(rs) != 1 || rs[0].Source != ReadFromBase {
		t.Errorf("readSet: %+v", rs)
	}
}

func TestMVStateView_MVHit(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(map[string][]byte{"a": []byte("baseA")})
	// Tx 3 writes "a" = "mvA" at inc 0.
	mv.Write([]byte("a"), Version{TxIdx: 3}, []byte("mvA"))

	// Tx 7 reads: sees tx 3's write, NOT base.
	v := NewMVStateView(mv, base, 7, 0)
	got, _ := v.Get([]byte("a"))
	if !bytes.Equal(got, []byte("mvA")) {
		t.Errorf("val: got %s want mvA", got)
	}
	rs := v.ReadSet()
	if len(rs) != 1 || rs[0].Source != ReadFromMV || rs[0].WriterVer.TxIdx != 3 {
		t.Errorf("readSet: %+v", rs)
	}
}

func TestMVStateView_SelfRead(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)
	v := NewMVStateView(mv, base, 5, 0)

	v.Set([]byte("x"), []byte("tx5wrote"))
	// Self-read returns own write, does NOT enter readSet (self-reads
	// are tx-local — no dependency on external state).
	got, _ := v.Get([]byte("x"))
	if !bytes.Equal(got, []byte("tx5wrote")) {
		t.Errorf("self-read val: got %s want tx5wrote", got)
	}
	if len(v.ReadSet()) != 0 {
		t.Errorf("self-read should NOT be in readSet: %+v", v.ReadSet())
	}
}

func TestMVStateView_EstimateAborts(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)
	mv.Write([]byte("k"), Version{TxIdx: 3}, []byte("V3"))
	mv.MarkEstimate([]byte("k"), 3)

	v := NewMVStateView(mv, base, 7, 0)
	val, _ := v.Get([]byte("k"))
	if val != nil {
		t.Errorf("read of estimate should return nil; got %s", val)
	}
	if !v.AbortPending() {
		t.Error("AbortPending should be true")
	}
	if v.BlockingWriter().TxIdx != 3 {
		t.Errorf("BlockingWriter: got %d want 3", v.BlockingWriter().TxIdx)
	}
}

func TestMVStateView_FlushAndValidate(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(map[string][]byte{"a": []byte("A0")})

	// Tx 5 reads "a" (from base), writes "b" = "5wroteB".
	v5 := NewMVStateView(mv, base, 5, 0)
	if _, err := v5.Get([]byte("a")); err != nil {
		t.Fatal(err)
	}
	v5.Set([]byte("b"), []byte("5wroteB"))
	keys5 := v5.FlushWrites()
	if len(keys5) != 1 || keys5[0] != "b" {
		t.Errorf("flushed keys: %v", keys5)
	}

	// Validate at this point: nothing has changed, should pass.
	if !v5.Validate() {
		t.Error("validate should pass when no concurrent writes")
	}

	// Now a concurrent earlier tx (tx 3) writes "a" = "A-from-3".
	// This INVALIDATES tx 5's read of "a" (it read from base, but
	// now there's a closer writer).
	mv.Write([]byte("a"), Version{TxIdx: 3}, []byte("A-from-3"))
	if v5.Validate() {
		t.Error("validate should FAIL after earlier tx wrote same key")
	}
}

func TestMVStateView_ValidateMVWriterChanged(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	// Tx 2 wrote "k"=V0.
	mv.Write([]byte("k"), Version{TxIdx: 2, Incarnation: 0}, []byte("V0"))

	// Tx 7 reads it, captures (writer=2, inc=0).
	v := NewMVStateView(mv, base, 7, 0)
	if _, err := v.Get([]byte("k")); err != nil {
		t.Fatal(err)
	}

	// Tx 2 re-executed and now writes V1 (inc 1).
	mv.Write([]byte("k"), Version{TxIdx: 2, Incarnation: 1}, []byte("V1"))

	// Tx 7's validate should fail — same writer, different incarnation.
	if v.Validate() {
		t.Error("validate should fail when writer's incarnation changed")
	}
}

func TestMVStateView_DropWrites(t *testing.T) {
	mv := NewMVHashMap(16)
	base := NewMapBaseReader(nil)

	v := NewMVStateView(mv, base, 5, 0)
	v.Set([]byte("x"), []byte("X"))
	keys := v.FlushWrites()

	// Anyone reading later sees X.
	val, _, st := mv.Read([]byte("x"), 6)
	if st != MVOk || string(val) != "X" {
		t.Fatalf("pre-drop: st=%d val=%s", st, val)
	}

	// Drop writes.
	v.DropWrites(keys)

	// Now readers fall through.
	_, _, st = mv.Read([]byte("x"), 6)
	if st != MVNotFound {
		t.Errorf("post-drop: st=%d want NotFound", st)
	}
}
