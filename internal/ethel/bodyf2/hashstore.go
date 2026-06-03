package bodyf2

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

// HashStore is the OPTIONAL per-block canonical tx-hash sidecar that F2 itself
// omits. It exists only to serve the hash-bearing parts of the RPC surface that
// F2's ledger view cannot reproduce: getBlockByNumber(fullTx=false) returns the
// block's tx-hash list, and fullTx=true listings carry each tx's hash. Hashes
// are random 32 bytes, so this costs ~32 B/tx — a node keeps it only if it must
// serve wire-faithful hash lists; pure-ledger F2 nodes omit it. Source: the
// canonical hash computed during F2 conversion (or reth TransactionHashNumbers).
//
// On-disk (mirrors the F2 store): f2.txhashes.cidx (8B/seg) + f2.txhashes.NNNN.cdat
// (per-seg [4B compSize][zstd]); segment payload = varint(nblocks) then per block
// varint(ntx) + ntx×32B hashes.

var ErrHashAbsent = errors.New("bodyf2: tx-hash segment absent")

// ---- HashWriter ----

type HashWriter struct {
	dir     string
	enc     *zstd.Encoder
	entries map[uint64][8]byte
	maxSeg  uint64
	cur     *os.File
	curBuf  *bufio.Writer
	fileNum uint16
	offset  uint32
}

func NewHashWriter(dir string) (*HashWriter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	w := &HashWriter{dir: dir, enc: enc, entries: map[uint64][8]byte{}}
	if err := w.openData(0); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *HashWriter) openData(num uint16) error {
	f, err := os.Create(filepath.Join(w.dir, fmt.Sprintf("f2.txhashes.%04d.cdat", num)))
	if err != nil {
		return err
	}
	w.cur, w.curBuf, w.fileNum, w.offset = f, bufio.NewWriterSize(f, 1<<20), num, 0
	return nil
}

// AppendSegment writes one segment's per-block hash lists (blocks[i] = hashes of
// block i's txs in order).
func (w *HashWriter) AppendSegment(segNum uint64, blocks [][][32]byte) error {
	var raw []byte
	raw = appendUvarint(raw, uint64(len(blocks)))
	for _, hs := range blocks {
		raw = appendUvarint(raw, uint64(len(hs)))
		for i := range hs {
			raw = append(raw, hs[i][:]...)
		}
	}
	comp := w.enc.EncodeAll(raw, nil)
	if int64(w.offset)+int64(len(comp))+4 > f2MaxFileSize {
		if err := w.curBuf.Flush(); err != nil {
			return err
		}
		if err := w.cur.Close(); err != nil {
			return err
		}
		if err := w.openData(w.fileNum + 1); err != nil {
			return err
		}
	}
	var entry [8]byte
	binary.LittleEndian.PutUint16(entry[0:2], w.fileNum)
	binary.LittleEndian.PutUint32(entry[4:8], w.offset)
	w.entries[segNum] = entry
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(comp)))
	if _, err := w.curBuf.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := w.curBuf.Write(comp); err != nil {
		return err
	}
	w.offset += uint32(4 + len(comp))
	if segNum > w.maxSeg {
		w.maxSeg = segNum
	}
	return nil
}

func (w *HashWriter) Close() error {
	if err := w.curBuf.Flush(); err != nil {
		return err
	}
	if err := w.cur.Sync(); err != nil {
		return err
	}
	if err := w.cur.Close(); err != nil {
		return err
	}
	idx, err := os.Create(filepath.Join(w.dir, "f2.txhashes.cidx"))
	if err != nil {
		return err
	}
	buf := bufio.NewWriterSize(idx, 1<<20)
	var absent [8]byte
	binary.LittleEndian.PutUint16(absent[0:2], absentFileNum)
	for s := uint64(0); s <= w.maxSeg; s++ {
		e, ok := w.entries[s]
		if !ok {
			e = absent
		}
		if _, err := buf.Write(e[:]); err != nil {
			idx.Close()
			return err
		}
	}
	if err := buf.Flush(); err != nil {
		idx.Close()
		return err
	}
	if err := idx.Sync(); err != nil {
		idx.Close()
		return err
	}
	return idx.Close()
}

// ---- HashReader ----

type HashReader struct {
	dir          string
	idx          *os.File
	dec          *zstd.Decoder
	segments     uint64
	dataFiles    map[uint16]*os.File
	cachedSeg    int64
	cachedBlocks [][][32]byte
}

func OpenHashReader(dir string) (*HashReader, error) {
	idx, err := os.Open(filepath.Join(dir, "f2.txhashes.cidx"))
	if err != nil {
		return nil, err
	}
	fi, err := idx.Stat()
	if err != nil {
		idx.Close()
		return nil, err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		idx.Close()
		return nil, err
	}
	return &HashReader{dir: dir, idx: idx, dec: dec, segments: uint64(fi.Size()) / 8, dataFiles: map[uint16]*os.File{}, cachedSeg: -1}, nil
}

func (r *HashReader) Close() {
	r.dec.Close()
	r.idx.Close()
	for _, f := range r.dataFiles {
		f.Close()
	}
}

// BlockHashes returns the canonical tx hashes for blockNum's txs in order.
func (r *HashReader) BlockHashes(blockNum uint64) ([][32]byte, error) {
	seg := int64(blockNum / SegSize)
	idx := int(blockNum % SegSize)
	if seg != r.cachedSeg {
		if err := r.loadSegment(seg); err != nil {
			return nil, err
		}
	}
	if idx >= len(r.cachedBlocks) {
		return nil, fmt.Errorf("bodyf2: block %d hash index out of range", blockNum)
	}
	return r.cachedBlocks[idx], nil
}

func (r *HashReader) loadSegment(seg int64) error {
	if uint64(seg) >= r.segments {
		return fmt.Errorf("bodyf2: hash segment %d out of range", seg)
	}
	var entry [8]byte
	if _, err := r.idx.ReadAt(entry[:], seg*8); err != nil {
		return err
	}
	fileNum := binary.LittleEndian.Uint16(entry[0:2])
	if fileNum == absentFileNum {
		return fmt.Errorf("%w: segment %d", ErrHashAbsent, seg)
	}
	offset := binary.LittleEndian.Uint32(entry[4:8])
	df, ok := r.dataFiles[fileNum]
	if !ok {
		f, err := os.Open(filepath.Join(r.dir, fmt.Sprintf("f2.txhashes.%04d.cdat", fileNum)))
		if err != nil {
			return err
		}
		r.dataFiles[fileNum] = f
		df = f
	}
	var sizeBuf [4]byte
	if _, err := df.ReadAt(sizeBuf[:], int64(offset)); err != nil {
		return err
	}
	comp := make([]byte, binary.LittleEndian.Uint32(sizeBuf[:]))
	if _, err := df.ReadAt(comp, int64(offset)+4); err != nil {
		return err
	}
	raw, err := r.dec.DecodeAll(comp, nil)
	if err != nil {
		return err
	}
	rd := &reader{b: raw}
	nb, err := rd.uvarint()
	if err != nil {
		return err
	}
	blocks := make([][][32]byte, nb)
	for bi := uint64(0); bi < nb; bi++ {
		ntx, err := rd.uvarint()
		if err != nil {
			return err
		}
		hs := make([][32]byte, ntx)
		for ti := uint64(0); ti < ntx; ti++ {
			hb, err := rd.bytes(32)
			if err != nil {
				return err
			}
			copy(hs[ti][:], hb)
		}
		blocks[bi] = hs
	}
	r.cachedSeg = seg
	r.cachedBlocks = blocks
	return nil
}
