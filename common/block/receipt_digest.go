// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// One-pass receipts digest. A block write needs three things from its
// receipts: the native receipts root (DeriveSha over Receipts.EncodeIndex),
// the block bloom (CreateBloom) and the compact storage row
// (MarshalCompact). Computed separately they walk the receipt list three
// times; at 163k receipts of ~440 B each the walks are cache-miss bound, and
// the reflection RLP encoder allocated a storedReceipt per receipt on top.
// ReceiptsDigest reads each receipt once and emits all three, using a
// hand-written RLP encoder that is byte-identical to rlp.Encode of
// storedReceipt.

package block

import (
	"encoding/binary"
	"math/bits"
	"sync"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// rlpUintLen is the encoded size of an RLP unsigned integer.
func rlpUintLen(v uint64) int {
	if v < 0x80 {
		return 1
	}
	return 1 + (bits.Len64(v)+7)/8
}

func rlpAppendUint(b []byte, v uint64) []byte {
	switch {
	case v == 0:
		return append(b, 0x80)
	case v < 0x80:
		return append(b, byte(v))
	}
	n := (bits.Len64(v) + 7) / 8
	b = append(b, 0x80+byte(n))
	for i := n - 1; i >= 0; i-- {
		b = append(b, byte(v>>(8*uint(i))))
	}
	return b
}

// rlpHeaderLen is the size of a string or list header for a payload of n.
func rlpHeaderLen(n int) int {
	if n < 56 {
		return 1
	}
	return 1 + (bits.Len64(uint64(n))+7)/8
}

// rlpAppendHeader appends a string (base 0x80) or list (base 0xc0) header.
func rlpAppendHeader(b []byte, base byte, n int) []byte {
	if n < 56 {
		return append(b, base+byte(n))
	}
	ln := (bits.Len64(uint64(n)) + 7) / 8
	b = append(b, base+55+byte(ln))
	for i := ln - 1; i >= 0; i-- {
		b = append(b, byte(uint64(n)>>(8*uint(i))))
	}
	return b
}

func rlpStringLen(s []byte) int {
	if len(s) == 1 && s[0] < 0x80 {
		return 1
	}
	return rlpHeaderLen(len(s)) + len(s)
}

func rlpAppendString(b []byte, s []byte) []byte {
	if len(s) == 1 && s[0] < 0x80 {
		return append(b, s[0])
	}
	b = rlpAppendHeader(b, 0x80, len(s))
	return append(b, s...)
}

// nativeLogPayloadLen is the list payload size of one storedLog.
func nativeLogPayloadLen(l *Log) int {
	topics := len(l.Topics) * (1 + types.HashLength)
	return 1 + types.AddressLength + rlpHeaderLen(topics) + topics + rlpStringLen(l.Data)
}

// appendNativeReceipt appends the N42 native receipt encoding of r, the
// bytes Receipts.EncodeIndex writes: RLP [Status, CumulativeGasUsed,
// [[Address, [Topics...], Data]...]].
func appendNativeReceipt(b []byte, r *Receipt) []byte {
	logsPayload := 0
	for _, l := range r.Logs {
		p := nativeLogPayloadLen(l)
		logsPayload += rlpHeaderLen(p) + p
	}
	payload := rlpUintLen(r.Status) + rlpUintLen(r.CumulativeGasUsed) + rlpHeaderLen(logsPayload) + logsPayload
	b = rlpAppendHeader(b, 0xc0, payload)
	b = rlpAppendUint(b, r.Status)
	b = rlpAppendUint(b, r.CumulativeGasUsed)
	b = rlpAppendHeader(b, 0xc0, logsPayload)
	for _, l := range r.Logs {
		b = rlpAppendHeader(b, 0xc0, nativeLogPayloadLen(l))
		b = append(b, 0x80+types.AddressLength)
		b = append(b, l.Address[:]...)
		topics := len(l.Topics) * (1 + types.HashLength)
		b = rlpAppendHeader(b, 0xc0, topics)
		for i := range l.Topics {
			b = append(b, 0x80+types.HashLength)
			b = append(b, l.Topics[i][:]...)
		}
		b = rlpAppendString(b, l.Data)
	}
	return b
}

// appendCompactReceipt appends one receipt's compact storage record (see
// receipt_compact.go for the layout).
func appendCompactReceipt(out []byte, r *Receipt) []byte {
	var flags byte
	if len(r.PostState) == 32 {
		flags |= rcfPostState
	}
	hasContract := r.ContractAddress != (types.Address{})
	if hasContract {
		flags |= rcfContract
	}
	out = append(out, r.Type, flags)
	if flags&rcfPostState != 0 {
		out = append(out, r.PostState...)
	} else {
		out = binary.AppendUvarint(out, r.Status)
	}
	if hasContract {
		out = append(out, r.ContractAddress[:]...)
	}
	out = binary.AppendUvarint(out, r.CumulativeGasUsed)
	out = binary.AppendUvarint(out, uint64(len(r.Logs)))
	for _, lg := range r.Logs {
		out = append(out, lg.Address[:]...)
		out = binary.AppendUvarint(out, uint64(len(lg.Topics)))
		for _, tp := range lg.Topics {
			out = append(out, tp[:]...)
		}
		out = binary.AppendUvarint(out, uint64(len(lg.Data)))
		out = append(out, lg.Data...)
	}
	return out
}

// nativeRootFlush is how much native encoding accumulates before it is
// absorbed by the hasher.
const nativeRootFlush = 32 << 10

// ReceiptsDigest returns, from one pass over rs: the native receipts root
// (equal to hash.DeriveSha(rs)) when nativeRoot, the block bloom (equal to
// CreateBloom(rs)), and the compact storage row (equal to rs.MarshalCompact())
// when compact. Skipped outputs are left zero / nil.
func ReceiptsDigest(rs Receipts, nativeRoot, compact bool) (root types.Hash, bloom Bloom, enc []byte) {
	h := bloomHasherPool.Get().(*bloomHasher)
	defer bloomHasherPool.Put(h)

	var sha crypto.KeccakState
	var buf []byte
	var bp *[]byte
	if nativeRoot {
		if len(rs) == 0 {
			root = hash.NilHash
		} else {
			sha = hash.HasherPool.Get().(crypto.KeccakState)
			defer hash.HasherPool.Put(sha)
			sha.Reset()
			bp = nativeBufPool.Get().(*[]byte)
			defer nativeBufPool.Put(bp)
			buf = (*bp)[:0]
		}
	}
	if compact {
		enc = make([]byte, 0, 2+binary.MaxVarintLen64+16*len(rs))
		enc = append(enc, compactReceiptsMarker, compactReceiptsVersion)
		enc = binary.AppendUvarint(enc, uint64(len(rs)))
	}
	for _, r := range rs {
		if len(r.Logs) != 0 {
			bloom.addLogs(r.Logs, h)
		}
		if compact {
			enc = appendCompactReceipt(enc, r)
		}
		if sha != nil {
			buf = appendNativeReceipt(buf, r)
			if len(buf) >= nativeRootFlush {
				sha.Write(buf) //nolint:errcheck
				buf = buf[:0]
			}
		}
	}
	if sha != nil {
		sha.Write(buf) //nolint:errcheck
		sha.Read(root[:])
		*bp = buf[:0]
	}
	return root, bloom, enc
}

var nativeBufPool = sync.Pool{New: func() any {
	b := make([]byte, 0, nativeRootFlush+4<<10)
	return &b
}}
