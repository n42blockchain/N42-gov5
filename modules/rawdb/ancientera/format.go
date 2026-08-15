// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package ancientera implements the sealed, self-describing era container
// for the n42 native chain's ancient history (docs/QS_ANCIENT_ERA_DESIGN.md).
//
// One era file covers a fixed power-of-two block range for one content
// class. Files are immutable after sealing and byte-deterministic: the
// same chain range always seals to the same bytes (creator metadata is
// excluded from the hashed payload region).
//
// File layout (format version 1):
//
//	magic "N42ERA1\0"                                    (8 bytes)
//	frame[0..n)     zstd-compressed 64-block batches
//	frame index     u32 count, then per frame:
//	                u64 offset | u32 compLen | u32 rawLen | u64 xxh64
//	meta            u32 len | JSON (EraMeta)
//	footer (72 B)   u64 frameIndexOff | u64 metaOff |
//	                payload blake3-256 (32) | u64 indexXxh | u64 metaXxh |
//	                u32 version | magic "N42E"
//
// The payload blake3 covers exactly the frame region (from byte 8 to
// frameIndexOff) and is the manifest cross-check value.
package ancientera

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	// FormatVersion is the era container version this package writes and
	// the only major version it accepts.
	FormatVersion = 1

	// DefaultSpan is the default number of blocks per era (2^20).
	DefaultSpan = 1 << 20

	// FrameBlocks is the fixed number of blocks per compressed frame.
	// Era spans must be a multiple of FrameBlocks.
	FrameBlocks = 64

	headMagic   = "N42ERA1\x00"
	footMagic   = "N42E"
	footerSize  = 8 + 8 + 32 + 8 + 8 + 4 + 4
	maxMetaSize = 1 << 20 // sanity cap when reading

	// FileExt is the era file extension.
	FileExt = ".era"
)

// Class identifies the content class of an era file.
type Class uint16

const (
	// ClassChain is the mandatory class: canonical hash + header +
	// consensus evidence. Never auto-pruned.
	ClassChain Class = 1
	// ClassExec holds bodies (with inlined transactions), receipts and
	// transaction logs. Optional; missing ⇒ pruned.
	ClassExec Class = 2
	// ClassAux holds block witnesses and account/storage changesets.
	// Optional; first to prune.
	ClassAux Class = 3
)

func (c Class) String() string {
	switch c {
	case ClassChain:
		return "chain"
	case ClassExec:
		return "exec"
	case ClassAux:
		return "aux"
	}
	return fmt.Sprintf("class(%d)", uint16(c))
}

// ParseClass parses "chain"/"exec"/"aux".
func ParseClass(s string) (Class, error) {
	switch s {
	case "chain":
		return ClassChain, nil
	case "exec":
		return ClassExec, nil
	case "aux":
		return ClassAux, nil
	}
	return 0, fmt.Errorf("unknown era class %q", s)
}

// Classes lists all classes in canonical order.
var Classes = []Class{ClassChain, ClassExec, ClassAux}

// Per-block entry table IDs. Entries are sub-TLVs inside a block record:
// u8 tableID | u32 len | bytes. Absent tables are simply omitted (e.g.
// receipts for empty blocks). IDs are namespaced per class but unique
// globally for clarity.
const (
	TblCanonicalHash byte = 0x01 // 32-byte canonical hash
	TblHeader        byte = 0x02 // raw header bytes (storage codec)
	TblEvidence      byte = 0x03 // raw ConsensusEvidence bytes

	TblBody     byte = 0x10 // raw body storage value (BaseTxId+TxAmount)
	TblTxs      byte = 0x11 // concat of u32-len-prefixed raw tx bytes
	TblSenders  byte = 0x12 // reserved (TxSender unused on the qs chain)
	TblReceipts byte = 0x13 // raw receipts bytes (whole-block value)
	TblLogs     byte = 0x14 // concat of u32 txId | u32 len | raw log bytes

	TblWitness byte = 0x20 // raw block witness bytes
	TblAcctCS  byte = 0x21 // concat of u32-len-prefixed changeset dup values
	TblStorCS  byte = 0x22 // concat of u32-len-prefixed changeset dup values
)

// BlockEntry is the decoded per-block record: raw bytes per table ID.
// Values are nil when the table is absent for the block.
type BlockEntry map[byte][]byte

// EncodeInto appends the block record encoding (u32 total | TLVs) to dst.
// Table IDs are emitted in ascending order for determinism.
func (e BlockEntry) EncodeInto(dst []byte) []byte {
	total := 0
	for id := 0; id < 256; id++ {
		if v, ok := e[byte(id)]; ok && v != nil {
			total += 1 + 4 + len(v)
		}
	}
	var b4 [4]byte
	binary.BigEndian.PutUint32(b4[:], uint32(total))
	dst = append(dst, b4[:]...)
	for id := 0; id < 256; id++ {
		if v, ok := e[byte(id)]; ok && v != nil {
			dst = append(dst, byte(id))
			binary.BigEndian.PutUint32(b4[:], uint32(len(v)))
			dst = append(dst, b4[:]...)
			dst = append(dst, v...)
		}
	}
	return dst
}

// decodeBlockEntry parses one block record at buf[off:], returning the
// entry and the offset just past it.
func decodeBlockEntry(buf []byte, off int) (BlockEntry, int, error) {
	if off+4 > len(buf) {
		return nil, 0, errors.New("era: truncated block record length")
	}
	total := int(binary.BigEndian.Uint32(buf[off:]))
	off += 4
	end := off + total
	if end > len(buf) {
		return nil, 0, errors.New("era: block record exceeds frame")
	}
	e := make(BlockEntry, 4)
	for off < end {
		if off+5 > end {
			return nil, 0, errors.New("era: truncated TLV header")
		}
		id := buf[off]
		l := int(binary.BigEndian.Uint32(buf[off+1:]))
		off += 5
		if off+l > end {
			return nil, 0, errors.New("era: TLV value exceeds record")
		}
		e[id] = buf[off : off+l]
		off += l
	}
	return e, end, nil
}

