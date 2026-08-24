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
	ahead.EnableLookahead()
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

// A caller that seeks backwards invalidates the slot; the reader must fall back
// to an inline decode rather than serve the wrong segment.
func TestBodyLookaheadSeekFallsBack(t *testing.T) {
	dir := t.TempDir()
	const blocks = HeaderSegmentSize*3 + 5
	writeSyntheticBodySegments(t, dir, blocks)

	r, err := OpenBodyCompact(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	r.EnableLookahead()
	defer r.Close()

	for _, n := range []uint64{0, HeaderSegmentSize * 2, 3, HeaderSegmentSize, HeaderSegmentSize * 2} {
		b, err := r.ReadBody(n)
		if err != nil {
			t.Fatalf("block %d: %v", n, err)
		}
		if got := r.cachedSeg; got != int64(n/HeaderSegmentSize) {
			t.Fatalf("block %d: cached segment %d, want %d", n, got, n/HeaderSegmentSize)
		}
		if len(b.Txs) == 0 {
			t.Fatalf("block %d: no txs decoded", n)
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
