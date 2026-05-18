package history

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/klauspost/compress/zstd"
)

// Mirror coldstore's file-magic / version style but distinct so a
// reader doesn't accidentally open a coldstore file as history.
const (
	KVMagic   uint32 = 0x484B564B // "HKVK"  (history kv)
	IdxMagic  uint32 = 0x48494458 // "HIDX"
	Version   uint32 = 1
	HeaderLen        = 16
)

type fileHeader struct {
	Magic    uint32
	Version  uint32
	KeyLen   uint16
	PageSize uint16
	Reserved uint32
}

func (h fileHeader) marshal() []byte {
	b := make([]byte, HeaderLen)
	binary.LittleEndian.PutUint32(b[0:4], h.Magic)
	binary.LittleEndian.PutUint32(b[4:8], h.Version)
	binary.LittleEndian.PutUint16(b[8:10], h.KeyLen)
	binary.LittleEndian.PutUint16(b[10:12], h.PageSize)
	binary.LittleEndian.PutUint32(b[12:16], h.Reserved)
	return b
}

func unmarshalHeader(b []byte) (fileHeader, error) {
	if len(b) < HeaderLen {
		return fileHeader{}, fmt.Errorf("history: header truncated")
	}
	return fileHeader{
		Magic:    binary.LittleEndian.Uint32(b[0:4]),
		Version:  binary.LittleEndian.Uint32(b[4:8]),
		KeyLen:   binary.LittleEndian.Uint16(b[8:10]),
		PageSize: binary.LittleEndian.Uint16(b[10:12]),
		Reserved: binary.LittleEndian.Uint32(b[12:16]),
	}, nil
}

// Writer builds a (key → packed-history) coldstore for one domain.
// Keys must be appended in strictly ascending order. Values are the
// already-PackHistory'd timeline bytes (caller responsibility).
type Writer struct {
	keyLen   int
	pageSize int

	kvFile  *os.File
	kvBuf   *bufio.Writer
	idxFile *os.File
	idxBuf  *bufio.Writer
	enc     *zstd.Encoder

	rawPage  []byte
	firstKey []byte
	lastKey  []byte

	pageCount    uint64
	keyCount     uint64
	kvOffset     int64
	totalKvSize  uint64
	totalValSize uint64
}

func NewWriter(baseDir, prefix string, keyLen, pageSize int) (*Writer, error) {
	if keyLen <= 0 || keyLen > 256 {
		return nil, fmt.Errorf("history: bad keyLen %d", keyLen)
	}
	if pageSize <= 0 || pageSize > 65535 {
		return nil, fmt.Errorf("history: bad pageSize %d", pageSize)
	}
	kvPath := baseDir + "/" + prefix + ".kv"
	idxPath := baseDir + "/" + prefix + ".idx"

	kvF, err := os.Create(kvPath)
	if err != nil {
		return nil, fmt.Errorf("create kv: %w", err)
	}
	idxF, err := os.Create(idxPath)
	if err != nil {
		kvF.Close()
		return nil, fmt.Errorf("create idx: %w", err)
	}

	hdr := fileHeader{Magic: KVMagic, Version: Version, KeyLen: uint16(keyLen), PageSize: uint16(pageSize)}
	if _, err := kvF.Write(hdr.marshal()); err != nil {
		kvF.Close()
		idxF.Close()
		return nil, err
	}
	hdr.Magic = IdxMagic
	if _, err := idxF.Write(hdr.marshal()); err != nil {
		kvF.Close()
		idxF.Close()
		return nil, err
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		kvF.Close()
		idxF.Close()
		return nil, err
	}

	return &Writer{
		keyLen:   keyLen,
		pageSize: pageSize,
		kvFile:   kvF,
		kvBuf:    bufio.NewWriterSize(kvF, 1<<20),
		idxFile:  idxF,
		idxBuf:   bufio.NewWriterSize(idxF, 1<<20),
		enc:      enc,
		kvOffset: int64(HeaderLen),
		rawPage:  make([]byte, 0, 65536),
	}, nil
}

