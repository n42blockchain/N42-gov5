// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package syncwire holds the byte layouts of the v1.1 sync protocols of the
// N42 common sync standard (N42-SYNC-SPEC.md §5): bodies_by_range/2,
// commit_qc/1 and the QMDB state-sync protocols qmdb_status/1,
// qmdb_manifest/1, qmdb_twigs/1 and code_by_hash/1. It is a leaf package
// (no p2p, database or consensus imports) so servers, clients and the golden
// vector generator share one definition.
//
// Framing is that of v1 (§1.2): a request is uvarint(len) ‖ snappy-framed
// bytes; a response chunk is code u8 ‖ [digest 4, block and header chunks of
// bodies_by_range/2 only] ‖ uvarint(len) ‖ snappy-framed payload. All
// integers are little-endian. Fixed-layout requests are plain
// concatenations; decoders reject a wrong length and any declared length
// above its maximum before allocating.
package syncwire

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Protocol IDs (§5).
const (
	ProtocolBodiesByRangeV1 = "/rpc/bodies_by_range/1/ssz_snappy"
	ProtocolBodiesByRangeV2 = "/rpc/bodies_by_range/2/ssz_snappy"
	ProtocolCommitQC        = "/rpc/commit_qc/1/ssz_snappy"
	ProtocolQMDBStatus      = "/rpc/qmdb_status/1/ssz_snappy"
	ProtocolQMDBManifest    = "/rpc/qmdb_manifest/1/ssz_snappy"
	ProtocolQMDBTwigs       = "/rpc/qmdb_twigs/1/ssz_snappy"
	ProtocolCodeByHash      = "/rpc/code_by_hash/1/ssz_snappy"
)

// Result codes (§5.0), byte 0 of a chunk.
const (
	CodeSuccess             byte = 0x00
	CodeInvalidRequest      byte = 0x01
	CodeServerError         byte = 0x02
	CodeResourceUnavailable byte = 0x03
	CodeRateLimited         byte = 0x04
	CodeEnd                 byte = 0x10
)

// END reasons (§5.0).
const (
	ReasonComplete     byte   = 0
	ReasonByteBudget   byte   = 1
	ReasonCountCap     byte   = 2
	ReasonCanonicalEnd byte   = 3
	ReasonNotAvailable byte   = 4
	ReasonRateLimited  byte   = 5
	ReasonShuttingDown byte   = 6
	maxReason          byte   = 6
	HashLen                   = 32
	MaxRangeCount             = 1024
	DefaultRangeBytes         = 64 << 20  // max_bytes = 0
	MaxRangeReplyBytes        = 128 << 20 // server cap
	MaxHeaderBytes            = 64 << 10  // HEADERS_ONLY and commit_qc headers
	MaxQCBytes                = 2048
	MaxCommitQCHeaders        = 256
	MaxManifestTwigs          = 16384
	MaxTwigsPerRequest        = 256
	DefaultTwigBytes          = 16 << 20
	MaxTwigReplyBytes         = 64 << 20
	MaxRecordValue            = 16 << 20
	MaxCodeHashes             = 256
	MaxCodeBytes              = 24576 * 2
	TwigSize                  = 2048
	TwigBitsLen               = TwigSize / 8
	FlagHeadersOnly    uint64 = 1
	FlagBitsOnly       uint32 = 1
)

// ErrLength is returned for a fixed-layout message of the wrong length.
var ErrLength = errors.New("syncwire: wrong message length")

// Hash is a 32-byte value.
type Hash = [32]byte

// ---------------------------------------------------------------------------
// bodies_by_range/2 (§5.1)

// RangeRequest is the 32-byte request of bodies_by_range/2.
type RangeRequest struct {
	Start    uint64
	Count    uint64 // 1..1024
	MaxBytes uint64 // 0 = server default (64 MiB)
	Flags    uint64 // bit0 HEADERS_ONLY
}

// RangeRequestLen is the encoded size of a RangeRequest.
const RangeRequestLen = 32

func (r RangeRequest) Encode() []byte {
	b := make([]byte, 0, RangeRequestLen)
	b = binary.LittleEndian.AppendUint64(b, r.Start)
	b = binary.LittleEndian.AppendUint64(b, r.Count)
	b = binary.LittleEndian.AppendUint64(b, r.MaxBytes)
	return binary.LittleEndian.AppendUint64(b, r.Flags)
}

