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

// SegSize is blocks per F2 segment (matches bodyc / headerc).
const SegSize = 8192

const f2MaxFileSize = 2_000_000_000 // 2 GB per cdat, matches bodyc

// absentFileNum marks a segment that was never written (gap / trimmed). Real
// cdat file numbers start at 0; 0xFFFF is the sentinel.
const absentFileNum = 0xFFFF

// ErrF2Absent is returned by Reader.ReadBlock when the block's segment was not
// written to this store (a partial/cold-offloaded F2 store).
var ErrF2Absent = errors.New("bodyf2: segment absent from store")

// On-disk layout (mirrors bodyc):
//
//	f2.cidx          8 bytes/segment: fileNum u16 LE [0:2], offset u32 LE [4:8]
//	f2.NNNN.cdat     per segment: [4B compSize LE][zstd(EncodeSegment bytes)]
//	f2.addr.dict     the AddrDict sidecar (Save/LoadAddrDict)

// ---- Writer ----

// Writer builds an F2 store. Segments are appended by absolute segment number
// (gaps allowed — they get the absent sentinel). The shared AddrDict grows as
// segments are encoded and is persisted on Close.
type Writer struct {
	dir     string
	dict    *AddrDict
	enc     *zstd.Encoder
	entries map[uint64][8]byte // segNum -> cidx entry
	maxSeg  uint64
	cur     *os.File
	curBuf  *bufio.Writer
	fileNum uint16
	offset  uint32
}

func NewWriter(dir string, dict *AddrDict) (*Writer, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, dict: dict, enc: enc, entries: map[uint64][8]byte{}}
	if err := w.openData(0); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Writer) openData(num uint16) error {
	f, err := os.Create(filepath.Join(w.dir, fmt.Sprintf("f2.%04d.cdat", num)))
	if err != nil {
		return err
	}
	w.cur, w.curBuf, w.fileNum, w.offset = f, bufio.NewWriterSize(f, 1<<20), num, 0
	return nil
}

// AppendSegment encodes + compresses one segment's blocks and records its index.
func (w *Writer) AppendSegment(segNum uint64, blocks []F2Block) error {
	raw := EncodeSegment(blocks, w.dict)
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

// Close flushes the data file, writes the dense cidx (absent sentinel for gaps)
// and the addr-dict sidecar.
func (w *Writer) Close() error {
	if err := w.curBuf.Flush(); err != nil {
		return err
	}
	if err := w.cur.Sync(); err != nil {
		return err
	}
	if err := w.cur.Close(); err != nil {
		return err
	}
	idx, err := os.Create(filepath.Join(w.dir, "f2.cidx"))
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
	if err := idx.Close(); err != nil {
		return err
	}
	return w.dict.Save(filepath.Join(w.dir, "f2.addr.dict"))
}

// ---- Reader ----

// Reader provides random/sequential access to an F2 store (cidx + cdat + dict),
// caching the current segment. ReadBlock returns the decoded F2Block.
type Reader struct {
	dir       string
	idx       *os.File
	dict      *AddrDict
	dec       *zstd.Decoder
	segments  uint64
	dataFiles map[uint16]*os.File

	cachedSeg    int64
	cachedBlocks []F2Block
}

func OpenReader(dir string) (*Reader, error) {
	idx, err := os.Open(filepath.Join(dir, "f2.cidx"))
	if err != nil {
		return nil, fmt.Errorf("open f2.cidx: %w", err)
	}
	fi, err := idx.Stat()
	if err != nil {
		idx.Close()
		return nil, err
	}
	dict, err := LoadAddrDict(filepath.Join(dir, "f2.addr.dict"))
	if err != nil {
		idx.Close()
		return nil, fmt.Errorf("load addr dict: %w", err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		idx.Close()
		return nil, err
	}
	return &Reader{
		dir: dir, idx: idx, dict: dict, dec: dec,
		segments:  uint64(fi.Size()) / 8,
		dataFiles: map[uint16]*os.File{},
		cachedSeg: -1,
	}, nil
}

func (r *Reader) Close() {
	r.dec.Close()
	r.idx.Close()
	for _, f := range r.dataFiles {
		f.Close()
	}
}

func (r *Reader) Segments() uint64 { return r.segments }
func (r *Reader) MaxBlock() uint64 { return r.segments * SegSize }

// ReadBlock returns the F2Block for blockNum (ErrF2Absent if its segment was
// not written to this store).
func (r *Reader) ReadBlock(blockNum uint64) (*F2Block, error) {
	seg := int64(blockNum / SegSize)
	idx := int(blockNum % SegSize)
	if seg != r.cachedSeg {
		if err := r.loadSegment(seg); err != nil {
			return nil, err
		}
	}
	if idx >= len(r.cachedBlocks) {
		return nil, fmt.Errorf("bodyf2: block %d: index %d out of range (%d)", blockNum, idx, len(r.cachedBlocks))
	}
	return &r.cachedBlocks[idx], nil
}

func (r *Reader) loadSegment(seg int64) error {
	if uint64(seg) >= r.segments {
		return fmt.Errorf("bodyf2: segment %d out of range (%d)", seg, r.segments)
	}
	var entry [8]byte
	if _, err := r.idx.ReadAt(entry[:], seg*8); err != nil {
		return fmt.Errorf("read cidx: %w", err)
	}
	fileNum := binary.LittleEndian.Uint16(entry[0:2])
	if fileNum == absentFileNum {
		return fmt.Errorf("%w: segment %d (block ~%d)", ErrF2Absent, seg, uint64(seg)*SegSize)
	}
	offset := binary.LittleEndian.Uint32(entry[4:8])

	df, ok := r.dataFiles[fileNum]
	if !ok {
		f, err := os.Open(filepath.Join(r.dir, fmt.Sprintf("f2.%04d.cdat", fileNum)))
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("%w: segment %d cdat %04d", ErrF2Absent, seg, fileNum)
			}
			return err
		}
		r.dataFiles[fileNum] = f
		df = f
	}
	var sizeBuf [4]byte
	if _, err := df.ReadAt(sizeBuf[:], int64(offset)); err != nil {
		return fmt.Errorf("read size: %w", err)
	}
	comp := make([]byte, binary.LittleEndian.Uint32(sizeBuf[:]))
	if _, err := df.ReadAt(comp, int64(offset)+4); err != nil {
		return fmt.Errorf("read data: %w", err)
	}
	raw, err := r.dec.DecodeAll(comp, nil)
	if err != nil {
		return fmt.Errorf("decompress: %w", err)
	}
	blocks, err := DecodeSegment(raw, r.dict)
	if err != nil {
		return fmt.Errorf("decode segment %d: %w", seg, err)
	}
	r.cachedSeg = seg
	r.cachedBlocks = blocks
	return nil
}
