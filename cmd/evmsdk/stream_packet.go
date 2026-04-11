// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// V2 stream packet codec for mobile verification data.
// Declares StreamPacket (BlockHash plus ordered read log, tx list
// and bytecode bundle) and decoder safety caps MaxTxCount /
// MaxBytecodeCount. ErrPacketTruncated / ErrTxCountExceeded /
// ErrBytecodeExceeded report malformed or oversized inputs before
// any allocation.

package evmsdk

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// Safety limits to prevent excessive memory allocation during decoding.
const (
	MaxTxCount       = 100000
	MaxBytecodeCount = 10000
)

var (
	ErrPacketTruncated    = errors.New("stream_packet: data truncated")
	ErrTxCountExceeded    = errors.New("stream_packet: transaction count exceeds limit")
	ErrBytecodeExceeded   = errors.New("stream_packet: bytecode count exceeds limit")
)

// StreamPacket is the V2 verification data format.
// More compact than V1 -- uses ordered read log instead of keyed witness accounts.
type StreamPacket struct {
	BlockHash    types.Hash
	HeaderRLP    []byte      // Protobuf-encoded header (named RLP for wire format convention)
	Transactions [][]byte    // Raw transaction bytes
	ReadLogData  []byte      // Pre-encoded read log
	Bytecodes    []CodeEntry // Contract bytecodes (code_hash -> bytes)
}

// CodeEntry pairs a code hash with its bytecode.
type CodeEntry struct {
	Hash types.Hash
	Code []byte
}

// EncodeStreamPacket serializes a StreamPacket with V2 wire header.
//
// Wire format (little-endian lengths):
//
//	wire_header(4B)
//	+ block_hash(32B)
//	+ header_rlp_len(4B LE) + header_rlp
//	+ tx_count(4B LE) + [tx_len(4B LE) + tx_bytes]...
//	+ read_log_len(4B LE) + read_log_bytes
//	+ bytecode_count(4B LE) + [hash(32B) + code_len(4B LE) + code_bytes]...
func EncodeStreamPacket(pkt *StreamPacket) ([]byte, error) {
	if pkt == nil {
		return nil, errors.New("stream_packet: nil packet")
	}

	// Determine flags.
	var flags uint8
	if len(pkt.Bytecodes) > 0 {
		flags |= FlagHasBytecodes
	}

	// Pre-calculate total size.
	size := pkt.EstimatedSize()
	buf := make([]byte, 0, size)

	// Wire header.
	buf = append(buf, EncodeWireHeader(WireVersion1, flags)...)

	// Block hash (32 bytes).
	buf = append(buf, pkt.BlockHash[:]...)

	// Header RLP: length prefix + data.
	buf = appendU32LE(buf, uint32(len(pkt.HeaderRLP)))
	buf = append(buf, pkt.HeaderRLP...)

	// Transactions: count + [len + data]...
	buf = appendU32LE(buf, uint32(len(pkt.Transactions)))
	for _, tx := range pkt.Transactions {
		buf = appendU32LE(buf, uint32(len(tx)))
		buf = append(buf, tx...)
	}

	// Read log: length prefix + data.
	buf = appendU32LE(buf, uint32(len(pkt.ReadLogData)))
	buf = append(buf, pkt.ReadLogData...)

	// Bytecodes: count + [hash(32) + len + data]...
	buf = appendU32LE(buf, uint32(len(pkt.Bytecodes)))
	for _, entry := range pkt.Bytecodes {
		buf = append(buf, entry.Hash[:]...)
		buf = appendU32LE(buf, uint32(len(entry.Code)))
		buf = append(buf, entry.Code...)
	}

	return buf, nil
}

