package rlp

import "testing"

func TestListPrefixLenAndEncode(t *testing.T) {
	if got := ListPrefixLen(10); got != 1 {
		t.Errorf("ListPrefixLen(10) = %d, want 1", got)
	}
	if got := ListPrefixLen(1000); got <= 1 {
		t.Errorf("ListPrefixLen(1000) = %d, want > 1", got)
	}

	buf := make([]byte, 16)
	n := EncodeListPrefix(10, buf)
	if n != 1 || buf[0] != 192+10 {
		t.Fatalf("EncodeListPrefix short: n=%d buf=%v", n, buf[:n])
	}

	n = EncodeListPrefix(1000, buf)
	if n != ListPrefixLen(1000) {
		t.Fatalf("EncodeListPrefix long: n=%d want %d", n, ListPrefixLen(1000))
	}
}

func TestU32LenAndEncodeU32(t *testing.T) {
	cases := []uint32{0, 1, 127, 128, 255, 256, 65535, 65536, 1 << 24, 1<<32 - 1}
	buf := make([]byte, 16)
	for _, v := range cases {
		wantLen := U32Len(v)
		n := EncodeU32(v, buf)
		if n != wantLen {
			t.Errorf("EncodeU32(%d): wrote %d bytes, U32Len reports %d", v, n, wantLen)
		}
	}
}

func TestU64LenAndEncodeU64(t *testing.T) {
	cases := []uint64{
		0, 1, 127, 128, 255, 256,
		1 << 8, 1<<16 - 1, 1 << 16, 1<<24 - 1, 1 << 24,
		1<<32 - 1, 1 << 32, 1<<40 - 1, 1 << 40,
		1<<48 - 1, 1 << 48, 1<<56 - 1, 1 << 56,
		^uint64(0),
	}
	buf := make([]byte, 16)
	for _, v := range cases {
		wantLen := U64Len(v)
		n := EncodeU64(v, buf)
		if n != wantLen {
			t.Errorf("EncodeU64(%d): wrote %d bytes, U64Len reports %d", v, n, wantLen)
		}
	}
}

func TestStringLenAndEncodeString2(t *testing.T) {
	cases := [][]byte{
		{},
		{0x01},             // single byte < 128
		{0x80},             // single byte >= 128
		[]byte("ab"),       // 1 < len < 56
		make([]byte, 55),   // boundary
		make([]byte, 56),   // >= 56
		make([]byte, 1000), // large
	}
	buf := make([]byte, 1100)
	for _, s := range cases {
		wantLen := StringLen(s)
		n := EncodeString2(s, buf)
		if n != wantLen {
			t.Errorf("EncodeString2(len=%d): wrote %d bytes, StringLen reports %d", len(s), n, wantLen)
		}
	}
}

func TestEncodeHash(t *testing.T) {
	h := make([]byte, 32)
	for i := range h {
		h[i] = byte(i)
	}
	to := make([]byte, 33)
	n := EncodeHash(h, to)
	if n != 33 {
		t.Fatalf("EncodeHash returned %d, want 33", n)
	}
	if to[0] != 128+32 {
		t.Fatalf("EncodeHash prefix byte = %#x, want 0xA0", to[0])
	}
}

func TestHashesLenAndEncodeHashes(t *testing.T) {
	hashes := make([]byte, 32*3)
	for i := range hashes {
		hashes[i] = byte(i)
	}
	wantLen := HashesLen(hashes)
	buf := make([]byte, wantLen+8)
	n := EncodeHashes(hashes, buf)
	if n != wantLen {
		t.Fatalf("EncodeHashes wrote %d bytes, HashesLen reports %d", n, wantLen)
	}
}

func TestHashesLenEmpty(t *testing.T) {
	if got := HashesLen(nil); got != ListPrefixLen(0) {
		t.Fatalf("HashesLen(nil) = %d, want %d", got, ListPrefixLen(0))
	}
}

func TestAnnouncementsLenAndEncodeEmpty(t *testing.T) {
	if got := AnnouncementsLen(nil, nil, nil); got != 4 {
		t.Fatalf("AnnouncementsLen(empty) = %d, want 4", got)
	}
	buf := make([]byte, 8)
	n := EncodeAnnouncements(nil, nil, nil, buf)
	if n != 4 {
		t.Fatalf("EncodeAnnouncements(empty) wrote %d, want 4", n)
	}
	want := []byte{0xc3, 0x80, 0xc0, 0xc0}
	for i, b := range want {
		if buf[i] != b {
			t.Fatalf("EncodeAnnouncements(empty) buf[%d] = %#x, want %#x", i, buf[i], b)
		}
	}
}

func TestAnnouncementsLenAndEncodeNonEmpty(t *testing.T) {
	types := []byte{1, 2, 3}
	sizes := []uint32{100, 200, 70000}
	hashes := make([]byte, 32*3)
	for i := range hashes {
		hashes[i] = byte(i)
	}

	wantLen := AnnouncementsLen(types, sizes, hashes)
	buf := make([]byte, wantLen+16)
	n := EncodeAnnouncements(types, sizes, hashes, buf)
	if n != wantLen {
		t.Fatalf("EncodeAnnouncements wrote %d bytes, AnnouncementsLen reports %d", n, wantLen)
	}
}