// Append adds one (key, packedHistory) pair. Keys must be strictly ascending.
func (w *Writer) Append(key, packedHistory []byte) error {
	if len(key) != w.keyLen {
		return fmt.Errorf("history: key len %d != configured %d", len(key), w.keyLen)
	}
	if w.lastKey != nil {
		if compareKey(key, w.lastKey) <= 0 {
			return fmt.Errorf("history: keys not strictly ascending at #%d (prev %x, curr %x)",
				w.keyCount, w.lastKey, key)
		}
	}
	if len(w.rawPage) == 0 {
		w.firstKey = append(w.firstKey[:0], key...)
	}
	w.rawPage = append(w.rawPage, key...)
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(packedHistory)))
	w.rawPage = append(w.rawPage, lenBuf[:n]...)
	w.rawPage = append(w.rawPage, packedHistory...)

	w.totalValSize += uint64(len(packedHistory))
	w.lastKey = append(w.lastKey[:0], key...)
	w.keyCount++

	if w.keyCount%uint64(w.pageSize) == 0 {
		if err := w.flushPage(); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) flushPage() error {
	if len(w.rawPage) == 0 {
		return nil
	}
	comp := w.enc.EncodeAll(w.rawPage, nil)

	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(comp)))
	if _, err := w.kvBuf.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := w.kvBuf.Write(comp); err != nil {
		return err
	}

	if _, err := w.idxBuf.Write(w.firstKey); err != nil {
		return err
	}
	var offBuf [8]byte
	binary.LittleEndian.PutUint64(offBuf[:], uint64(w.kvOffset))
	if _, err := w.idxBuf.Write(offBuf[:]); err != nil {
		return err
	}

	w.kvOffset += int64(4 + len(comp))
	w.totalKvSize += uint64(4 + len(comp))
	w.pageCount++
	w.rawPage = w.rawPage[:0]
	return nil
}

// Close finalises both files.
func (w *Writer) Close() error {
	if err := w.flushPage(); err != nil {
		return err
	}
	if err := w.kvBuf.Flush(); err != nil {
		return err
	}
	if err := w.idxBuf.Flush(); err != nil {
		return err
	}
	w.enc.Close()
	if err := w.kvFile.Sync(); err != nil {
		return err
	}
	if err := w.idxFile.Sync(); err != nil {
		return err
	}
	if err := w.kvFile.Close(); err != nil {
		return err
	}
	return w.idxFile.Close()
}

// Stats are the counters useful for the build tool's summary.
type Stats struct {
	KeyCount     uint64
	PageCount    uint64
	TotalKvSize  uint64
	TotalValSize uint64
}

func (w *Writer) Stats() Stats {
	return Stats{
		KeyCount:     w.keyCount,
		PageCount:    w.pageCount,
		TotalKvSize:  w.totalKvSize,
		TotalValSize: w.totalValSize,
	}
}

// Reader provides point lookups against a .kv + .idx pair.
type Reader struct {
	keyLen    int
	pageSize  int
	idx       []byte
	idxStride int
	kvFile    *os.File
	dec       *zstd.Decoder
}

