package rlp2

import (
	"bytes"
	"testing"
)

func TestEncoderShortString(t *testing.T) {
	e := NewEncoder(nil)
	e.Str([]byte("cat"))
	got := e.Buffer()
	want := []byte{0x83, 'c', 'a', 't'}
	if !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestEncoderLongString(t *testing.T) {
	s := bytes.Repeat([]byte{0x41}, 60)
	e := NewEncoder(nil)
	e.Str(s)
	got := e.Buffer()
	// decode back using parse.go to confirm correctness
	dataPos, dataLen, err := String(got, 0)
	if err != nil {
		t.Fatalf("String decode error: %v", err)
	}
	if dataLen != len(s) {
		t.Fatalf("dataLen=%d want %d", dataLen, len(s))
	}
	if !bytes.Equal(got[dataPos:dataPos+dataLen], s) {
		t.Errorf("content mismatch")
	}
}

func TestEncoderListShort(t *testing.T) {
	e := NewEncoder(nil)
	e.List(
		func(i *Encoder) *Encoder { return i.Str([]byte("a")) },
		func(i *Encoder) *Encoder { return i.Str([]byte("b")) },
	)
	got := e.Buffer()
	dataPos, dataLen, isList, err := Prefix(got, 0)
	if err != nil {
		t.Fatalf("Prefix error: %v", err)
	}
	if !isList {
		t.Fatalf("expected list")
	}
	if dataPos+dataLen != len(got) {
		t.Errorf("list bounds mismatch")
	}
}

// NOTE: Encoder.List / LongList with a payload >55 bytes (the "long list"
// header path in writeList) writes an incorrect length byte (observed: 0x00
// instead of the actual payload length), producing a non-canonical/corrupt
// RLP header. This looks like a pre-existing bug in writeList's putUint
// placement, reproducible with e.g. a single >56-byte string item. Left
// uncovered/unasserted here per instructions not to modify non-test code;
// see test-writer report.

func TestEncoderBytesAndByte(t *testing.T) {
	e := NewEncoder(nil)
	e.Byte(0xAA).Bytes([]byte{0xBB, 0xCC})
	got := e.Buffer()
	want := []byte{0xAA, 0xBB, 0xCC}
	if !bytes.Equal(got, want) {
		t.Errorf("got %x want %x", got, want)
	}
}

func TestDecoderBasic(t *testing.T) {
	e := NewEncoder(nil)
	e.List(
		func(i *Encoder) *Encoder { return i.Str([]byte("hello")) },
		func(i *Encoder) *Encoder { return i.Str([]byte("world")) },
	)
	raw := e.Buffer()

	d := NewDecoder(raw)
	tok, err := d.PeekToken()
	if err != nil {
		t.Fatalf("PeekToken error: %v", err)
	}
	if !tok.IsListType() {
		t.Fatalf("expected list token, got %v", tok)
	}

	elem, token, err := d.Elem()
	if err != nil {
		t.Fatalf("Elem error: %v", err)
	}
	if !token.IsListType() {
		t.Fatalf("expected list type")
	}

	inner := NewDecoder(elem)
	var got []string
	for !inner.Empty() {
		b, tk, err := inner.Elem()
		if err != nil {
			t.Fatalf("inner Elem error: %v", err)
		}
		if !tk.IsBlobType() && tk != TokenDecimal {
			t.Fatalf("unexpected token %v", tk)
		}
		got = append(got, string(b))
	}
	if len(got) != 2 || got[0] != "hello" || got[1] != "world" {
		t.Errorf("got %v", got)
	}
}

func TestDecoderForList(t *testing.T) {
	e := NewEncoder(nil)
	e.List(
		func(i *Encoder) *Encoder { return i.Str([]byte("x")) },
		func(i *Encoder) *Encoder { return i.Str([]byte("y")) },
		func(i *Encoder) *Encoder { return i.Str([]byte("z")) },
	)
	raw := e.Buffer()
	d := NewDecoder(raw)
	var items []string
	err := d.ForList(func(dec *Decoder) error {
		b, _, err := dec.Elem()
		if err != nil {
			return err
		}
		items = append(items, string(b))
		return nil
	})
	if err != nil {
		t.Fatalf("ForList error: %v", err)
	}
	if len(items) != 3 || items[0] != "x" || items[1] != "y" || items[2] != "z" {
		t.Errorf("got %v", items)
	}
}

func TestDecoderForListOnNonList(t *testing.T) {
	d := NewDecoder([]byte{0x01})
	err := d.ForList(func(dec *Decoder) error { return nil })
	if err == nil {
		t.Error("expected error for ForList on non-list")
	}
}

func TestDecoderReadByteAndRebase(t *testing.T) {
	d := NewDecoder([]byte{0x01, 0x02, 0x03})
	b, err := d.ReadByte()
	if err != nil || b != 0x01 {
		t.Fatalf("ReadByte = %v, %v", b, err)
	}
	peek, err := d.PeekByte()
	if err != nil || peek != 0x02 {
		t.Fatalf("PeekByte = %v, %v", peek, err)
	}
	if d.Offset() != 1 {
		t.Errorf("Offset = %d, want 1", d.Offset())
	}
	d.Rebase()
	if d.Offset() != 0 {
		t.Errorf("after Rebase Offset = %d, want 0", d.Offset())
	}
	if !bytes.Equal(d.Bytes(), []byte{0x02, 0x03}) {
		t.Errorf("Bytes after rebase = %x", d.Bytes())
	}
}

func TestDecoderFork(t *testing.T) {
	d := NewDecoder([]byte{0x01, 0x02, 0x03})
	_, _ = d.ReadByte()
	f := d.Fork()
	if f.Offset() != d.Offset() {
		t.Errorf("forked decoder offset mismatch")
	}
	_, _ = f.ReadByte()
	if f.Offset() == d.Offset() {
		t.Errorf("fork should be independent")
	}
}

func TestDecoderString(t *testing.T) {
	d := NewDecoder([]byte{0x01, 0x02})
	s := d.String()
	if s == "" {
		t.Error("expected non-empty debug string")
	}
}

func TestDecoderEmptyAndUnderlying(t *testing.T) {
	d := NewDecoder([]byte{})
	if !d.Empty() {
		t.Error("expected empty decoder")
	}
	if len(d.Underlying()) != 0 {
		t.Error("expected empty underlying")
	}
	if len(d.Consumed()) != 0 {
		t.Error("expected empty consumed")
	}
}

func TestReadElemHelper(t *testing.T) {
	e := NewEncoder(nil)
	e.Str([]byte("hi"))
	raw := e.Buffer()
	d := NewDecoder(raw)
	var dst []byte
	err := ReadElem(d, Bytes, &dst)
	if err != nil {
		t.Fatalf("ReadElem error: %v", err)
	}
	if string(dst) != "hi" {
		t.Errorf("got %q", dst)
	}
}

func TestElemErrorOnEOF(t *testing.T) {
	d := NewDecoder([]byte{})
	_, _, err := d.Elem()
	if err == nil {
		t.Error("expected error reading elem from empty buffer")
	}
}

func TestElemTruncatedLongBlob(t *testing.T) {
	// 0xb9 => long blob with 2-byte length, but then truncated
	d := NewDecoder([]byte{0xb9, 0x00})
	_, _, err := d.Elem()
	if err == nil {
		t.Error("expected error for truncated long blob length")
	}
}

func TestRawElemAndRawElemDec(t *testing.T) {
	e := NewEncoder(nil)
	e.Str([]byte("payload"))
	raw := e.Buffer()
	d := NewDecoder(raw)
	rawBytes, tok, err := d.RawElem()
	if err != nil {
		t.Fatalf("RawElem error: %v", err)
	}
	if !bytes.Equal(rawBytes, raw) {
		t.Errorf("RawElem returned %x, want %x", rawBytes, raw)
	}
	if !tok.IsBlobType() {
		t.Errorf("expected blob token")
	}

	d2 := NewDecoder(raw)
	dec, tok2, err := d2.RawElemDec()
	if err != nil {
		t.Fatalf("RawElemDec error: %v", err)
	}
	if !tok2.IsBlobType() {
		t.Errorf("expected blob token")
	}
	if dec.Empty() {
		t.Errorf("sub-decoder should not be empty")
	}
}

func TestElemDec(t *testing.T) {
	e := NewEncoder(nil)
	e.Str([]byte("sub"))
	raw := e.Buffer()
	d := NewDecoder(raw)
	dec, tok, err := d.ElemDec()
	if err != nil {
		t.Fatalf("ElemDec error: %v", err)
	}
	if !tok.IsBlobType() {
		t.Errorf("expected blob type")
	}
	b, err := dec.ReadByte()
	if err != nil || b != 's' {
		t.Errorf("sub-decoder content mismatch: %v %v", b, err)
	}
}

func TestTokenHelpers(t *testing.T) {
	if TokenDecimal.String() != "decimal" {
		t.Errorf("unexpected string for TokenDecimal")
	}
	if TokenShortBlob.String() != "short_blob" {
		t.Errorf("unexpected string for TokenShortBlob")
	}
	if TokenLongBlob.String() != "long_blob" {
		t.Errorf("unexpected string")
	}
	if TokenShortList.String() != "short_list" {
		t.Errorf("unexpected string")
	}
	if TokenLongList.String() != "long_list" {
		t.Errorf("unexpected string")
	}
	if TokenEOF.String() != "eof" {
		t.Errorf("unexpected string")
	}
	if TokenUnknown.String() != "unknown" {
		t.Errorf("unexpected string")
	}
	if Token(0x12345).String() != "nan" {
		t.Errorf("unexpected string for unmapped token")
	}
	if !TokenShortList.IsListType() || !TokenLongList.IsListType() {
		t.Error("expected list types")
	}
	if TokenShortBlob.IsListType() {
		t.Error("blob should not be list type")
	}
	if !TokenShortBlob.IsBlobType() || !TokenLongBlob.IsBlobType() {
		t.Error("expected blob types")
	}
	if TokenShortList.IsBlobType() {
		t.Error("list should not be blob type")
	}
}
