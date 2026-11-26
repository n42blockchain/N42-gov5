// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package syncwire

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/lib/qmdb"
)

// buildTree appends n keys, deletes every third and rewrites every fifth, so
// the tree has live slots, dead slots and (with enough deletions) whole dead
// twigs.
func buildTree(t testing.TB, n int, hollow bool) *qmdb.Tree {
	t.Helper()
	tree := qmdb.New()
	key := func(i int) qmdb.Hash { return qmdb.Hash(KeyHash([]byte(fmt.Sprintf("k%d", i)))) }
	for i := 0; i < n; i++ {
		tree.Set(key(i), []byte(fmt.Sprintf("value-%d", i)))
	}
	for i := 0; i < n; i += 3 {
		tree.Delete(key(i))
	}
	for i := 1; i < n; i += 5 {
		tree.Set(key(i), []byte(fmt.Sprintf("rewritten-%d", i)))
	}
	if hollow {
		// Kill every key of the first twig so it becomes a kind-3 twig.
		for i := 0; i < n && i < TwigSize; i++ {
			tree.Delete(key(i))
		}
	}
	return tree
}

func portableOf(t testing.TB, tree *qmdb.Tree) []byte {
	t.Helper()
	var buf bytes.Buffer
	md := qmdb.PortableSnapshotMetadata{ChainID: 1144, BlockNumber: 7, Root: tree.Root(), NextSlot: tree.NextSlot()}
	md.GenesisHash[0], md.BlockHash[0] = 0xaa, 0xbb
	if _, err := qmdb.WritePortableSnapshotTombstone(&buf, md, tree, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestQMDBHashingMatchesLibQMDB pins the restated hashing against lib/qmdb:
// the twigs parsed from a real tombstone export fold to the tree root, and
// reassembling them reproduces the export byte for byte.
func TestQMDBHashingMatchesLibQMDB(t *testing.T) {
	for _, tc := range []struct {
		n      int
		hollow bool
	}{{1, false}, {100, false}, {TwigSize, false}, {TwigSize + 3, false}, {3*TwigSize - 1, false}, {5*TwigSize + 7, true}} {
		tree := buildTree(t, tc.n, tc.hollow)
		enc := portableOf(t, tree)
		h, twigs, err := TwigsFromPortable(enc)
		if err != nil {
			t.Fatalf("n=%d: %v", tc.n, err)
		}
		if h.Root != Hash(tree.Root()) {
			t.Fatalf("n=%d: root", tc.n)
		}
		again, err := AssemblePortable(h, twigs)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, enc) {
			t.Fatalf("n=%d: reassembled snapshot differs", tc.n)
		}
		if _, _, err := qmdb.ImportPortableSnapshot(again, h.ChainID, qmdb.Hash(h.GenesisHash)); err != nil {
			t.Fatal(err)
		}
		if tc.hollow && twigs[0].Records[0].Kind != KindHollow {
			t.Fatalf("expected a hollow first twig")
		}
		for _, tw := range twigs {
			c := TwigChunk{TwigID: tw.ID, Bits: tw.Bits, Records: tw.Records}
			raw, err := c.Encode()
			if err != nil {
				t.Fatal(err)
			}
			dec, err := DecodeTwigChunk(raw)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyTwig(dec, tw.Roots, h.NextSlot); err != nil {
				t.Fatalf("twig %d: %v", tw.ID, err)
			}
		}
	}
}

func TestRangeProofAllRanges(t *testing.T) {
	for _, total := range []int{1, 2, 3, 5, 8, 2048 + 3} {
		roots := make([]Hash, total)
		for i := range roots {
			roots[i] = KeyHash([]byte{byte(i), byte(i >> 8)})
		}
		root := UpperRoot(roots)
		step := 1
		if total > 64 {
			step = 97
		}
		for first := 0; first < total; first += step {
			for n := 1; first+n <= total; n += step {
				proof, err := RangeProof(roots, uint64(first), uint64(n))
				if err != nil {
					t.Fatal(err)
				}
				if err := VerifyRange(root, uint64(total), uint64(first), roots[first:first+n], proof); err != nil {
					t.Fatalf("total %d [%d,+%d): %v", total, first, n, err)
				}
				// A wrong leaf, a missing or an extra proof node fails.
				bad := append([]Hash(nil), roots[first:first+n]...)
				bad[0][0] ^= 1
				if VerifyRange(root, uint64(total), uint64(first), bad, proof) == nil {
					t.Fatal("wrong leaf verified")
				}
				if len(proof) > 0 && VerifyRange(root, uint64(total), uint64(first), roots[first:first+n], proof[:len(proof)-1]) == nil {
					t.Fatal("short proof verified")
				}
				if VerifyRange(root, uint64(total), uint64(first), roots[first:first+n], append(append([]Hash(nil), proof...), Hash{})) == nil {
					t.Fatal("long proof verified")
				}
			}
		}
	}
}

func TestFixedRequestsRefuseBadInput(t *testing.T) {
	ok := RangeRequest{Start: 5, Count: 3}.Encode()
	if _, err := DecodeRangeRequest(ok); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRangeRequest(ok[:31]); !errors.Is(err, ErrLength) {
		t.Fatal("short range request accepted")
	}
	for _, r := range []RangeRequest{{Start: 1, Count: 0}, {Start: 1, Count: 1025}, {Start: 1, Count: 1, Flags: 2}, {Start: ^uint64(0), Count: 2}} {
		if _, err := DecodeRangeRequest(r.Encode()); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
	if _, err := DecodeManifestRequest(ManifestRequest{Count: 1, Reserved: 1}.Encode()); err == nil {
		t.Fatal("reserved manifest field accepted")
	}
	if _, err := DecodeManifestRequest(ManifestRequest{Count: MaxManifestTwigs + 1}.Encode()); err == nil {
		t.Fatal("oversized manifest accepted")
	}
	if _, err := DecodeTwigsRequest(TwigsRequest{TwigCount: 257}.Encode()); err == nil {
		t.Fatal("oversized twig request accepted")
	}
	if _, err := DecodeTwigsRequest(TwigsRequest{TwigCount: 1, Flags: 4}.Encode()); err == nil {
		t.Fatal("reserved twig flag accepted")
	}
	if _, err := DecodeCodeRequest([]byte{0, 0}); err == nil {
		t.Fatal("empty code request accepted")
	}
	if _, err := DecodeCodeRequest(binary.LittleEndian.AppendUint16(nil, 2)); err == nil {
		t.Fatal("truncated code request accepted")
	}
}

func TestRangeEndRules(t *testing.T) {
	req := RangeRequest{Start: 10, Count: 4}
	for _, tc := range []struct {
		e      RangeEnd
		chunks uint64
		bytes  uint64
		ok     bool
	}{
		{RangeEnd{Next: 14, ServedBytes: 40, Reason: ReasonComplete}, 4, 40, true},
		{RangeEnd{Next: 12, ServedBytes: 20, Reason: ReasonByteBudget}, 2, 20, true},
		{RangeEnd{Next: 10, Reason: ReasonCanonicalEnd}, 0, 0, true},
		{RangeEnd{Next: 10, Reason: ReasonByteBudget}, 0, 0, false},                   // zero blocks with reason 1
		{RangeEnd{Next: 13, ServedBytes: 20, Reason: ReasonByteBudget}, 2, 20, false}, // next lies
		{RangeEnd{Next: 12, ServedBytes: 21, Reason: ReasonByteBudget}, 2, 20, false}, // bytes lie
		{RangeEnd{Next: 12, ServedBytes: 20, Reason: ReasonComplete}, 2, 20, false},   // complete but short
		{RangeEnd{Next: 14, ServedBytes: 40, Reason: ReasonCountCap}, 4, 40, false},   // full but not complete
	} {
		err := CheckRangeEnd(req, tc.e, tc.chunks, tc.bytes)
		if (err == nil) != tc.ok {
			t.Fatalf("%+v chunks %d: err %v", tc.e, tc.chunks, err)
		}
	}
}

func TestFramingRefusesOversizeBeforeAllocating(t *testing.T) {
	wire := binary.AppendUvarint(nil, 1<<40)
	if _, err := ReadFramed(bytes.NewReader(wire), 1<<20); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
	payload := []byte("hello")
	got, err := ReadFramed(bytes.NewReader(AppendFramed(nil, payload)), 16)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("round trip: %v %q", err, got)
	}
}
