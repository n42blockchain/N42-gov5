package datc

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	dst := filepath.Join(dir, "dst.bin")
	want := []byte("hello datc")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("copyFile content = %q, want %q", got, want)
	}
}

func TestCopyFileMissingSrc(t *testing.T) {
	dir := t.TempDir()
	if err := copyFile(filepath.Join(dir, "nope"), filepath.Join(dir, "dst")); err == nil {
		t.Fatal("expected error copying a nonexistent source")
	}
}

func TestFileNonEmpty(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	nonEmpty := filepath.Join(dir, "full")
	if err := os.WriteFile(nonEmpty, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if fileNonEmpty(empty) {
		t.Error("empty file reported non-empty")
	}
	if !fileNonEmpty(nonEmpty) {
		t.Error("non-empty file reported empty")
	}
	if fileNonEmpty(filepath.Join(dir, "missing")) {
		t.Error("missing file reported non-empty")
	}
}

func TestStoDepthSet(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	d := fs.Int("sto-depth", 3, "")
	if stoDepthSet(fs) {
		t.Fatal("stoDepthSet true before parsing")
	}
	if err := fs.Parse([]string{"-sto-depth", "5"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !stoDepthSet(fs) {
		t.Fatal("stoDepthSet false after explicit flag")
	}
	if *d != 5 {
		t.Fatalf("sto-depth = %d, want 5", *d)
	}
}

func TestStoDepthSetUntouched(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Int("sto-depth", 3, "")
	fs.Int("other", 0, "")
	if err := fs.Parse([]string{"-other", "9"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if stoDepthSet(fs) {
		t.Fatal("stoDepthSet true when sto-depth was not passed")
	}
}

func TestLoadStoDepthMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "map.txt")
	hash := make([]byte, stoDomainLen)
	for i := range hash {
		hash[i] = byte(i)
	}
	hexHash := ""
	for _, b := range hash {
		hexHash += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&0xf])
	}
	content := "# comment\n\n" + hexHash + " 3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	m, err := loadStoDepthMap(path)
	if err != nil {
		t.Fatalf("loadStoDepthMap: %v", err)
	}
	lad, ok := m[string(hash)]
	if !ok {
		t.Fatal("expected entry for contract hash")
	}
	if lad.depth != 3 {
		t.Fatalf("depth = %d, want 3", lad.depth)
	}
	if lad.level != 2 { // default level = d-1
		t.Fatalf("level = %d, want 2", lad.level)
	}
	if lad.shift != 0 {
		t.Fatalf("shift = %d, want 0", lad.shift)
	}
}

func TestLoadStoDepthMapWithShiftAndLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "map.txt")
	hexHash := ""
	for i := 0; i < stoDomainLen; i++ {
		hexHash += "ab"
	}
	if err := os.WriteFile(path, []byte(hexHash+" 4 2 7\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	m, err := loadStoDepthMap(path)
	if err != nil {
		t.Fatalf("loadStoDepthMap: %v", err)
	}
	var key [stoDomainLen]byte
	for i := range key {
		key[i] = 0xab
	}
	lad, ok := m[string(key[:])]
	if !ok {
		t.Fatal("missing entry")
	}
	if lad.depth != 4 || lad.level != 2 || lad.shift != 7 {
		t.Fatalf("got %+v, want depth=4 level=2 shift=7", lad)
	}
}

func TestLoadStoDepthMapMissingFile(t *testing.T) {
	if _, err := loadStoDepthMap("/nonexistent/path/map.txt"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadStoDepthMapBadLines(t *testing.T) {
	cases := []string{
		"onlyonefield\n",
		"zz 3\n",                       // bad hex
		"aabb 3\n",                     // wrong length
		"aa 999\n",                     // depth out of range (still too short hash so hits bad addrHash first in practice)
		"1 2 3 4 5\n",                  // too many fields
	}
	for i, c := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "map.txt")
		if err := os.WriteFile(path, []byte(c), 0o644); err != nil {
			t.Fatalf("setup %d: %v", i, err)
		}
		if _, err := loadStoDepthMap(path); err == nil {
			t.Errorf("case %d (%q): expected error", i, c)
		}
	}
}

func TestStoLadderForDefault(t *testing.T) {
	b := &builder{stoDepth: 5}
	lad := b.stoLadderFor([]byte("anything"))
	if lad.depth != 5 {
		t.Fatalf("depth = %d, want 5 (fallback to builder default)", lad.depth)
	}
	if b.stoDepthFor([]byte("anything")) != 5 {
		t.Fatal("stoDepthFor mismatch with stoLadderFor")
	}
}

func TestStoLadderForMapOverride(t *testing.T) {
	domain := []byte("contract-domain-key-32-bytes...")
	b := &builder{
		stoDepth: 2,
		stoDepthMap: map[string]stoLadder{
			string(domain): {depth: 9, level: 8, shift: 3},
		},
	}
	lad := b.stoLadderFor(domain)
	if lad.depth != 9 || lad.level != 8 || lad.shift != 3 {
		t.Fatalf("got %+v, want override from map", lad)
	}
	// An unrelated domain falls back to the builder default.
	other := b.stoLadderFor([]byte("other"))
	if other.depth != 2 {
		t.Fatalf("other depth = %d, want fallback 2", other.depth)
	}
}

func TestStoLadderShiftAt(t *testing.T) {
	lad := stoLadder{depth: 4, level: 3, shift: 6}
	if got := lad.shiftAt(3); got != 6 {
		t.Fatalf("shiftAt(level) = %d, want 6", got)
	}
	if got := lad.shiftAt(2); got != 0 {
		t.Fatalf("shiftAt(non-level) = %d, want 0", got)
	}
	zero := stoLadder{depth: 4, level: 3, shift: 0}
	if got := zero.shiftAt(3); got != 0 {
		t.Fatalf("shiftAt with zero shift = %d, want 0", got)
	}
}

func TestStoShiftsAt(t *testing.T) {
	b := &builder{}
	got := b.stoShiftsAt(0)
	if len(got) != 1 || got[0] != 0 {
		t.Fatalf("default stoShiftsAt = %v, want [0]", got)
	}
	b.stoShifts = []uint8{0, 3, 7}
	got = b.stoShiftsAt(0)
	if len(got) != 3 {
		t.Fatalf("stoShiftsAt with populated shifts = %v", got)
	}
}
