// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package v11

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// testSnapshot builds a tree of n appended keys (every third deleted; with
// hollow, the whole first twig dead) and its snapshot at pivot 7.
func testSnapshot(t testing.TB, n int, hollow bool) *Snapshot {
	t.Helper()
	tree := qmdb.New()
	key := func(i int) qmdb.Hash { return qmdb.Hash(syncwire.KeyHash([]byte(fmt.Sprintf("k%d", i)))) }
	for i := 0; i < n; i++ {
		tree.Set(key(i), []byte(fmt.Sprintf("v%d", i)))
	}
	for i := 0; i < n; i += 3 {
		tree.Delete(key(i))
	}
	if hollow {
		for i := 0; i < syncwire.TwigSize && i < n; i++ {
			tree.Delete(key(i))
		}
	}
	var buf bytes.Buffer
	md := qmdb.PortableSnapshotMetadata{ChainID: 1144, BlockNumber: 7, BlockHash: qmdb.Hash{7}, Root: tree.Root(), NextSlot: tree.NextSlot()}
	if _, err := qmdb.WritePortableSnapshotTombstone(&buf, md, tree, nil); err != nil {
		t.Fatal(err)
	}
	h, twigs, err := syncwire.TwigsFromPortable(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return NewSnapshot(h.BlockNumber, types.Hash(h.BlockHash), types.Hash(h.Root), h.NextSlot, twigs)
}

func readManifest(t *testing.T, snap *Snapshot, req syncwire.ManifestRequest) (syncwire.ManifestPage, error) {
	t.Helper()
	payload, code, msg := ServeManifest(snap, req)
	if code != syncwire.CodeSuccess {
		return syncwire.ManifestPage{}, &syncwire.CodeError{Code: code, Msg: msg}
	}
	return syncwire.DecodeManifestPage(payload)
}

func TestManifestPagesVerifyAtTheEdges(t *testing.T) {
	for _, n := range []int{1, syncwire.TwigSize + 1, 2*syncwire.TwigSize + 5, 4*syncwire.TwigSize + 9} {
		snap := testSnapshot(t, n, false)
		total := snap.TwigCount()
		for first := uint64(0); first < total; first++ {
			for count := uint32(1); uint64(count) <= total-first+1; count++ {
				page, err := readManifest(t, snap, syncwire.ManifestRequest{PivotHash: snap.PivotHash, FirstTwig: first, Count: count})
				if err != nil {
					t.Fatal(err)
				}
				if err := syncwire.VerifyManifestPage(page, snap.Root); err != nil {
					t.Fatalf("n=%d first=%d count=%d: %v", n, first, count, err)
				}
				// The wrong root (a pivot that is not the anchored one) fails.
				if syncwire.VerifyManifestPage(page, types.Hash{1}) == nil {
					t.Fatal("page verified against a wrong root")
				}
			}
		}
		if _, err := readManifest(t, snap, syncwire.ManifestRequest{PivotHash: snap.PivotHash, FirstTwig: total, Count: 1}); err == nil {
			t.Fatal("out-of-range manifest page served")
		}
	}
	if _, err := readManifest(t, nil, syncwire.ManifestRequest{Count: 1}); err == nil {
		t.Fatal("manifest for an unknown pivot served")
	}
}

func readTwigs(t *testing.T, snap *Snapshot, req syncwire.TwigsRequest) ([]syncwire.TwigChunk, *syncwire.TwigsEnd, error) {
	t.Helper()
	var buf bytes.Buffer
	ServeTwigs(snap, req, func(b []byte) bool { buf.Write(b); return true })
	var chunks []syncwire.TwigChunk
	for {
		c, err := syncwire.ReadChunk(&buf, false, syncwire.MaxTwigChunkPayload)
		if errors.Is(err, io.EOF) {
			return chunks, nil, nil
		}
		if err != nil {
			return chunks, nil, err
		}
		switch c.Code {
		case syncwire.CodeSuccess:
			tc, err := syncwire.DecodeTwigChunk(c.Payload)
			if err != nil {
				return chunks, nil, err
			}
			chunks = append(chunks, tc)
		case syncwire.CodeEnd:
			e, err := syncwire.DecodeTwigsEnd(c.Payload)
			return chunks, &e, err
		default:
			return chunks, nil, &syncwire.CodeError{Code: c.Code, Msg: string(c.Payload)}
		}
	}
}

func TestTwigsServeVerify(t *testing.T) {
	snap := testSnapshot(t, 3*syncwire.TwigSize+17, true)
	chunks, end, err := readTwigs(t, snap, syncwire.TwigsRequest{PivotHash: snap.PivotHash, TwigCount: 256})
	if err != nil || end == nil {
		t.Fatal(err)
	}
	if len(chunks) != 4 || end.Reason != syncwire.ReasonNotAvailable || end.NextTwig != 4 || end.NextSlot != snap.NextSlot {
		t.Fatalf("%d chunks, END %+v", len(chunks), end)
	}
	if chunks[0].Records[0].Kind != syncwire.KindHollow {
		t.Fatal("first twig is not hollow")
	}
	for _, c := range chunks {
		if err := syncwire.VerifyTwig(c, snap.Twigs[c.TwigID].Roots, snap.NextSlot); err != nil {
			t.Fatal(err)
		}
	}
	// Byte budget: one twig per reply when the budget is below two.
	chunks, end, _ = readTwigs(t, snap, syncwire.TwigsRequest{PivotHash: snap.PivotHash, FirstTwig: 1, TwigCount: 3, MaxBytes: 1})
	if len(chunks) != 1 || end.Reason != syncwire.ReasonByteBudget || end.NextTwig != 2 {
		t.Fatalf("budget: %d chunks END %+v", len(chunks), end)
	}
	// BITS_ONLY: bits without records.
	chunks, end, _ = readTwigs(t, snap, syncwire.TwigsRequest{PivotHash: snap.PivotHash, FirstTwig: 1, TwigCount: 2, Flags: syncwire.FlagBitsOnly})
	if len(chunks) != 2 || len(chunks[0].Records) != 0 || end.Reason != syncwire.ReasonComplete {
		t.Fatalf("bits only: %d chunks END %+v", len(chunks), end)
	}
	// Out of range and unknown pivot.
	if _, _, err := readTwigs(t, snap, syncwire.TwigsRequest{PivotHash: snap.PivotHash, FirstTwig: 4, TwigCount: 1}); err == nil {
		t.Fatal("twig past the end served")
	}
	if _, _, err := readTwigs(t, nil, syncwire.TwigsRequest{TwigCount: 1}); err == nil {
		t.Fatal("twigs of an unknown pivot served")
	}
}

// A twig with a wrong leaf, a flipped tag or forged bits fails verification.
func TestTwigTampering(t *testing.T) {
	snap := testSnapshot(t, syncwire.TwigSize+40, false)
	chunks, _, err := readTwigs(t, snap, syncwire.TwigsRequest{PivotHash: snap.PivotHash, FirstTwig: 1, TwigCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := snap.Twigs[1].Roots
	c := chunks[0]
	var live, dead int = -1, -1
	for i, r := range c.Records {
		if r.Kind == syncwire.KindLive && live < 0 {
			live = i
		}
		if r.Kind == syncwire.KindDead && dead < 0 {
			dead = i
		}
	}
	wrongValue := c
	wrongValue.Records = append([]syncwire.Record(nil), c.Records...)
	wrongValue.Records[live].Value = []byte("forged")
	if syncwire.VerifyTwig(wrongValue, want, snap.NextSlot) == nil {
		t.Fatal("wrong value verified")
	}
	wrongTag := c
	wrongTag.Records = append([]syncwire.Record(nil), c.Records...)
	wrongTag.Records[dead] = syncwire.Record{Kind: syncwire.KindLive, Value: []byte("x")}
	if syncwire.VerifyTwig(wrongTag, want, snap.NextSlot) == nil {
		t.Fatal("dead slot tagged live verified")
	}
	wrongBits := c
	wrongBits.Bits[0] ^= 1
	if syncwire.VerifyTwig(wrongBits, want, snap.NextSlot) == nil {
		t.Fatal("forged bits verified")
	}
}

func TestCodeServe(t *testing.T) {
	code := []byte{0x60, 0x00}
	have := syncwire.Hash{1}
	var buf bytes.Buffer
	ServeCode(func(h syncwire.Hash) []byte {
		if h == have {
			return code
		}
		return nil
	}, []syncwire.Hash{have, {2}}, func(b []byte) bool { buf.Write(b); return true })
	c, err := syncwire.ReadChunk(&buf, false, syncwire.MaxCodeBytes)
	if err != nil || c.Code != 0 || !bytes.Equal(c.Payload, code) {
		t.Fatalf("hit: %v %x", err, c.Payload)
	}
	c, err = syncwire.ReadChunk(&buf, false, syncwire.MaxCodeBytes)
	if err != nil || c.Code != syncwire.CodeResourceUnavailable {
		t.Fatalf("miss: %v %d", err, c.Code)
	}
}
