// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// receipt_hash.go — Ethereum-compatible receipts-root computation.
//
// ethReceiptList adapts a slice of block.Receipt to the DeriveSha list
// interface. EncodeIndex writes the canonical post-Byzantium RLP layout
// (type byte for EIP-2718 plus Status, CumulativeGasUsed, Bloom, Logs)
// and uses the bloom produced by transaction execution. A compatibility
// fallback recomputes it only for callers that supply compactly decoded
// receipts, whose storage codec strips Bloom. EthReceiptHash feeds the list
// through DeriveShaErigon so the resulting root matches live mainnet headers
// bit-for-bit.

package ethel

import (
	"bytes"
	"encoding/binary"
	"math/bits"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

// ethReceiptList wraps receipts for Ethereum-compatible DeriveShaETH.
type ethReceiptList []*block.Receipt

func (rs ethReceiptList) Len() int { return len(rs) }

// ethRLPLog is retained as the reflection-based reference shape used by the
// canonical-encoding regression tests.
type ethRLPLog struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
}

// EncodeIndex writes the Ethereum-compatible RLP encoding of receipt i.
// Post-Byzantium: RLP([Status, CumulativeGasUsed, Bloom, Logs])
// EIP-2718:       type || RLP([Status, CumulativeGasUsed, Bloom, Logs])
//
// Bloom is recomputed from logs because the on-disk ReceiptForStorage format
// (used by both Geth ancient and our compact storage) does NOT persist Bloom —
// it is a deterministic function of the logs.
func (rs ethReceiptList) EncodeIndex(i int, w *bytes.Buffer) {
	r := rs[i]

	if r.Type > 0 {
		w.WriteByte(r.Type)
	}

	bloom := r.Bloom
	if bloom == (block.Bloom{}) && len(r.Logs) > 0 {
		bloom = block.CreateBloom(block.Receipts{r})
	}

	var logsContent uint64
	for _, log := range r.Logs {
		logsContent += ethLogRLPSize(log)
	}
	receiptContent := uint64(rlp.IntSize(r.Status)+rlp.IntSize(r.CumulativeGasUsed)) +
		rlp.BytesSize(bloom[:]) + rlp.ListSize(logsContent)

	// The exact size is known, so append directly into bytes.Buffer's spare
	// capacity and commit it with one Write. This avoids the reflection walk,
	// temporary []ethRLPLog and encbuf copy previously paid for every receipt.
	w.Grow(int(rlp.ListSize(receiptContent)))
	out := w.AvailableBuffer()
	out = appendRLPListPrefix(out, receiptContent)
	out = rlp.AppendUint64(out, r.Status)
	out = rlp.AppendUint64(out, r.CumulativeGasUsed)
	out = appendRLPString(out, bloom[:])
	out = appendRLPListPrefix(out, logsContent)
	for _, log := range r.Logs {
		var topicsContent = uint64(len(log.Topics)) * 33 // 0xa0 + 32 bytes
		logContent := uint64(21) + rlp.ListSize(topicsContent) + rlp.BytesSize(log.Data)
		out = appendRLPListPrefix(out, logContent)
		out = appendRLPString(out, log.Address[:])
		out = appendRLPListPrefix(out, topicsContent)
		for _, topic := range log.Topics {
			out = appendRLPString(out, topic[:])
		}
		out = appendRLPString(out, log.Data)
	}
	_, _ = w.Write(out)
}

func ethLogRLPSize(log *block.Log) uint64 {
	topicsContent := uint64(len(log.Topics)) * 33 // 0xa0 + 32 bytes
	content := uint64(21) + rlp.ListSize(topicsContent) + rlp.BytesSize(log.Data)
	return rlp.ListSize(content)
}

func appendRLPString(dst, value []byte) []byte {
	if len(value) == 1 && value[0] <= 0x7f {
		return append(dst, value[0])
	}
	dst = appendRLPSize(dst, 0x80, 0xb7, uint64(len(value)))
	return append(dst, value...)
}

func appendRLPListPrefix(dst []byte, contentSize uint64) []byte {
	return appendRLPSize(dst, 0xc0, 0xf7, contentSize)
}

func appendRLPSize(dst []byte, shortBase, longBase byte, size uint64) []byte {
	if size < 56 {
		return append(dst, shortBase+byte(size))
	}
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], size)
	n := (bits.Len64(size) + 7) / 8
	dst = append(dst, longBase+byte(n))
	return append(dst, encoded[8-n:]...)
}

// EthReceiptHash computes the Ethereum-standard receipt root hash
// using the canonical MPT (erigon HashBuilder backend).
func EthReceiptHash(receipts []*block.Receipt) types.Hash {
	return hash.DeriveShaErigon(ethReceiptList(receipts))
}