// HeadersOnly reports the HEADERS_ONLY flag.
func (r RangeRequest) HeadersOnly() bool { return r.Flags&FlagHeadersOnly != 0 }

// Budget is the decoded-byte budget a server applies: min(max_bytes or
// 64 MiB, 128 MiB).
func (r RangeRequest) Budget() uint64 {
	b := r.MaxBytes
	if b == 0 {
		b = DefaultRangeBytes
	}
	if b > MaxRangeReplyBytes {
		b = MaxRangeReplyBytes
	}
	return b
}

// DecodeRangeRequest decodes and validates a bodies_by_range/2 request.
func DecodeRangeRequest(b []byte) (RangeRequest, error) {
	if len(b) != RangeRequestLen {
		return RangeRequest{}, ErrLength
	}
	r := RangeRequest{
		Start:    binary.LittleEndian.Uint64(b[0:8]),
		Count:    binary.LittleEndian.Uint64(b[8:16]),
		MaxBytes: binary.LittleEndian.Uint64(b[16:24]),
		Flags:    binary.LittleEndian.Uint64(b[24:32]),
	}
	if r.Count == 0 || r.Count > MaxRangeCount {
		return r, fmt.Errorf("syncwire: range count %d outside 1..%d", r.Count, MaxRangeCount)
	}
	if r.Flags&^FlagHeadersOnly != 0 {
		return r, fmt.Errorf("syncwire: reserved range flag bits %#x set", r.Flags&^FlagHeadersOnly)
	}
	if r.Start+r.Count < r.Start {
		return r, errors.New("syncwire: range overflows u64")
	}
	return r, nil
}

// RangeEnd is the 17-byte END trailer of bodies_by_range/2.
type RangeEnd struct {
	Next        uint64 // first number not served
	ServedBytes uint64 // sum of decoded payload lengths
	Reason      byte
}

// EndLen is the encoded size of RangeEnd and TwigsEnd.
const EndLen = 17

func (e RangeEnd) Encode() []byte {
	b := make([]byte, 0, EndLen)
	b = binary.LittleEndian.AppendUint64(b, e.Next)
	b = binary.LittleEndian.AppendUint64(b, e.ServedBytes)
	return append(b, e.Reason)
}

// DecodeRangeEnd decodes a RangeEnd.
func DecodeRangeEnd(b []byte) (RangeEnd, error) {
	if len(b) != EndLen {
		return RangeEnd{}, ErrLength
	}
	e := RangeEnd{Next: binary.LittleEndian.Uint64(b[0:8]), ServedBytes: binary.LittleEndian.Uint64(b[8:16]), Reason: b[16]}
	if e.Reason > maxReason {
		return e, fmt.Errorf("syncwire: unknown END reason %d", e.Reason)
	}
	return e, nil
}

// CheckRangeEnd applies the client MUSTs of §5.1 to a reply of `chunks`
// payloads whose decoded lengths sum to `bytes`: next = start + chunks,
// served_bytes = bytes, reason 0 iff next = start + count, and zero chunks
// only with reason 3..6. A violation is a provable fault of the server.
func CheckRangeEnd(req RangeRequest, e RangeEnd, chunks, bytes uint64) error {
	if e.Next != req.Start+chunks {
		return fmt.Errorf("syncwire: END next %d, but %d chunks from %d were sent", e.Next, chunks, req.Start)
	}
	if e.ServedBytes != bytes {
		return fmt.Errorf("syncwire: END served_bytes %d, chunks carried %d", e.ServedBytes, bytes)
	}
	if (e.Reason == ReasonComplete) != (e.Next == req.Start+req.Count) {
		return fmt.Errorf("syncwire: END reason %d with next %d for start %d count %d", e.Reason, e.Next, req.Start, req.Count)
	}
	if chunks == 0 && e.Reason < ReasonCanonicalEnd {
		return fmt.Errorf("syncwire: END reason %d with no chunk", e.Reason)
	}
	if chunks > req.Count {
		return fmt.Errorf("syncwire: %d chunks for a count of %d", chunks, req.Count)
	}
	return nil
}

// ---------------------------------------------------------------------------
// commit_qc/1 (§5.2)

