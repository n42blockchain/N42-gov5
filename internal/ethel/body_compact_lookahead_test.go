package ethel

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// Read-ahead must be invisible: the same blocks come back, in the same order,
// with the same contents, whether or not the next segment is decoded early.
func TestBodyLookaheadMatchesInline(t *testing.T) {
	dir := t.TempDir()
	const blocks = HeaderSegmentSize*3 + 17
	writeSyntheticBodySegments(t, dir, blocks)

	plain, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open plain: %v", err)
	}
	defer plain.Close()

	ahead, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open ahead: %v", err)
	}
	defer ahead.Close()

	for n := uint64(0); n < blocks; n++ {
		want, werr := plain.ReadBody(n)
		got, gerr := ahead.TakeBody(n)
		if (werr == nil) != (gerr == nil) {
			t.Fatalf("block %d: err mismatch: plain=%v ahead=%v", n, werr, gerr)
		}
		if werr != nil {
			continue
		}
		if len(got.Txs) != len(want.Txs) {
			t.Fatalf("block %d: %d txs, want %d", n, len(got.Txs), len(want.Txs))
		}
		for i := range want.Txs {
			if got.Txs[i].Hash() != want.Txs[i].Hash() {
				t.Fatalf("block %d tx %d: hash mismatch", n, i)
			}
		}
		// The point of the exercise: while any later segment remains, the
		// successor must already be in flight rather than decoded on demand.
		wantAhead := int64(n/HeaderSegmentSize) + 1
		if uint64(wantAhead) < ahead.Segments() {
			if ahead.pending == nil {
				t.Fatalf("block %d: no read-ahead in flight", n)
			}
			if ahead.pending.seg != wantAhead {
				t.Fatalf("block %d: read-ahead holds segment %d, want %d",
					n, ahead.pending.seg, wantAhead)
			}
		}
	}
}

// A seek leaves a read-ahead slot holding the wrong segment. The reader must
// fall back to an inline decode AND recover: an implementation that simply
// refuses to start another prefetch while one is outstanding stays disabled for
// the rest of the run and pins the orphaned segment in memory forever.
func TestBodyLookaheadRecoversFromSeek(t *testing.T) {
	dir := t.TempDir()
	const blocks = HeaderSegmentSize*4 + 5
	writeSyntheticBodySegments(t, dir, blocks)

	r, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	// Sequential use arms read-ahead for segment 1.
	if _, err := r.TakeBody(0); err != nil {
		t.Fatalf("take block 0: %v", err)
	}
	if r.pending == nil || r.pending.seg != 1 {
		t.Fatalf("expected read-ahead on segment 1, got %v", r.pending)
	}

	// Seek past it. The slot now holds a segment nobody asked for.
	const seekBlock = HeaderSegmentSize * 3
	b, err := r.ReadBody(seekBlock)
	if err != nil {
		t.Fatalf("seek read: %v", err)
	}
	if len(b.Txs) == 0 {
		t.Fatal("seek read returned no txs")
	}
	if r.cachedSeg != 3 {
		t.Fatalf("cached segment %d after seek, want 3", r.cachedSeg)
	}

	// Sequential use resumes: the stale slot must be reclaimed, not honoured.
	if _, err := r.TakeBody(seekBlock); err != nil {
		t.Fatalf("take after seek: %v", err)
	}
	if r.pending == nil {
		t.Fatal("read-ahead did not restart after a seek")
	}
	if r.pending.seg != 4 {
		t.Fatalf("read-ahead holds segment %d after seek, want 4", r.pending.seg)
	}

	// And the data is still right on both sides of the seek.
	for _, n := range []uint64{HeaderSegmentSize, HeaderSegmentSize*4 + 1} {
		if _, err := r.ReadBody(n); err != nil {
			t.Fatalf("block %d after seek: %v", n, err)
		}
	}
}

// writeSyntheticBodySegments builds a small on-disk bodyc store covering
// [0, blocks) using the production framing: an 8-byte cidx entry per segment
// pointing at a length-prefixed zstd frame in bodyc.0000.cdat.
func writeSyntheticBodySegments(t *testing.T, dir string, blocks uint64) {
	t.Helper()

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	defer enc.Close()

	datPath := filepath.Join(dir, "bodyc.0000.cdat")
	dat, err := os.Create(datPath)
	if err != nil {
		t.Fatalf("create cdat: %v", err)
	}
	defer dat.Close()

	to := types.Address{0x22}
	var (
		idx    []byte
		offset uint64
	)
	for start := uint64(0); start < blocks; start += HeaderSegmentSize {
		n := blocks - start
		if n > HeaderSegmentSize {
			n = HeaderSegmentSize
		}
		seg := make([]*DecodedBlock, n)
		for i := range seg {
			// One tx per block, with a nonce unique across the whole store so a
			// mis-served segment cannot pass as the right one.
			seg[i] = &DecodedBlock{Txs: []*transaction.Transaction{
				transaction.NewTransaction(start+uint64(i), types.Address{0x11},
					&to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil),
			}}
		}
		frame := encodeBodySegment(seg, 1, enc)

		var sizeBuf [4]byte
		binary.LittleEndian.PutUint32(sizeBuf[:], uint32(len(frame)))
		if _, err := dat.Write(sizeBuf[:]); err != nil {
			t.Fatalf("write size: %v", err)
		}
		if _, err := dat.Write(frame); err != nil {
			t.Fatalf("write frame: %v", err)
		}
		idx = append(idx, encodeBodyIdx(bodyIdxEntry{fileNum: 0, offset: offset})...)
		offset += 4 + uint64(len(frame))
	}
	if err := os.WriteFile(filepath.Join(dir, "bodyc.cidx"), idx, 0o644); err != nil {
		t.Fatalf("write cidx: %v", err)
	}
}

// The authorization-V repair must be gated on the segment's declared format,
// not on whether some authorization happens to carry V == 0. y_parity 0 is an
// ordinary value, so a value-based gate fires constantly on a lossless archive
// and pays a transaction-root derivation per block for nothing.
func TestSegmentNeedsAuthVRepairKeysOnFormat(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags bodyFlags
		want  bool
	}{
		{"current segment with SetCode txs", bfHasSetCode | bfAuthVFull | bfPostMerge, false},
		{"legacy segment with SetCode txs", bfHasSetCode | bfPostMerge, true},
		{"segment with no SetCode txs", bfPostMerge | bfHasAccessList, false},
		{"empty flags", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &BodyCompactReader{cachedFlags: tc.flags}
			if got := r.SegmentNeedsAuthVRepair(); got != tc.want {
				t.Fatalf("SegmentNeedsAuthVRepair() = %v, want %v", got, tc.want)
			}
		})
	}
}

// A segment written by the current encoder must declare the lossless
// authorization encoding, so the gate above can rely on it.
func TestCurrentEncoderDeclaresAuthVFull(t *testing.T) {
	blocks := makeTestBlocks()
	flags := detectBodyFlags(blocks)
	if flags&bfHasSetCode == 0 {
		t.Fatal("fixture no longer contains a SetCode transaction")
	}
	if flags&bfAuthVFull == 0 {
		t.Fatal("current encoder must declare bfAuthVFull alongside bfHasSetCode")
	}
}