// DecodeStreamPacket deserializes a V2 stream packet from wire format.
func DecodeStreamPacket(data []byte) (*StreamPacket, error) {
	// Decode and validate wire header.
	hdr, payload, err := DecodeWireHeader(data)
	if err != nil {
		return nil, fmt.Errorf("stream_packet: %w", err)
	}

	r := &reader{data: payload}

	// Block hash (32 bytes).
	var pkt StreamPacket
	hashBytes, err := r.readN(types.HashLength)
	if err != nil {
		return nil, fmt.Errorf("stream_packet: reading block hash: %w", ErrPacketTruncated)
	}
	copy(pkt.BlockHash[:], hashBytes)

	// Header RLP.
	pkt.HeaderRLP, err = r.readLenPrefixed()
	if err != nil {
		return nil, fmt.Errorf("stream_packet: reading header: %w", ErrPacketTruncated)
	}

	// Transactions.
	txCount, err := r.readU32LE()
	if err != nil {
		return nil, fmt.Errorf("stream_packet: reading tx count: %w", ErrPacketTruncated)
	}
	if txCount > MaxTxCount {
		return nil, fmt.Errorf("%w: %d", ErrTxCountExceeded, txCount)
	}
	pkt.Transactions = make([][]byte, 0, txCount)
	for i := uint32(0); i < txCount; i++ {
		tx, err := r.readLenPrefixed()
		if err != nil {
			return nil, fmt.Errorf("stream_packet: reading tx %d: %w", i, ErrPacketTruncated)
		}
		pkt.Transactions = append(pkt.Transactions, tx)
	}

	// Read log.
	pkt.ReadLogData, err = r.readLenPrefixed()
	if err != nil {
		return nil, fmt.Errorf("stream_packet: reading read log: %w", ErrPacketTruncated)
	}

	// Bytecodes (only if flagged or if data remains).
	bcCount, err := r.readU32LE()
	if err != nil {
		// If no bytecodes flag and we're at end of data, this is acceptable.
		if hdr.HasBytecodes() {
			return nil, fmt.Errorf("stream_packet: reading bytecode count: %w", ErrPacketTruncated)
		}
		// No bytecodes section -- return packet as-is.
		return &pkt, nil
	}
	if bcCount > MaxBytecodeCount {
		return nil, fmt.Errorf("%w: %d", ErrBytecodeExceeded, bcCount)
	}
	pkt.Bytecodes = make([]CodeEntry, 0, bcCount)
	for i := uint32(0); i < bcCount; i++ {
		var entry CodeEntry
		hashBytes, err := r.readN(types.HashLength)
		if err != nil {
			return nil, fmt.Errorf("stream_packet: reading bytecode hash %d: %w", i, ErrPacketTruncated)
		}
		copy(entry.Hash[:], hashBytes)
		entry.Code, err = r.readLenPrefixed()
		if err != nil {
			return nil, fmt.Errorf("stream_packet: reading bytecode %d: %w", i, ErrPacketTruncated)
		}
		pkt.Bytecodes = append(pkt.Bytecodes, entry)
	}

	return &pkt, nil
}

// HeaderInfo extracts block number from the header data.
// The header is protobuf-encoded (block.Header).
// Returns (blockNumber, error).
func (p *StreamPacket) HeaderInfo() (uint64, error) {
	if len(p.HeaderRLP) == 0 {
		return 0, errors.New("stream_packet: empty header data")
	}

	var hdr block.Header
	if err := hdr.Unmarshal(p.HeaderRLP); err != nil {
		return 0, fmt.Errorf("stream_packet: unmarshal header: %w", err)
	}

	if hdr.Number == nil {
		return 0, errors.New("stream_packet: header has nil block number")
	}
	return hdr.Number.Uint64(), nil
}

// EstimatedSize returns the approximate wire size in bytes.
func (p *StreamPacket) EstimatedSize() int {
	size := WireHeaderSize        // wire header
	size += types.HashLength      // block hash
	size += 4 + len(p.HeaderRLP)  // header length prefix + data
	size += 4                     // tx count
	for _, tx := range p.Transactions {
		size += 4 + len(tx) // tx length prefix + data
	}
	size += 4 + len(p.ReadLogData) // read log length prefix + data
	size += 4                      // bytecode count
	for _, entry := range p.Bytecodes {
		size += types.HashLength   // code hash
		size += 4 + len(entry.Code) // code length prefix + data
	}
	return size
}

// --- internal helpers ---

func appendU32LE(buf []byte, v uint32) []byte {
	return binary.LittleEndian.AppendUint32(buf, v)
}

// reader is a simple cursor over a byte slice for sequential reads.
type reader struct {
	data []byte
	pos  int
}

func (r *reader) readN(n int) ([]byte, error) {
	if r.pos+n > len(r.data) {
		return nil, ErrPacketTruncated
	}
	out := make([]byte, n)
	copy(out, r.data[r.pos:r.pos+n])
	r.pos += n
	return out, nil
}

func (r *reader) readU32LE() (uint32, error) {
	if r.pos+4 > len(r.data) {
		return 0, ErrPacketTruncated
	}
	v := binary.LittleEndian.Uint32(r.data[r.pos : r.pos+4])
	r.pos += 4
	return v, nil
}

func (r *reader) readLenPrefixed() ([]byte, error) {
	length, err := r.readU32LE()
	if err != nil {
		return nil, err
	}
	// Guard against uint32-to-int overflow on 32-bit platforms (mobile SDK).
	if length > uint32(len(r.data)) {
		return nil, ErrPacketTruncated
	}
	return r.readN(int(length))
}
