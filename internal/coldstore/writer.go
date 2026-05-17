package coldstore

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/klauspost/compress/zstd"
)

// Writer builds the .kv and .idx file pair for one domain in one step.
// Keys MUST be appended in sorted ascending order (Append asserts this).
type Writer struct {
	keyLen   int
	pageSize int

	kvFile  *os.File
	kvBuf   *bufio.Writer
	idxFile *os.File
	idxBuf  *bufio.Writer
	enc     *zstd.Encoder

	// Page accumulation buffer (cleartext concat of [key][scheme-C value]).
	rawPage []byte
	// Per-page first-key cache so we can flush the index entry at page boundary.
	firstKey []byte
	// Bookkeeping.
	pageCount    uint64
	keyCount     uint64
	kvOffset     int64 // byte offset of NEXT write into kv file
	lastKey      []byte
	totalRawSize uint64
	totalKvSize  uint64
}

// NewWriter creates a Writer that produces baseDir/{prefix}.kv + .idx.
// keyLen e.g. 52 for storage (addr+slot). pageSize=64 is RFC default.
func NewWriter(baseDir, prefix string, keyLen, pageSize int) (*Writer, error) {
	if keyLen <= 0 || keyLen > 256 {
		return nil, fmt.Errorf("coldstore: bad keyLen %d", keyLen)
	}
	if pageSize <= 0 || pageSize > 65535 {
		return nil, fmt.Errorf("coldstore: bad pageSize %d", pageSize)
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

	hdr := FileHeader{
		Magic:    KVMagic,
		Version:  Version,
		KeyLen:   uint16(keyLen),
		PageSize: uint16(pageSize),
	}
	hdrBytes := hdr.Marshal()
	if _, err := kvF.Write(hdrBytes); err != nil {
		kvF.Close()
		idxF.Close()
		return nil, fmt.Errorf("write kv header: %w", err)
	}
	hdr.Magic = IdxMagic
	hdrBytes = hdr.Marshal()
	if _, err := idxF.Write(hdrBytes); err != nil {
		kvF.Close()
		idxF.Close()
		return nil, fmt.Errorf("write idx header: %w", err)
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		kvF.Close()
		idxF.Close()
		return nil, fmt.Errorf("zstd writer: %w", err)
	}

	return &Writer{
		keyLen:   keyLen,
		pageSize: pageSize,
		kvFile:   kvF,
		kvBuf:    bufio.NewWriterSize(kvF, 1<<20),
		idxFile:  idxF,
		idxBuf:   bufio.NewWriterSize(idxF, 1<<20),
		enc:      enc,
		kvOffset: int64(HeaderSize),
		rawPage:  make([]byte, 0, 4096),
	}, nil
}

// Append adds one (key, value) pair. Keys must be strictly ascending.
func (w *Writer) Append(key, value []byte) error {
	if len(key) != w.keyLen {
		return fmt.Errorf("coldstore: key len %d != configured %d", len(key), w.keyLen)
	}
	if w.lastKey != nil {
		// strictly ascending check via bytes compare
		cmp := compareKey(key, w.lastKey)
		if cmp <= 0 {
			return fmt.Errorf("coldstore: keys not strictly ascending at #%d (prev %x, curr %x)",
				w.keyCount, w.lastKey, key)
		}
	}
	if len(w.rawPage) == 0 {
		w.firstKey = append(w.firstKey[:0], key...)
	}
	// Append [key][scheme-C value] to current raw page.
	w.rawPage = append(w.rawPage, key...)
	before := len(w.rawPage)
	w.rawPage = EncodeValue(w.rawPage, value)
	w.totalRawSize += uint64(len(w.rawPage) - before + len(key))

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
	// Page layout in .kv: [comp_len:4B LE][comp_bytes]
	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(comp)))
	if _, err := w.kvBuf.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := w.kvBuf.Write(comp); err != nil {
		return err
	}

	// Index entry: [firstKey:keyLen B][pageOffset:8B LE]
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

// Close finalizes both files. Any partial trailing page is flushed.
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

// Stats reports counters useful for the build tool's summary.
type Stats struct {
	KeyCount     uint64
	PageCount    uint64
	TotalKvSize  uint64
	TotalRawSize uint64
}

func (w *Writer) Stats() Stats {
	return Stats{
		KeyCount:     w.keyCount,
		PageCount:    w.pageCount,
		TotalKvSize:  w.totalKvSize,
		TotalRawSize: w.totalRawSize,
	}
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
