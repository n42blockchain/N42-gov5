// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers the small nibble-conversion helpers left at 0%: uncompactNibbles
// (the compact-bytes -> nibbles inverse of HexNibblesToCompactBytes, including
// the terminator flag and odd-length cases), updatedNibs (bitmask -> comma
// list of hex digits, used in diagnostics), and PrefixStringToNibbles
// (hex-string -> nibble bytes, including its error path on invalid hex).

package commitment

import (
	"reflect"
	"testing"
)

func TestUncompactNibbles(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"empty", nil, nil},
		{"even-no-term", []byte{0x00, 0x12, 0x34}, []byte{1, 2, 3, 4}},
		{"odd-no-term", []byte{0x15, 0x23}, []byte{5, 2, 3}},
		{"even-term", []byte{0x20, 0x12}, []byte{1, 2, terminatorHexByte}},
		{"odd-term", []byte{0x35, 0x23}, []byte{5, 2, 3, terminatorHexByte}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := uncompactNibbles(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("uncompactNibbles(% x) = % x, want % x", c.in, got, c.want)
			}
		})
	}
}

// Round trip: HexNibblesToCompactBytes then uncompactNibbles must reproduce
// the original nibble slice, for both terminated and non-terminated keys.
func TestUncompactNibblesRoundTripsWithCompact(t *testing.T) {
	cases := [][]byte{
		{1, 2, 3, 4, 5, 6},
		{1, 2, 3, 4, 5, 6, terminatorHexByte},
		{0xf, 0xe, 0xd},
		{0xf, 0xe, 0xd, terminatorHexByte},
		{},
	}
	for _, nibs := range cases {
		compact := HexNibblesToCompactBytes(nibs)
		got := uncompactNibbles(compact)
		if len(got) != len(nibs) {
			t.Fatalf("round trip %v -> compact %x -> %v, want same length as %v", nibs, compact, got, nibs)
		}
		for i := range nibs {
			if got[i] != nibs[i] {
				t.Fatalf("round trip %v -> compact %x -> %v, want %v", nibs, compact, got, nibs)
			}
		}
	}
}

func TestUpdatedNibs(t *testing.T) {
	cases := []struct {
		num  uint16
		want string
	}{
		{0, ""},
		{1, "0"},
		{0b1010, "1,3"},
		{0xFFFF, "0,1,2,3,4,5,6,7,8,9,A,B,C,D,E,F"},
	}
	for _, c := range cases {
		if got := updatedNibs(c.num); got != c.want {
			t.Fatalf("updatedNibs(%d) = %q, want %q", c.num, got, c.want)
		}
	}
}

func TestPrefixStringToNibbles(t *testing.T) {
	got, err := PrefixStringToNibbles("1a2F")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 0xa, 2, 0xf}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PrefixStringToNibbles = % x, want % x", got, want)
	}

	if _, err := PrefixStringToNibbles(""); err != nil {
		t.Fatalf("empty string must not error, got %v", err)
	}

	if _, err := PrefixStringToNibbles("1g"); err == nil {
		t.Fatal("expected an error for a non-hex character")
	}
}
