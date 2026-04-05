// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// receipt_codec.go — compact receipt encoding following the Reth Compact
// codec design (paradigmxyz/reth crates/codecs/src/alloy/receipt.rs).
//
// Strips bloom (256B), txHash, gasUsed, contractAddr, blockHash/Number,
// logIndex, and pre-Byzantium PostState (recoverable via re-execution).
//
// Flags byte layout (matches Reth):
//
//	bit 0-1:   txType identifier — 2 bits
//	             0 = Legacy, 1 = EIP-2930, 2 = EIP-1559
//	             3 = extended (actual type in next byte)
//	bit 2:     success — 0 = failed, 1 = successful
//	bit 3-6:   gasLen  — 4 bits (0-8 bytes of cumulativeGas)
//	bit 7:     reserved (0)
//
// Per receipt:
//
//	[flags: 1]
//	[txType: 1]                     — only if identifier == 3
//	[cumulativeGas: gasLen bytes, big-endian trimmed]
//	[logCount: varint]
//	per log:
//	  [address: 20]
//	  [topicCount: 1]
//	  [topics: 32*N]
//	  [dataLen: varint]
//	  [data: N]

package ethel

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// EncodeReceiptsCompact encodes a block's receipts in compact format.
func EncodeReceiptsCompact(receipts block.Receipts) []byte {
	if len(receipts) == 0 {
		return nil
	}
	buf := make([]byte, 0, len(receipts)*32)
	for _, r := range receipts {
		buf = encodeOneReceipt(buf, r)
	}
	return buf
}

func encodeOneReceipt(buf []byte, r *block.Receipt) []byte {
	var flags byte

	// bit 0-1: txType identifier (Reth scheme).
	txID := r.Type
	if txID >= 3 {
		txID = 3 // extended
	}
	flags |= txID & 0x03

	// bit 2: success.
	if r.Status == block.ReceiptStatusSuccessful {
		flags |= 0x04
	}

	// Cumulative gas: trim leading zeros.
	var gasBuf [8]byte
	binary.BigEndian.PutUint64(gasBuf[:], r.CumulativeGasUsed)
	gasStart := 0
	for gasStart < 8 && gasBuf[gasStart] == 0 {
		gasStart++
	}
	gasLen := 8 - gasStart // 0-8

	// bit 3-6: gasLen (4 bits, 0-8).
	flags |= byte(gasLen&0x0F) << 3

	buf = append(buf, flags)

	// Extended type byte (Reth: identifier==3 → write actual type).
	if txID == 3 {
		buf = append(buf, r.Type)
	}

	buf = append(buf, gasBuf[gasStart:]...)

	// Logs.
	buf = appendVarint(buf, uint64(len(r.Logs)))
	for _, l := range r.Logs {
		buf = append(buf, l.Address[:]...)
		buf = append(buf, byte(len(l.Topics)))
		for _, t := range l.Topics {
			buf = append(buf, t[:]...)
		}
		buf = appendVarint(buf, uint64(len(l.Data)))
		buf = append(buf, l.Data...)
	}
	return buf
}

// DecodeReceiptsCompact decodes compact-encoded receipts.
func DecodeReceiptsCompact(data []byte) (block.Receipts, error) {
	if len(data) == 0 {
		return nil, nil
	}
	var receipts block.Receipts
	pos := 0
	for pos < len(data) {
		r, newPos, err := decodeOneReceipt(data, pos)
		if err != nil {
			return nil, fmt.Errorf("receipt %d: %w", len(receipts), err)
		}
		receipts = append(receipts, r)
		pos = newPos
	}
	return receipts, nil
}

func decodeOneReceipt(data []byte, pos int) (*block.Receipt, int, error) {
	if pos >= len(data) {
		return nil, pos, fmt.Errorf("truncated flags")
	}
	flags := data[pos]
	pos++

	r := &block.Receipt{}

	// bit 0-1: txType identifier.
	txID := flags & 0x03
	if txID < 3 {
		r.Type = txID
	} else {
		// Extended: read actual type byte.
		if pos >= len(data) {
			return nil, pos, fmt.Errorf("truncated extended txType")
		}
		r.Type = data[pos]
		pos++
	}

	// bit 2: success.
	if flags&0x04 != 0 {
		r.Status = block.ReceiptStatusSuccessful
	} else {
		r.Status = block.ReceiptStatusFailed
	}

	// bit 3-6: gasLen (0-8).
	gasLen := int((flags >> 3) & 0x0F)
	if gasLen > 8 {
		return nil, pos, fmt.Errorf("invalid gasLen %d", gasLen)
	}

	if pos+gasLen > len(data) {
		return nil, pos, fmt.Errorf("truncated gas")
	}
	if gasLen > 0 {
		var gasBuf [8]byte
		copy(gasBuf[8-gasLen:], data[pos:pos+gasLen])
		r.CumulativeGasUsed = binary.BigEndian.Uint64(gasBuf[:])
		pos += gasLen
	}

	// Logs.
	logCount, n := readVarint(data[pos:])
	if n <= 0 {
		return nil, pos, fmt.Errorf("bad log count varint")
	}
	pos += n

	r.Logs = make([]*block.Log, logCount)
	for i := uint64(0); i < logCount; i++ {
		l := &block.Log{}
		if pos+20 > len(data) {
			return nil, pos, fmt.Errorf("truncated log address")
		}
		copy(l.Address[:], data[pos:pos+20])
		pos += 20

		if pos >= len(data) {
			return nil, pos, fmt.Errorf("truncated topic count")
		}
		topicCount := int(data[pos])
		pos++

		l.Topics = make([]types.Hash, topicCount)
		for j := 0; j < topicCount; j++ {
			if pos+32 > len(data) {
				return nil, pos, fmt.Errorf("truncated topic")
			}
			copy(l.Topics[j][:], data[pos:pos+32])
			pos += 32
		}

		dataLen, n := readVarint(data[pos:])
		if n <= 0 {
			return nil, pos, fmt.Errorf("bad data len varint")
		}
		pos += n
		if pos+int(dataLen) > len(data) {
			return nil, pos, fmt.Errorf("truncated log data")
		}
		l.Data = make([]byte, dataLen)
		copy(l.Data, data[pos:pos+int(dataLen)])
		pos += int(dataLen)

		r.Logs[i] = l
	}
	return r, pos, nil
}

// appendVarint appends a variable-length unsigned integer.
func appendVarint(buf []byte, v uint64) []byte {
	var tmp [10]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(buf, tmp[:n]...)
}

// readVarint reads a variable-length unsigned integer.
func readVarint(data []byte) (uint64, int) {
	return binary.Uvarint(data)
}
