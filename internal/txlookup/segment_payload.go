package txlookup

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
)

const datV3HeaderSize = 128

func rejectUnknownDatMagic(data []byte) error {
	if len(data) >= 3 && bytes.Equal(data[:3], []byte("EFD")) &&
		(len(data) < 4 || (!bytes.Equal(data[:4], datMagicV2[:]) && !bytes.Equal(data[:4], datMagicV3[:]))) {
		return fmt.Errorf("unknown txindex data version")
	}
	return nil
}

// SegmentAnchor binds the committed frame to one chain and its canonical
// terminal block. The caller must read the terminal hash from the same
// canonical prefix used to produce the transaction hashes.
type SegmentAnchor struct {
	Identity         types.Hash
	EndCanonicalHash types.Hash
}

type segmentPayload struct {
	version             uint8
	blockCount, txCount uint64
	identity, endHash   types.Hash
	ef                  *eliasfano32.EliasFano
	raw                 []byte
}

// decodeSegmentPayload is the only format decoder used by inventory, direct
// OpenSegment, and lazy service loading. An unknown EFD version is never V1.
func decodeSegmentPayload(data []byte, idx *recsplit.Index, ri []byte) (segmentPayload, error) {
	var p segmentPayload
	start := idx.BaseDataID()
	switch {
	case len(data) >= 4 && bytes.Equal(data[:4], datMagicV3[:]):
		if len(data) < datV3HeaderSize || binary.LittleEndian.Uint32(data[4:8]) != datV3HeaderSize {
			return p, fmt.Errorf("invalid EFD3 header")
		}
		if err := verifyV3Digest(data, ri); err != nil {
			return p, err
		}
		if got := binary.LittleEndian.Uint64(data[8:16]); got != start {
			return p, fmt.Errorf("EFD3 start %d differs from index %d", got, start)
		}
		p.version = 3
		p.blockCount = binary.LittleEndian.Uint64(data[16:24])
		p.txCount = binary.LittleEndian.Uint64(data[24:32])
		copy(p.identity[:], data[32:64])
		copy(p.endHash[:], data[64:96])
		data = data[datV3HeaderSize:]
		if p.identity == (types.Hash{}) || p.endHash == (types.Hash{}) {
			return p, fmt.Errorf("EFD3 missing chain or canonical anchor")
		}
	case len(data) >= 4 && bytes.Equal(data[:4], datMagicV2[:]):
		if len(data) < 16 {
			return p, fmt.Errorf("truncated EFD2 header")
		}
		p.version = 2
		p.blockCount = uint64(binary.LittleEndian.Uint32(data[4:8]))
		p.txCount = binary.LittleEndian.Uint64(data[8:16])
		data = data[16:]
	case len(data) >= 3 && bytes.Equal(data[:3], []byte("EFD")):
		return p, fmt.Errorf("unknown txindex data version")
	default:
		if len(data) == 0 {
			return p, fmt.Errorf("empty txindex data")
		}
		p.version = 1
		p.blockCount = SegmentSize
		p.txCount = idx.KeyCount()
		if p.txCount > ^uint64(0)/4 || uint64(len(data)) != p.txCount*4 {
			return p, fmt.Errorf("V1 index/data count mismatch")
		}
		p.raw = data
		return p, nil
	}
	if p.blockCount == 0 || start > ^uint64(0)-p.blockCount || p.txCount != idx.KeyCount() {
		return p, fmt.Errorf("EFD%d invalid range or transaction count", p.version)
	}
	if p.txCount == 0 {
		if len(data) != 0 {
			return p, fmt.Errorf("empty EFD%d has trailing payload", p.version)
		}
		return p, nil
	}
	if len(data) < 24 || (len(data)-16)%8 != 0 || p.txCount == ^uint64(0) {
		return p, fmt.Errorf("invalid EFD%d Elias-Fano size", p.version)
	}
	var err error
	p.ef, err = validatedEliasFano(data, p.blockCount, p.txCount)
	return p, err
}

func buildDatV3Bytes(start, blockCount, totalTx uint64, txPerBlock []uint32, anchor SegmentAnchor) []byte {
	var header [datV3HeaderSize]byte
	copy(header[:4], datMagicV3[:])
	binary.LittleEndian.PutUint32(header[4:8], datV3HeaderSize)
	binary.LittleEndian.PutUint64(header[8:16], start)
	binary.LittleEndian.PutUint64(header[16:24], blockCount)
	binary.LittleEndian.PutUint64(header[24:32], totalTx)
	copy(header[32:64], anchor.Identity[:])
	copy(header[64:96], anchor.EndCanonicalHash[:])
	if totalTx == 0 {
		return header[:]
	}
	v2 := buildDatV2Bytes(blockCount, totalTx, txPerBlock)
	return append(header[:], v2[16:]...)
}

func writeV3Digest(h hash.Hash, data, ri []byte) {
	h.Write([]byte("N42/TXLOOKUP/FRAME/v3\x00"))
	h.Write(data[:96])
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(data)-datV3HeaderSize))
	h.Write(size[:])
	h.Write(data[datV3HeaderSize:])
	binary.BigEndian.PutUint64(size[:], uint64(len(ri)))
	h.Write(size[:])
	h.Write(ri)
}

func verifyV3Digest(data, ri []byte) error {
	if len(data) < datV3HeaderSize || len(ri) == 0 {
		return fmt.Errorf("EFD3 missing digest input")
	}
	h := sha256.New()
	writeV3Digest(h, data, ri)
	if !bytes.Equal(data[96:128], h.Sum(nil)) {
		return fmt.Errorf("EFD3 frame digest mismatch")
	}
	return nil
}

func sealV3Digest(dat []byte, idxPath string) error {
	if len(dat) < datV3HeaderSize {
		return fmt.Errorf("EFD3 header truncated")
	}
	f, err := os.Open(idxPath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() <= 0 {
		return fmt.Errorf("EFD3 index empty")
	}
	h := sha256.New()
	h.Write([]byte("N42/TXLOOKUP/FRAME/v3\x00"))
	h.Write(dat[:96])
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(dat)-datV3HeaderSize))
	h.Write(size[:])
	h.Write(dat[datV3HeaderSize:])
	binary.BigEndian.PutUint64(size[:], uint64(st.Size()))
	h.Write(size[:])
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	copy(dat[96:128], h.Sum(nil))
	return nil
}
