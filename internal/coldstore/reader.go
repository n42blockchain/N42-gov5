package coldstore

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/klauspost/compress/zstd"
)

// Reader provides point lookups against a .kv+.idx pair built by Writer.
// All access is read-only and safe for concurrent Get() calls.
type Reader struct {
	keyLen   int
	pageSize int

	idx     []byte // mmap'd idx file body (after header), held verbatim for binary search
	idxStride int   // keyLen + 8

	kvFile *os.File
	dec    *zstd.Decoder
}

// Open opens baseDir/{prefix}.kv + .idx and returns a Reader.
func Open(baseDir, prefix string) (*Reader, error) {
	kvPath := baseDir + "/" + prefix + ".kv"
	idxPath := baseDir + "/" + prefix + ".idx"

	idxBytes, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, fmt.Errorf("read idx: %w", err)
	}
	if len(idxBytes) < HeaderSize {
		return nil, fmt.Errorf("idx too short: %d B", len(idxBytes))
	}
	hdr, err := UnmarshalHeader(idxBytes[:HeaderSize])
	if err != nil {
		return nil, err
	}
	if hdr.Magic != IdxMagic {
		return nil, fmt.Errorf("idx magic mismatch: 0x%08x", hdr.Magic)
	}
	if hdr.Version != Version {
		return nil, fmt.Errorf("idx version %d != %d", hdr.Version, Version)
	}
	if hdr.KeyLen == 0 || hdr.PageSize == 0 {
		return nil, fmt.Errorf("idx zero keyLen/pageSize")
	}
	body := idxBytes[HeaderSize:]
	stride := int(hdr.KeyLen) + 8
	if len(body)%stride != 0 {
		return nil, fmt.Errorf("idx body %d not multiple of stride %d", len(body), stride)
	}

	kvF, err := os.Open(kvPath)
	if err != nil {
		return nil, fmt.Errorf("open kv: %w", err)
	}
	var kvHdrBuf [HeaderSize]byte
	if _, err := io.ReadFull(kvF, kvHdrBuf[:]); err != nil {
		kvF.Close()
		return nil, fmt.Errorf("read kv header: %w", err)
	}
	kvHdr, _ := UnmarshalHeader(kvHdrBuf[:])
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
		return nil, fmt.Errorf("zstd reader: %w", err)
	}

	return &Reader{
		keyLen:    int(hdr.KeyLen),
		pageSize:  int(hdr.PageSize),
		idx:       body,
		idxStride: stride,
		kvFile:    kvF,
		dec:       dec,
	}, nil
}

func (r *Reader) Close() error {
	r.dec.Close()
	return r.kvFile.Close()
}

func (r *Reader) PageCount() int { return len(r.idx) / r.idxStride }

func (r *Reader) KeyLen() int { return r.keyLen }

// Get returns the value for key, or (nil, false) if absent.
func (r *Reader) Get(key []byte) ([]byte, bool, error) {
	if len(key) != r.keyLen {
		return nil, false, fmt.Errorf("Get: key len %d != %d", len(key), r.keyLen)
	}
	if len(r.idx) == 0 {
		return nil, false, nil
	}

	// Find rightmost page whose firstKey <= key.
	n := r.PageCount()
	page := sort.Search(n, func(i int) bool {
		fk := r.idx[i*r.idxStride : i*r.idxStride+r.keyLen]
		return compareKey(fk, key) > 0
	})
	// If page == 0, key < all firstKeys → not present.
	if page == 0 {
		return nil, false, nil
	}
	page--

	pageOff := binary.LittleEndian.Uint64(r.idx[page*r.idxStride+r.keyLen : page*r.idxStride+r.keyLen+8])

	// Read page len from .kv at pageOff.
	var lenBuf [4]byte
	if _, err := r.kvFile.ReadAt(lenBuf[:], int64(pageOff)); err != nil {
		return nil, false, fmt.Errorf("read page len at %d: %w", pageOff, err)
	}
	compLen := binary.LittleEndian.Uint32(lenBuf[:])
	comp := make([]byte, compLen)
	if _, err := r.kvFile.ReadAt(comp, int64(pageOff)+4); err != nil {
		return nil, false, fmt.Errorf("read page body at %d: %w", pageOff+4, err)
	}
	raw, err := r.dec.DecodeAll(comp, nil)
	if err != nil {
		return nil, false, fmt.Errorf("decompress page %d: %w", page, err)
	}

	// Linear scan inside page. Layout: repeated [key:keyLen][scheme-C value].
	pos := 0
	for pos < len(raw) {
		if pos+r.keyLen > len(raw) {
			return nil, false, fmt.Errorf("page %d truncated key at pos %d", page, pos)
		}
		k := raw[pos : pos+r.keyLen]
		pos += r.keyLen
		v, np, err := DecodeValue(raw, pos)
		if err != nil {
			return nil, false, fmt.Errorf("page %d: %w", page, err)
		}
		pos = np
		cmp := compareKey(k, key)
		if cmp == 0 {
			// Copy out — caller may retain.
			out := make([]byte, len(v))
			copy(out, v)
			return out, true, nil
		}
		if cmp > 0 {
			// Sorted page; past our key → not present.
			return nil, false, nil
		}
	}
	return nil, false, nil
}
