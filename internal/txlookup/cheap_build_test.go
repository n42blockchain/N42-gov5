package txlookup

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/recsplit"
)

// TestSegmentStoreCheapBuildIdentical builds the same segments with
// recsplit.CheapBuild on and off and requires every file of the segment
// store to be byte-identical, then resolves every hash from the cheap build.
func TestSegmentStoreCheapBuildIdentical(t *testing.T) {
	prevSalt := sourceSaltForTest
	sourceSaltForTest = 0x7a11
	defer func() { sourceSaltForTest = prevSalt }()

	const start = 5_000
	perBlock := make([][]types.Hash, 9)
	var buf [16]byte
	for bl := range perBlock {
		perBlock[bl] = make([]types.Hash, 3_000+bl*1_111)
		for i := range perBlock[bl] {
			binary.BigEndian.PutUint64(buf[:], uint64(bl))
			binary.BigEndian.PutUint64(buf[8:], uint64(i))
			perBlock[bl][i] = crypto.Keccak256Hash(buf[:])
		}
	}
	perBlock[4] = nil // an empty block inside a segment
	src := memSource(start, perBlock)

	build := func(cheap bool) (string, map[string][]byte) {
		prev := recsplit.CheapBuild
		recsplit.CheapBuild = cheap
		defer func() { recsplit.CheapBuild = prev }()
		dir := t.TempDir()
		for _, r := range [][2]uint64{{start, start + 5}, {start + 5, start + 9}} {
			if err := BuildSegmentFromSource(context.Background(), dir, r[0], r[1], src); err != nil {
				t.Fatal(err)
			}
		}
		files := map[string][]byte{}
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range ents {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			files[e.Name()] = b
		}
		return dir, files
	}
	_, off := build(false)
	dirOn, on := build(true)
	if len(off) == 0 || len(off) != len(on) {
		t.Fatalf("file sets differ: %d vs %d", len(off), len(on))
	}
	names := make([]string, 0, len(off))
	for n := range off {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !bytes.Equal(off[n], on[n]) {
			t.Fatalf("%s differs (%d vs %d bytes)", n, len(off[n]), len(on[n]))
		}
	}

	svc, err := NewService(dirOn)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for bl, hs := range perBlock {
		for _, h := range hs {
			got, err := svc.Lookup(nil, h)
			if err != nil || got == nil {
				t.Fatalf("lookup %x: %v %v", h, got, err)
			}
			// Without a verifier an older segment's hash can phantom-hit the
			// newer segment (1/256 existence fingerprint), so only the newest
			// segment's blocks must resolve exactly.
			if bl >= 5 && *got != uint64(start+bl) {
				t.Fatalf("lookup %x: block %d, want %d", h, *got, start+bl)
			}
		}
	}
}