func Open(baseDir, prefix string) (*Reader, error) {
	kvPath := baseDir + "/" + prefix + ".kv"
	idxPath := baseDir + "/" + prefix + ".idx"

	idxBytes, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, fmt.Errorf("read idx: %w", err)
	}
	if len(idxBytes) < HeaderLen {
		return nil, fmt.Errorf("idx too short")
	}
	hdr, err := unmarshalHeader(idxBytes[:HeaderLen])
	if err != nil {
		return nil, err
	}
	if hdr.Magic != IdxMagic {
		return nil, fmt.Errorf("idx magic mismatch: 0x%08x", hdr.Magic)
	}
	if hdr.Version != Version {
		return nil, fmt.Errorf("idx version %d != %d", hdr.Version, Version)
	}
	body := idxBytes[HeaderLen:]
	stride := int(hdr.KeyLen) + 8
	if len(body)%stride != 0 {
		return nil, fmt.Errorf("idx body %d not multiple of stride %d", len(body), stride)
	}

	kvF, err := os.Open(kvPath)
	if err != nil {
		return nil, err
	}
	var kvHdrBuf [HeaderLen]byte
	if _, err := io.ReadFull(kvF, kvHdrBuf[:]); err != nil {
		kvF.Close()
		return nil, err
	}
	kvHdr, _ := unmarshalHeader(kvHdrBuf[:])
	if kvHdr.Magic != KVMagic {
		kvF.Close()
		return nil, fmt.Errorf("kv magic mismatch")
	}
	if kvHdr.KeyLen != hdr.KeyLen || kvHdr.PageSize != hdr.PageSize {
		kvF.Close()
		return nil, fmt.Errorf("kv/idx geometry mismatch")
	}

	dec, err := zstd.NewReader(nil)
	if err != nil {
		kvF.Close()
		return nil, err
	}
	return &Reader{
		keyLen: int(hdr.KeyLen), pageSize: int(hdr.PageSize),
		idx: body, idxStride: stride, kvFile: kvF, dec: dec,
	}, nil
}

func (r *Reader) Close() error {
	r.dec.Close()
	return r.kvFile.Close()
}

func (r *Reader) PageCount() int { return len(r.idx) / r.idxStride }

func (r *Reader) KeyLen() int { return r.keyLen }

// Get returns the packed-history blob for key, or (nil, false) if absent.
// The returned slice is a COPY; safe to retain.
func (r *Reader) Get(key []byte) ([]byte, bool, error) {
	if len(key) != r.keyLen {
		return nil, false, fmt.Errorf("history Get: key len %d != %d", len(key), r.keyLen)
	}
	n := r.PageCount()
	if n == 0 {
		return nil, false, nil
	}
	page := sort.Search(n, func(i int) bool {
		fk := r.idx[i*r.idxStride : i*r.idxStride+r.keyLen]
		return compareKey(fk, key) > 0
	})
	if page == 0 {
		return nil, false, nil
	}
	page--

	pageOff := binary.LittleEndian.Uint64(r.idx[page*r.idxStride+r.keyLen : page*r.idxStride+r.keyLen+8])

	var lenBuf [4]byte
	if _, err := r.kvFile.ReadAt(lenBuf[:], int64(pageOff)); err != nil {
		return nil, false, fmt.Errorf("read page len: %w", err)
	}
	compLen := binary.LittleEndian.Uint32(lenBuf[:])
	comp := make([]byte, compLen)
	if _, err := r.kvFile.ReadAt(comp, int64(pageOff)+4); err != nil {
		return nil, false, fmt.Errorf("read page body: %w", err)
	}
	raw, err := r.dec.DecodeAll(comp, nil)
	if err != nil {
		return nil, false, fmt.Errorf("decompress page %d: %w", page, err)
	}

	pos := 0
	for pos < len(raw) {
		if pos+r.keyLen > len(raw) {
			return nil, false, fmt.Errorf("page %d truncated key at pos %d", page, pos)
		}
		k := raw[pos : pos+r.keyLen]
		pos += r.keyLen
		blobLen, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, false, fmt.Errorf("page %d bad blobLen varint at pos %d", page, pos)
		}
		pos += n
		if pos+int(blobLen) > len(raw) {
			return nil, false, fmt.Errorf("page %d truncated blob (need %d, have %d)",
				page, blobLen, len(raw)-pos)
		}
		blob := raw[pos : pos+int(blobLen)]
		cmp := compareKey(k, key)
		if cmp == 0 {
			out := make([]byte, len(blob))
			copy(out, blob)
			return out, true, nil
		}
		if cmp > 0 {
			return nil, false, nil
		}
		pos += int(blobLen)
	}
	return nil, false, nil
}

func compareKey(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return 0
}
