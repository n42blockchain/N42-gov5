package recsplit

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/log/v3"
)

// TestInMemoryMatchesEtl builds the same keys through the etl collectors and
// through InMemory and requires byte-identical index files, for the Enums +
// LessFalsePositives shape txlookup uses and for plain offsets.
func TestInMemoryMatchesEtl(t *testing.T) {
	for _, tc := range []struct {
		name         string
		enums, lfp   bool
		keys, bucket int
	}{
		{"enums-lfp", true, true, 50_000, 2000},
		{"enums", true, false, 7_001, 100},
		{"offsets", false, false, 20_000, 2000},
		{"one", true, true, 1, 2000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			build := func(inMemory bool) []byte {
				dir := t.TempDir()
				idx := filepath.Join(dir, "x.idx")
				rs, err := NewRecSplit(RecSplitArgs{
					KeyCount: tc.keys, BucketSize: tc.bucket, LeafSize: 8, Salt: 42,
					Enums: tc.enums, LessFalsePositives: tc.lfp,
					IndexFile: idx, TmpDir: dir, InMemory: inMemory,
					EtlBufLimit: 64 * 1024, // force the etl path to spill and merge
				}, log.New())
				if err != nil {
					t.Fatal(err)
				}
				defer rs.Close()
				rs.DisableFsync()
				var k [8]byte
				for i := 0; i < tc.keys; i++ {
					binary.BigEndian.PutUint64(k[:], uint64(i)*0x9E3779B97F4A7C15)
					off := uint64(i)
					if !tc.enums {
						off = uint64(tc.keys-i) * 3 // unsorted offsets
					}
					if err := rs.AddKey(k[:], off); err != nil {
						t.Fatal(err)
					}
				}
				if err := rs.Build(context.Background()); err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(idx)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			a, b := build(false), build(true)
			if !bytes.Equal(a, b) {
				t.Fatalf("index differs: etl %d bytes, in-memory %d bytes", len(a), len(b))
			}
			// And it answers.
			dir := t.TempDir()
			p := filepath.Join(dir, "y.idx")
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
			ix := MustOpen(p)
			defer ix.Close()
			if ix.KeyCount() != uint64(tc.keys) {
				t.Fatalf("key count %d", ix.KeyCount())
			}
		})
	}
}