// CommitQCRequest is the 40-byte request of commit_qc/1. Hash zero asks by
// number only (the server's canonical block at Number).
type CommitQCRequest struct {
	Number uint64
	Hash   Hash
}

// CommitQCRequestLen is the encoded size of a CommitQCRequest.
const CommitQCRequestLen = 40

func (r CommitQCRequest) Encode() []byte {
	b := binary.LittleEndian.AppendUint64(make([]byte, 0, CommitQCRequestLen), r.Number)
	return append(b, r.Hash[:]...)
}

// DecodeCommitQCRequest decodes a CommitQCRequest.
func DecodeCommitQCRequest(b []byte) (CommitQCRequest, error) {
	if len(b) != CommitQCRequestLen {
		return CommitQCRequest{}, ErrLength
	}
	var r CommitQCRequest
	r.Number = binary.LittleEndian.Uint64(b[:8])
	copy(r.Hash[:], b[8:])
	return r, nil
}

// CommitQCResponse is the SUCCESS payload of commit_qc/1:
// qc_len u32 ‖ QC wire ‖ header_count u16 ‖ header_count × (u32 len ‖ RLP header).
// Headers run from the QC's block down to the requested number.
type CommitQCResponse struct {
	QC      []byte
	Headers [][]byte
}

// MaxCommitQCPayload bounds the framed payload of commit_qc/1.
const MaxCommitQCPayload = 4 + MaxQCBytes + 2 + MaxCommitQCHeaders*(4+MaxHeaderBytes)

func (r CommitQCResponse) Encode() ([]byte, error) {
	if len(r.QC) == 0 || len(r.QC) > MaxQCBytes {
		return nil, fmt.Errorf("syncwire: QC of %d bytes", len(r.QC))
	}
	if len(r.Headers) > MaxCommitQCHeaders {
		return nil, fmt.Errorf("syncwire: %d headers", len(r.Headers))
	}
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(r.QC)))
	out = append(out, r.QC...)
	out = binary.LittleEndian.AppendUint16(out, uint16(len(r.Headers)))
	for _, h := range r.Headers {
		if len(h) == 0 || len(h) > MaxHeaderBytes {
			return nil, fmt.Errorf("syncwire: header of %d bytes", len(h))
		}
		out = binary.LittleEndian.AppendUint32(out, uint32(len(h)))
		out = append(out, h...)
	}
	return out, nil
}

// DecodeCommitQCResponse decodes a commit_qc/1 payload (shape only).
func DecodeCommitQCResponse(b []byte) (CommitQCResponse, error) {
	rd := reader{b: b}
	var r CommitQCResponse
	n, err := rd.u32()
	if err != nil {
		return r, err
	}
	if n == 0 || n > MaxQCBytes {
		return r, fmt.Errorf("syncwire: QC length %d", n)
	}
	if r.QC, err = rd.take(int(n)); err != nil {
		return r, err
	}
	count, err := rd.u16()
	if err != nil {
		return r, err
	}
	if count > MaxCommitQCHeaders {
		return r, fmt.Errorf("syncwire: %d headers > %d", count, MaxCommitQCHeaders)
	}
	r.Headers = make([][]byte, 0, count)
	for i := 0; i < int(count); i++ {
		l, err := rd.u32()
		if err != nil {
			return r, err
		}
		if l == 0 || l > MaxHeaderBytes {
			return r, fmt.Errorf("syncwire: header %d length %d", i, l)
		}
		h, err := rd.take(int(l))
		if err != nil {
			return r, err
		}
		r.Headers = append(r.Headers, h)
	}
	if !rd.done() {
		return r, fmt.Errorf("syncwire: %d trailing bytes", len(rd.b))
	}
	return r, nil
}

// ---------------------------------------------------------------------------
// qmdb_status/1 (§5.3.2)

// QMDBStatus is the 57-byte qmdb_status/1 response.
type QMDBStatus struct {
	EarliestBody uint64
	PivotLo      uint64
	PivotHi      uint64
	PivotHiHash  Hash
	Flags        byte // bit0 twigs, bit1 code
}

const (
	QMDBStatusLen        = 57
	StatusFlagTwigs byte = 1
	StatusFlagCode  byte = 2
)