// skipBlockEntry returns the offset just past the record at buf[off:].
func skipBlockEntry(buf []byte, off int) (int, error) {
	if off+4 > len(buf) {
		return 0, errors.New("era: truncated block record length")
	}
	total := int(binary.BigEndian.Uint32(buf[off:]))
	end := off + 4 + total
	if end > len(buf) {
		return 0, errors.New("era: block record exceeds frame")
	}
	return end, nil
}

// EraMeta is the versioned self-description carried by every era file.
// Field order is fixed (struct order) so the JSON encoding is
// deterministic; Creator is descriptive only and excluded from the
// payload hash region by construction (meta sits after the frames).
type EraMeta struct {
	FormatVersion int    `json:"format_version"`
	Class         string `json:"class"`
	Era           uint64 `json:"era"`
	Span          uint64 `json:"span"`
	ChainID       uint64 `json:"chain_id"`
	GenesisHash   string `json:"genesis_hash"`
	StartBlock    uint64 `json:"start_block"`
	EndBlock      uint64 `json:"end_block"` // exclusive
	FirstHash     string `json:"first_hash"`
	LastHash      string `json:"last_hash"`
	ParentHash    string `json:"parent_hash"`
	Accumulator   string `json:"accumulator,omitempty"` // class A only
	Codecs        Codecs `json:"codecs"`
	PayloadBlake3 string `json:"payload_blake3"`
	Creator       string `json:"creator,omitempty"`
}

// Codecs pins every encoding that affects era bytes.
type Codecs struct {
	Container int    `json:"container"` // = FormatVersion
	Zstd      string `json:"zstd"`      // encoder level name
	Entry     int    `json:"entry"`     // block-record TLV version
}

// PinnedCodecs is the deterministic codec set for format version 1.
var PinnedCodecs = Codecs{Container: FormatVersion, Zstd: "better", Entry: 1}

// FileName returns the canonical era file name:
// <class>-<era 8 digits>-<first 8 hex of first canonical hash>.era
func FileName(class Class, era uint64, firstHash8 string) string {
	return fmt.Sprintf("%s-%08d-%s%s", class, era, firstHash8, FileExt)
}

type frameIndexEntry struct {
	offset  uint64
	compLen uint32
	rawLen  uint32
	xxh     uint64
}

func encodeFrameIndex(entries []frameIndexEntry) []byte {
	out := make([]byte, 4+len(entries)*24)
	binary.BigEndian.PutUint32(out, uint32(len(entries)))
	p := 4
	for _, e := range entries {
		binary.BigEndian.PutUint64(out[p:], e.offset)
		binary.BigEndian.PutUint32(out[p+8:], e.compLen)
		binary.BigEndian.PutUint32(out[p+12:], e.rawLen)
		binary.BigEndian.PutUint64(out[p+16:], e.xxh)
		p += 24
	}
	return out
}

func decodeFrameIndex(buf []byte) ([]frameIndexEntry, error) {
	if len(buf) < 4 {
		return nil, errors.New("era: frame index too short")
	}
	n := int(binary.BigEndian.Uint32(buf))
	if len(buf) != 4+n*24 {
		return nil, errors.New("era: frame index size mismatch")
	}
	entries := make([]frameIndexEntry, n)
	p := 4
	for i := range entries {
		entries[i] = frameIndexEntry{
			offset:  binary.BigEndian.Uint64(buf[p:]),
			compLen: binary.BigEndian.Uint32(buf[p+8:]),
			rawLen:  binary.BigEndian.Uint32(buf[p+12:]),
			xxh:     binary.BigEndian.Uint64(buf[p+16:]),
		}
		p += 24
	}
	return entries, nil
}

type footer struct {
	frameIndexOff uint64
	metaOff       uint64
	payloadBlake3 [32]byte
	indexXxh      uint64
	metaXxh       uint64
	version       uint32
}

func encodeFooter(f *footer) []byte {
	out := make([]byte, footerSize)
	binary.BigEndian.PutUint64(out[0:], f.frameIndexOff)
	binary.BigEndian.PutUint64(out[8:], f.metaOff)
	copy(out[16:48], f.payloadBlake3[:])
	binary.BigEndian.PutUint64(out[48:], f.indexXxh)
	binary.BigEndian.PutUint64(out[56:], f.metaXxh)
	binary.BigEndian.PutUint32(out[64:], f.version)
	copy(out[68:], footMagic)
	return out
}

func decodeFooter(buf []byte) (*footer, error) {
	if len(buf) != footerSize {
		return nil, errors.New("era: bad footer size")
	}
	if string(buf[68:72]) != footMagic {
		return nil, errors.New("era: bad footer magic")
	}
	f := &footer{
		frameIndexOff: binary.BigEndian.Uint64(buf[0:]),
		metaOff:       binary.BigEndian.Uint64(buf[8:]),
		indexXxh:      binary.BigEndian.Uint64(buf[48:]),
		metaXxh:       binary.BigEndian.Uint64(buf[56:]),
		version:       binary.BigEndian.Uint32(buf[64:]),
	}
	copy(f.payloadBlake3[:], buf[16:48])
	if f.version != FormatVersion {
		return nil, fmt.Errorf("era: unsupported format version %d", f.version)
	}
	return f, nil
}
