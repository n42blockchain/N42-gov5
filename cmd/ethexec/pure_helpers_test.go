package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEthexecBytesEqualPrefix(t *testing.T) {
	cases := []struct {
		k, prefix []byte
		want      bool
	}{
		{[]byte("hello"), []byte("he"), true},
		{[]byte("hello"), []byte("hello"), true},
		{[]byte("he"), []byte("hello"), false},
		{[]byte("hello"), []byte("xy"), false},
		{[]byte(""), []byte(""), true},
	}
	for _, c := range cases {
		if got := bytesEqualPrefix(c.k, c.prefix); got != c.want {
			t.Errorf("bytesEqualPrefix(%q,%q) = %v, want %v", c.k, c.prefix, got, c.want)
		}
	}
}

func TestEthexecKeccak256(t *testing.T) {
	// Known vector: keccak256("") = c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470
	h := keccak256(nil)
	want := "c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
	got := ""
	for _, b := range h {
		got += hexByte(b)
	}
	if got != want {
		t.Fatalf("keccak256(nil) = %s, want %s", got, want)
	}
}

func hexByte(b byte) string {
	const hexd = "0123456789abcdef"
	return string([]byte{hexd[b>>4], hexd[b&0xf]})
}

func TestEthexecUnwrapRethBytecodeRawPassthrough(t *testing.T) {
	code := []byte{0x60, 0x01, 0x60, 0x02}
	key := keccak256(code)
	if got := unwrapRethBytecode(code, key); string(got) != string(code) {
		t.Fatalf("unwrapRethBytecode raw passthrough = %x, want %x", got, code)
	}
}

func TestEthexecUnwrapRethBytecodeLengthPrefixed(t *testing.T) {
	code := []byte{0x60, 0x01, 0x60, 0x02, 0x00}
	key := keccak256(code)
	v := make([]byte, 4+len(code))
	v[3] = byte(len(code)) // BE length in last byte
	copy(v[4:], code)
	if got := unwrapRethBytecode(v, key); string(got) != string(code) {
		t.Fatalf("unwrapRethBytecode length-prefixed = %x, want %x", got, code)
	}
}

func TestEthexecUnwrapRethBytecodeNoMatch(t *testing.T) {
	v := []byte{0, 0, 0, 5, 1, 2, 3, 4, 5}
	key := make([]byte, 32) // never matches
	if got := unwrapRethBytecode(v, key); got != nil {
		t.Fatalf("unwrapRethBytecode = %x, want nil for no match", got)
	}
}

func TestEthexecDbStatsIsCidxFile(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"headers.cidx", true},
		{"storcs.cidx", true},
		{"abc.txt", false},
		{"foo.idx", false},
		{"a.cidx", true}, // len>=6? "a.cidx" is 6 chars
	}
	for _, c := range cases {
		if got := dbStatsIsCidxFile(c.name); got != c.want {
			t.Errorf("dbStatsIsCidxFile(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestEthexecDbStatsDiscoverFreezerTables(t *testing.T) {
	dir := t.TempDir()
	names := []string{"headers.cidx", "bodies.cidx", "notes.txt", "sub"}
	for _, n := range names {
		if n == "sub" {
			if err := os.Mkdir(filepath.Join(dir, n), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	got := dbStatsDiscoverFreezerTables(dir)
	if len(got) != 2 {
		t.Fatalf("discovered %d tables, want 2: %+v", len(got), got)
	}
	if got[0].name != "bodies" || got[1].name != "headers" {
		t.Fatalf("unexpected table names: %+v", got)
	}
}

func TestEthexecDbStatsDiscoverFreezerTablesMissingDir(t *testing.T) {
	if got := dbStatsDiscoverFreezerTables("/no/such/dir"); got != nil {
		t.Fatalf("expected nil for missing dir, got %+v", got)
	}
}

func TestEthexecDbStatsClassifyTable(t *testing.T) {
	cases := map[string]string{
		"headers":  "block",
		"codes":    "key",
		"accthist": "shard",
		"unknown1": "unknown",
	}
	for name, want := range cases {
		if got := dbStatsClassifyTable(name); got != want {
			t.Errorf("dbStatsClassifyTable(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestEthexecDbStatsEntrySize(t *testing.T) {
	cases := map[string]int{
		"bodies":   8,
		"headers":  8,
		"accthist": 12,
		"storhist": 12,
		"txindex":  12,
		"codes":    6,
	}
	for name, want := range cases {
		if got := dbStatsEntrySize(name); got != want {
			t.Errorf("dbStatsEntrySize(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestEthexecDbStatsBlocksPerEntry(t *testing.T) {
	cases := map[string]uint64{
		"bodies":   8192,
		"headers":  8192,
		"accthist": 1_000_000,
		"codes":    1,
	}
	for name, want := range cases {
		if got := dbStatsBlocksPerEntry(name); got != want {
			t.Errorf("dbStatsBlocksPerEntry(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestEthexecDbStatsHumanBytes(t *testing.T) {
	if got := dbStatsHumanBytes(0); got == "" {
		t.Fatal("dbStatsHumanBytes(0) returned empty string")
	}
	if got := dbStatsHumanBytes(1024); got == "" {
		t.Fatal("dbStatsHumanBytes(1024) returned empty string")
	}
}

func TestEthexecDbStatsHumanBytesF(t *testing.T) {
	if got := dbStatsHumanBytesF(0); got != "-" {
		t.Fatalf("dbStatsHumanBytesF(0) = %q, want -", got)
	}
	if got := dbStatsHumanBytesF(-5); got != "-" {
		t.Fatalf("dbStatsHumanBytesF(-5) = %q, want -", got)
	}
	if got := dbStatsHumanBytesF(2048); got == "-" {
		t.Fatal("dbStatsHumanBytesF(2048) unexpectedly returned -")
	}
}

func TestEthexecDbStatsCommaInt(t *testing.T) {
	cases := []struct {
		n    uint64
		want string
	}{
		{0, "0"},
		{5, "5"},
		{100, "100"},
		{1000, "1,000"},
		{1234567, "1,234,567"},
	}
	for _, c := range cases {
		if got := dbStatsCommaInt(c.n); got != c.want {
			t.Errorf("dbStatsCommaInt(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestEthexecDbStatsKnownTables(t *testing.T) {
	got := dbStatsKnownTables()
	if len(got) == 0 {
		t.Fatal("dbStatsKnownTables returned no tables")
	}
}
