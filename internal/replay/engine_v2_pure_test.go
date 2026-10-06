// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"

	log2 "github.com/n42blockchain/N42/lib/log/v3"

	"github.com/n42blockchain/N42/common/types"
)

func TestPtrU64AndCopyHashAndNewDefaultExtra(t *testing.T) {
	p1 := ptrU64(7)
	p2 := ptrU64(7)
	if p1 == p2 {
		t.Fatal("ptrU64 returned the same pointer for two calls")
	}
	if *p1 != 7 {
		t.Fatalf("*ptrU64(7) = %d, want 7", *p1)
	}

	h := types.Hash{0x01}
	hp1 := copyHash(h)
	hp2 := copyHash(h)
	if hp1 == hp2 {
		t.Fatal("copyHash returned the same pointer for two calls")
	}
	if *hp1 != h {
		t.Fatalf("*copyHash(h) = %x, want %x", *hp1, h)
	}
	// Mutating the source after copyHash must not affect the copy.
	h[1] = 0xFF
	if *hp1 == h {
		t.Fatal("copyHash aliased the source instead of copying")
	}

	extra := newDefaultExtra()
	if len(extra) != 32 {
		t.Fatalf("newDefaultExtra len = %d, want 32", len(extra))
	}
	for _, b := range extra {
		if b != 0 {
			t.Fatal("newDefaultExtra is not all-zero")
		}
	}
	// Each call returns an independent slice.
	extra2 := newDefaultExtra()
	extra[0] = 0xFF
	if extra2[0] != 0 {
		t.Fatal("newDefaultExtra shares backing storage across calls")
	}
}

func TestParseHaveWant(t *testing.T) {
	h, w, ok := parseHaveWant("sender balance have 100 want 250")
	if !ok {
		t.Fatal("parseHaveWant failed to parse a valid message")
	}
	if h.Cmp(uint256.NewInt(100)) != 0 || w.Cmp(uint256.NewInt(250)) != 0 {
		t.Fatalf("parseHaveWant = %s, %s, want 100, 250", h.Dec(), w.Dec())
	}

	if _, _, ok := parseHaveWant("no markers here"); ok {
		t.Fatal("parseHaveWant succeeded on a message with no have/want markers")
	}
	if _, _, ok := parseHaveWant("have 1 want "); ok {
		t.Fatal("parseHaveWant succeeded with an unparsable want value")
	}
	if _, _, ok := parseHaveWant("want 5 have 1"); ok {
		t.Fatal("parseHaveWant succeeded with want appearing before have")
	}
}

func TestDumpTopupsWritesFileAndNoopsWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "topups.tsv")

	addr := types.Address{0x01}
	e := &EngineV2{
		cfg: ConfigV2{AutoTopup: true, TopupDumpFile: outFile},
		log: log2.New(),
		topups: map[types.Address]*uint256.Int{
			addr: uint256.NewInt(1_000_000_000_000_000_000), // 1 ETH
		},
	}
	e.dumpTopups()

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("expected dumpTopups to write %s: %v", outFile, err)
	}
	if len(data) == 0 {
		t.Fatal("dumpTopups wrote an empty file")
	}

	// AutoTopup disabled: no-op, no file written.
	outFile2 := filepath.Join(dir, "topups2.tsv")
	e2 := &EngineV2{
		cfg:    ConfigV2{AutoTopup: false, TopupDumpFile: outFile2},
		log:    log2.New(),
		topups: map[types.Address]*uint256.Int{addr: uint256.NewInt(1)},
	}
	e2.dumpTopups()
	if _, err := os.Stat(outFile2); err == nil {
		t.Fatal("dumpTopups wrote a file despite AutoTopup=false")
	}

	// Empty topups map: no-op even with AutoTopup enabled.
	outFile3 := filepath.Join(dir, "topups3.tsv")
	e3 := &EngineV2{
		cfg:    ConfigV2{AutoTopup: true, TopupDumpFile: outFile3},
		log:    log2.New(),
		topups: map[types.Address]*uint256.Int{},
	}
	e3.dumpTopups()
	if _, err := os.Stat(outFile3); err == nil {
		t.Fatal("dumpTopups wrote a file despite an empty topups map")
	}
}