func (s QMDBStatus) Encode() []byte {
	b := make([]byte, 0, QMDBStatusLen)
	b = binary.LittleEndian.AppendUint64(b, s.EarliestBody)
	b = binary.LittleEndian.AppendUint64(b, s.PivotLo)
	b = binary.LittleEndian.AppendUint64(b, s.PivotHi)
	b = append(b, s.PivotHiHash[:]...)
	return append(b, s.Flags)
}

// DecodeQMDBStatus decodes a QMDBStatus.
func DecodeQMDBStatus(b []byte) (QMDBStatus, error) {
	if len(b) != QMDBStatusLen {
		return QMDBStatus{}, ErrLength
	}
	var s QMDBStatus
	s.EarliestBody = binary.LittleEndian.Uint64(b[0:8])
	s.PivotLo = binary.LittleEndian.Uint64(b[8:16])
	s.PivotHi = binary.LittleEndian.Uint64(b[16:24])
	copy(s.PivotHiHash[:], b[24:56])
	s.Flags = b[56]
	if s.PivotLo > s.PivotHi {
		return s, fmt.Errorf("syncwire: pivot_lo %d > pivot_hi %d", s.PivotLo, s.PivotHi)
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// qmdb_manifest/1 (§5.3.3)

// ManifestRequest is the 48-byte qmdb_manifest/1 request.
type ManifestRequest struct {
	PivotHash Hash
	FirstTwig uint64
	Count     uint32 // 1..16384
	Reserved  uint32 // = 0
}

const ManifestRequestLen = 48

func (r ManifestRequest) Encode() []byte {
	b := append(make([]byte, 0, ManifestRequestLen), r.PivotHash[:]...)
	b = binary.LittleEndian.AppendUint64(b, r.FirstTwig)
	b = binary.LittleEndian.AppendUint32(b, r.Count)
	return binary.LittleEndian.AppendUint32(b, r.Reserved)
}

// DecodeManifestRequest decodes and validates a ManifestRequest.
func DecodeManifestRequest(b []byte) (ManifestRequest, error) {
	if len(b) != ManifestRequestLen {
		return ManifestRequest{}, ErrLength
	}
	var r ManifestRequest
	copy(r.PivotHash[:], b[:32])
	r.FirstTwig = binary.LittleEndian.Uint64(b[32:40])
	r.Count = binary.LittleEndian.Uint32(b[40:44])
	r.Reserved = binary.LittleEndian.Uint32(b[44:48])
	if r.Count == 0 || r.Count > MaxManifestTwigs {
		return r, fmt.Errorf("syncwire: manifest count %d outside 1..%d", r.Count, MaxManifestTwigs)
	}
	if r.Reserved != 0 {
		return r, errors.New("syncwire: manifest reserved field is not zero")
	}
	return r, nil
}

// TwigRoots is one manifest entry.
type TwigRoots struct {
	LeafRoot Hash
	BitsRoot Hash
}

// ManifestPage is the qmdb_manifest/1 response.
type ManifestPage struct {
	PivotNumber uint64
	PivotHash   Hash
	Root        Hash
	NextSlot    uint64
	TwigCount   uint64
	FirstTwig   uint64
	Twigs       []TwigRoots
	Proof       []Hash
}

// MaxManifestPayload bounds the framed payload of a manifest page.
const MaxManifestPayload = 8 + 32 + 32 + 8 + 8 + 8 + 4 + MaxManifestTwigs*64 + 4 + 2*64*32

func (p ManifestPage) Encode() []byte {
	b := make([]byte, 0, 100+len(p.Twigs)*64+4+len(p.Proof)*32)
	b = binary.LittleEndian.AppendUint64(b, p.PivotNumber)
	b = append(b, p.PivotHash[:]...)
	b = append(b, p.Root[:]...)
	b = binary.LittleEndian.AppendUint64(b, p.NextSlot)
	b = binary.LittleEndian.AppendUint64(b, p.TwigCount)
	b = binary.LittleEndian.AppendUint64(b, p.FirstTwig)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(p.Twigs)))
	for _, t := range p.Twigs {
		b = append(b, t.LeafRoot[:]...)
		b = append(b, t.BitsRoot[:]...)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(p.Proof)))
	for _, h := range p.Proof {
		b = append(b, h[:]...)
	}
	return b
}

// DecodeManifestPage decodes a ManifestPage (shape only; VerifyManifestPage
// checks it).
func DecodeManifestPage(b []byte) (ManifestPage, error) {
	rd := reader{b: b}
	var p ManifestPage
	var err error
	if p.PivotNumber, err = rd.u64(); err != nil {
		return p, err
	}
	if err = rd.hash(&p.PivotHash); err != nil {
		return p, err
	}
	if err = rd.hash(&p.Root); err != nil {
		return p, err
	}
	if p.NextSlot, err = rd.u64(); err != nil {
		return p, err
	}
	if p.TwigCount, err = rd.u64(); err != nil {
		return p, err
	}
	if p.FirstTwig, err = rd.u64(); err != nil {
		return p, err
	}
	n, err := rd.u32()
	if err != nil {
		return p, err
	}
	if n > MaxManifestTwigs || int(n)*64 > len(rd.b) {
		return p, fmt.Errorf("syncwire: manifest page of %d twigs", n)
	}
	p.Twigs = make([]TwigRoots, n)
	for i := range p.Twigs {
		_ = rd.hash(&p.Twigs[i].LeafRoot)
		_ = rd.hash(&p.Twigs[i].BitsRoot)
	}
	m, err := rd.u32()
	if err != nil {
		return p, err
	}
	if m > 128 || int(m)*32 != len(rd.b) {
		return p, fmt.Errorf("syncwire: manifest proof of %d nodes over %d bytes", m, len(rd.b))
	}
	p.Proof = make([]Hash, m)
	for i := range p.Proof {
		_ = rd.hash(&p.Proof[i])
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// qmdb_twigs/1 (§5.3.4)

// TwigsRequest is the 60-byte qmdb_twigs/1 request.
type TwigsRequest struct {
	PivotHash Hash
	FirstTwig uint64
	TwigCount uint32 // 1..256
	FromSlot  uint64
	Flags     uint32 // bit0 BITS_ONLY
	MaxBytes  uint32 // 0 = 16 MiB; server cap 64 MiB
}

const TwigsRequestLen = 60

func (r TwigsRequest) Encode() []byte {
	b := append(make([]byte, 0, TwigsRequestLen), r.PivotHash[:]...)
	b = binary.LittleEndian.AppendUint64(b, r.FirstTwig)
	b = binary.LittleEndian.AppendUint32(b, r.TwigCount)
	b = binary.LittleEndian.AppendUint64(b, r.FromSlot)
	b = binary.LittleEndian.AppendUint32(b, r.Flags)
	return binary.LittleEndian.AppendUint32(b, r.MaxBytes)
}

// BitsOnly reports the BITS_ONLY flag.
func (r TwigsRequest) BitsOnly() bool { return r.Flags&FlagBitsOnly != 0 }

// Budget is the reply byte budget: max_bytes or 16 MiB, at most 64 MiB.
func (r TwigsRequest) Budget() uint64 {
	b := uint64(r.MaxBytes)
	if b == 0 {
		b = DefaultTwigBytes
	}
	if b > MaxTwigReplyBytes {
		b = MaxTwigReplyBytes
	}
	return b
}

// DecodeTwigsRequest decodes and validates a TwigsRequest.
func DecodeTwigsRequest(b []byte) (TwigsRequest, error) {
	if len(b) != TwigsRequestLen {
		return TwigsRequest{}, ErrLength
	}
	var r TwigsRequest
	copy(r.PivotHash[:], b[:32])
	r.FirstTwig = binary.LittleEndian.Uint64(b[32:40])
	r.TwigCount = binary.LittleEndian.Uint32(b[40:44])
	r.FromSlot = binary.LittleEndian.Uint64(b[44:52])
	r.Flags = binary.LittleEndian.Uint32(b[52:56])
	r.MaxBytes = binary.LittleEndian.Uint32(b[56:60])
	if r.TwigCount == 0 || r.TwigCount > MaxTwigsPerRequest {
		return r, fmt.Errorf("syncwire: twig count %d outside 1..%d", r.TwigCount, MaxTwigsPerRequest)
	}
	if r.Flags&^FlagBitsOnly != 0 {
		return r, fmt.Errorf("syncwire: reserved twig flag bits %#x set", r.Flags&^FlagBitsOnly)
	}
	return r, nil
}

// Record kinds on the twig wire. Kind 1 LIVE (keyHash only) is the
// portable-file form; on the wire a live slot is kind 4 LIVE_P with its
// preimage (owner decision Q-S3) whenever the server holds it. Kind 1 is
// sent only for a live leaf whose preimage the server's plain state does not
// hold (an EIP-161 empty account QMDB keeps; snapshotjoin's coverage rule).
const (
	KindLive   byte = 1
	KindDead   byte = 2
	KindHollow byte = 3
	KindLiveP  byte = 4
)

// Record is one twig record without its slot (positions are implicit).
type Record struct {
	Kind     byte
	KeyHash  Hash   // LIVE; for LIVE_P derived from Preimage
	Preimage []byte // LIVE_P: 20 (account) or 52 (storage) bytes
	Value    []byte // LIVE, LIVE_P
	LeafHash Hash   // DEAD
	LeafRoot Hash   // HOLLOW
}

// TwigChunk is one SUCCESS chunk of qmdb_twigs/1.
type TwigChunk struct {
	TwigID  uint64
	Bits    [TwigBitsLen]byte
	Records []Record
}

func (c TwigChunk) Encode() ([]byte, error) {
	b := binary.LittleEndian.AppendUint64(nil, c.TwigID)
	b = append(b, c.Bits[:]...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(c.Records)))
	for _, r := range c.Records {
		b = append(b, r.Kind)
		switch r.Kind {
		case KindLive:
			if len(r.Value) > MaxRecordValue {
				return nil, errors.New("syncwire: record value too large")
			}
			b = append(b, r.KeyHash[:]...)
			b = binary.LittleEndian.AppendUint32(b, uint32(len(r.Value)))
			b = append(b, r.Value...)
		case KindLiveP:
			if len(r.Preimage) != 20 && len(r.Preimage) != 52 {
				return nil, fmt.Errorf("syncwire: preimage of %d bytes", len(r.Preimage))
			}
			if len(r.Value) > MaxRecordValue {
				return nil, errors.New("syncwire: record value too large")
			}
			b = append(b, byte(len(r.Preimage)))
			b = append(b, r.Preimage...)
			b = binary.LittleEndian.AppendUint32(b, uint32(len(r.Value)))
			b = append(b, r.Value...)
		case KindDead:
			b = append(b, r.LeafHash[:]...)
		case KindHollow:
			b = binary.LittleEndian.AppendUint16(b, TwigSize)
			b = append(b, r.LeafRoot[:]...)
		default:
			return nil, fmt.Errorf("syncwire: record kind %d", r.Kind)
		}
	}
	return b, nil
}

// MaxTwigChunkPayload bounds one framed twig chunk.
const MaxTwigChunkPayload = MaxTwigReplyBytes + MaxRecordValue

// DecodeTwigChunk decodes a TwigChunk (shape only; VerifyTwig checks it).
func DecodeTwigChunk(b []byte) (TwigChunk, error) {
	rd := reader{b: b}
	var c TwigChunk
	var err error
	if c.TwigID, err = rd.u64(); err != nil {
		return c, err
	}
	bits, err := rd.take(TwigBitsLen)
	if err != nil {
		return c, err
	}
	copy(c.Bits[:], bits)
	k, err := rd.u32()
	if err != nil {
		return c, err
	}
	if k > TwigSize || int(k) > len(rd.b) {
		return c, fmt.Errorf("syncwire: %d records", k)
	}
	c.Records = make([]Record, 0, k)
	for i := 0; i < int(k); i++ {
		kind, err := rd.u8()
		if err != nil {
			return c, err
		}
		r := Record{Kind: kind}
		switch kind {
		case KindLive:
			if err = rd.hash(&r.KeyHash); err != nil {
				return c, err
			}
			if r.Value, err = rd.value(); err != nil {
				return c, err
			}
		case KindLiveP:
			pl, err := rd.u8()
			if err != nil {
				return c, err
			}
			if pl != 20 && pl != 52 {
				return c, fmt.Errorf("syncwire: preimage length %d", pl)
			}
			if r.Preimage, err = rd.take(int(pl)); err != nil {
				return c, err
			}
			r.KeyHash = KeyHash(r.Preimage)
			if r.Value, err = rd.value(); err != nil {
				return c, err
			}
		case KindDead:
			if err = rd.hash(&r.LeafHash); err != nil {
				return c, err
			}
		case KindHollow:
			n, err := rd.u16()
			if err != nil {
				return c, err
			}
			if n != TwigSize {
				return c, fmt.Errorf("syncwire: hollow count %d", n)
			}
			if err = rd.hash(&r.LeafRoot); err != nil {
				return c, err
			}
		default:
			return c, fmt.Errorf("syncwire: record kind %d", kind)
		}
		c.Records = append(c.Records, r)
	}
	if !rd.done() {
		return c, fmt.Errorf("syncwire: %d trailing bytes", len(rd.b))
	}
	return c, nil
}

// TwigsEnd is the 17-byte END trailer of qmdb_twigs/1. NextTwig is the first
// twig not served; NextSlot the first slot not covered by the records sent
// (where a follow-up request resumes).
type TwigsEnd struct {
	NextTwig uint64
	NextSlot uint64
	Reason   byte
}

func (e TwigsEnd) Encode() []byte {
	b := binary.LittleEndian.AppendUint64(make([]byte, 0, EndLen), e.NextTwig)
	b = binary.LittleEndian.AppendUint64(b, e.NextSlot)
	return append(b, e.Reason)
}

// DecodeTwigsEnd decodes a TwigsEnd.
func DecodeTwigsEnd(b []byte) (TwigsEnd, error) {
	if len(b) != EndLen {
		return TwigsEnd{}, ErrLength
	}
	e := TwigsEnd{NextTwig: binary.LittleEndian.Uint64(b[0:8]), NextSlot: binary.LittleEndian.Uint64(b[8:16]), Reason: b[16]}
	if e.Reason > maxReason {
		return e, fmt.Errorf("syncwire: unknown END reason %d", e.Reason)
	}
	return e, nil
}

// ---------------------------------------------------------------------------
// code_by_hash/1 (§5.3.5)

// EncodeCodeRequest encodes n u16 ‖ n × hash.
func EncodeCodeRequest(hashes []Hash) ([]byte, error) {
	if len(hashes) == 0 || len(hashes) > MaxCodeHashes {
		return nil, fmt.Errorf("syncwire: %d code hashes", len(hashes))
	}
	b := binary.LittleEndian.AppendUint16(make([]byte, 0, 2+32*len(hashes)), uint16(len(hashes)))
	for _, h := range hashes {
		b = append(b, h[:]...)
	}
	return b, nil
}

// MaxCodeRequest bounds the framed code_by_hash request.
const MaxCodeRequest = 2 + MaxCodeHashes*32

// DecodeCodeRequest decodes a code_by_hash request.
func DecodeCodeRequest(b []byte) ([]Hash, error) {
	if len(b) < 2 {
		return nil, ErrLength
	}
	n := int(binary.LittleEndian.Uint16(b[:2]))
	if n == 0 || n > MaxCodeHashes {
		return nil, fmt.Errorf("syncwire: %d code hashes outside 1..%d", n, MaxCodeHashes)
	}
	if len(b) != 2+32*n {
		return nil, ErrLength
	}
	out := make([]Hash, n)
	for i := range out {
		copy(out[i][:], b[2+32*i:])
	}
	return out, nil
}

// ---------------------------------------------------------------------------

type reader struct{ b []byte }

var errTruncated = errors.New("syncwire: truncated")

func (r *reader) take(n int) ([]byte, error) {
	if n < 0 || len(r.b) < n {
		return nil, errTruncated
	}
	out := r.b[:n:n]
	r.b = r.b[n:]
	return out, nil
}
func (r *reader) u8() (byte, error) {
	b, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}
func (r *reader) u16() (uint16, error) {
	b, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint16(b), nil
}
func (r *reader) u32() (uint32, error) {
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b), nil
}
func (r *reader) u64() (uint64, error) {
	b, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b), nil
}
func (r *reader) hash(h *Hash) error {
	b, err := r.take(32)
	if err != nil {
		return err
	}
	copy(h[:], b)
	return nil
}
func (r *reader) value() ([]byte, error) {
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	if n > MaxRecordValue {
		return nil, fmt.Errorf("syncwire: value of %d bytes", n)
	}
	return r.take(int(n))
}
func (r *reader) done() bool { return len(r.b) == 0 }
