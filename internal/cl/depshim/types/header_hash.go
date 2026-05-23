//go:build n42el

// Package types: Header.Hash production implementation.
//
// This file replaces the panic-stub in types.go with a real
// keccak256(RLP(header)) computation that matches erigon's
// execution/types.Header.EncodeRLP byte-for-byte. Mirrors the
// upstream field order + conditional encoding of optional
// fields (BaseFee, WithdrawalsHash, BlobGasUsed, ExcessBlobGas,
// ParentBeaconBlockRoot, RequestsHash, BlockAccessListHash,
// SlotNumber).
//
// Test guard: any divergence from erigon's encoding fails
// internal/cl/cltypes.TestBeaconBody, which asserts a specific
// HashSSZ root computed from a header containing only BaseFee=1.
package types

import (
	"io"

	"github.com/holiman/uint256"

	libcommon "github.com/n42blockchain/N42/lib/common"
	libcrypto "github.com/n42blockchain/N42/lib/crypto"
	rlp "github.com/n42blockchain/N42/lib/rlp"
)

// rlpHeader is the canonical RLP shape erigon emits, with explicit
// pointer fields so trailing optionals can be conditionally encoded
// (rlp tag `nilOK` would emit empty strings, breaking the hash).
//
// We don't use this struct directly with rlp.Encode — see
// encodeHeaderRLP for the manual layout that handles trailing
// optional inclusion exactly the way erigon does.

// keccakRLPHeader computes keccak256(RLP(header)) without any
// caching. Cheaper than maintaining a separate atomic.Pointer
// cache like erigon does — most call sites compute once per
// block import.
func keccakRLPHeader(h *Header) libcommon.Hash {
	// Compose a fresh byte buffer; the slice grows naturally for
	// the largest extension fields. 600 B comfortably fits even
	// post-Gloas headers.
	buf := make([]byte, 0, 600)
	w := writerOf(&buf)
	if err := encodeHeaderRLP(h, w); err != nil {
		// Any encoding error is a depshim/types bug — Headers
		// constructed by the cl/ tree have well-typed fields.
		panic("depshim/types: encodeHeaderRLP failed: " + err.Error())
	}
	return libcrypto.Keccak256Hash(buf)
}

// encodeHeaderRLP writes h to w using the same field order +
// conditional trailing-optional rules as erigon's
// execution/types.Header.EncodeRLP. The wire format must be
// identical for the keccak to match across the seam.
func encodeHeaderRLP(h *Header, w *bufWriter) error {
	// Compute the list payload size up front so we can write the
	// list prefix in one shot. RLP list prefix size depends on
	// the total payload length.
	size := headerEncodingSize(h)
	var prefix [10]byte
	pLen := rlp.EncodeListPrefix(size, prefix[:])
	if _, err := w.Write(prefix[:pLen]); err != nil {
		return err
	}
	var scratch [33]byte

	// 32-byte hash fields prefixed with 0xa0 (= 128 + 32).
	if err := writeFixed32(w, h.ParentHash[:]); err != nil {
		return err
	}
	if err := writeFixed32(w, h.UncleHash[:]); err != nil {
		return err
	}
	// 20-byte address prefixed with 0x94 (= 128 + 20).
	if err := writeFixed20(w, h.Coinbase[:]); err != nil {
		return err
	}
	if err := writeFixed32(w, h.Root[:]); err != nil {
		return err
	}
	if err := writeFixed32(w, h.TxHash[:]); err != nil {
		return err
	}
	if err := writeFixed32(w, h.ReceiptHash[:]); err != nil {
		return err
	}
	// 256-byte Bloom: prefix is 0xb9 0x01 0x00 (= 183+2, then
	// 0x0100 = 256).
	scratch[0] = 183 + 2
	scratch[1] = 1
	scratch[2] = 0
	if _, err := w.Write(scratch[:3]); err != nil {
		return err
	}
	if _, err := w.Write(h.Bloom[:]); err != nil {
		return err
	}
	if err := rlp.EncodeBigInt(&h.Difficulty, w, scratch[:]); err != nil {
		return err
	}
	if err := rlp.EncodeBigInt(&h.Number, w, scratch[:]); err != nil {
		return err
	}
	if err := rlp.EncodeInt(h.GasLimit, w, scratch[:]); err != nil {
		return err
	}
	if err := rlp.EncodeInt(h.GasUsed, w, scratch[:]); err != nil {
		return err
	}
	if err := rlp.EncodeInt(h.Time, w, scratch[:]); err != nil {
		return err
	}
	if err := rlp.EncodeString(h.Extra, w, scratch[:]); err != nil {
		return err
	}
	// MixDigest (32B) + Nonce (8B), non-AuRa path (we don't carry
	// AuRa state in the depshim Header).
	if err := writeFixed32(w, h.MixDigest[:]); err != nil {
		return err
	}
	if err := writeFixed8(w, h.Nonce[:]); err != nil {
		return err
	}
	// Trailing optionals (post-Merge through Gloas).
	if h.BaseFee != nil {
		bf := *h.BaseFee
		if err := encodeUint256(&bf, w, scratch[:]); err != nil {
			return err
		}
	}
	if h.WithdrawalsHash != nil {
		if err := writeFixed32(w, h.WithdrawalsHash[:]); err != nil {
			return err
		}
	}
	if h.BlobGasUsed != nil {
		if err := rlp.EncodeInt(*h.BlobGasUsed, w, scratch[:]); err != nil {
			return err
		}
	}
	if h.ExcessBlobGas != nil {
		if err := rlp.EncodeInt(*h.ExcessBlobGas, w, scratch[:]); err != nil {
			return err
		}
	}
	if h.ParentBeaconBlockRoot != nil {
		if err := writeFixed32(w, h.ParentBeaconBlockRoot[:]); err != nil {
			return err
		}
	}
	if h.RequestsHash != nil {
		if err := writeFixed32(w, h.RequestsHash[:]); err != nil {
			return err
		}
	}
	if h.BlockAccessListHash != nil {
		if err := writeFixed32(w, h.BlockAccessListHash[:]); err != nil {
			return err
		}
	}
	if h.SlotNumber != nil {
		if err := rlp.EncodeInt(*h.SlotNumber, w, scratch[:]); err != nil {
			return err
		}
	}
	return nil
}

// headerEncodingSize returns the byte length of the RLP-encoded
// header CONTENTS (excluding the outer list prefix).
func headerEncodingSize(h *Header) int {
	// Fixed prefix: ParentHash(33) + UncleHash(33) + Coinbase(21)
	//            + Root(33) + TxHash(33) + ReceiptHash(33)
	//            + Bloom(259) + MixDigest(33) + Nonce(9)
	size := 33 + 33 + 21 + 33 + 33 + 33 + 259 + 33 + 9
	size += rlp.BigIntLenExcludingHead(&h.Difficulty) + 1
	size += rlp.BigIntLenExcludingHead(&h.Number) + 1
	size += rlp.IntLenExcludingHead(h.GasLimit) + 1
	size += rlp.IntLenExcludingHead(h.GasUsed) + 1
	size += rlp.IntLenExcludingHead(h.Time) + 1
	size += rlp.StringLen(h.Extra)

	if h.BaseFee != nil {
		bf := *h.BaseFee
		size += uint256EncodingLen(&bf)
	}
	if h.WithdrawalsHash != nil {
		size += 33
	}
	if h.BlobGasUsed != nil {
		size += rlp.IntLenExcludingHead(*h.BlobGasUsed) + 1
	}
	if h.ExcessBlobGas != nil {
		size += rlp.IntLenExcludingHead(*h.ExcessBlobGas) + 1
	}
	if h.ParentBeaconBlockRoot != nil {
		size += 33
	}
	if h.RequestsHash != nil {
		size += 33
	}
	if h.BlockAccessListHash != nil {
		size += 33
	}
	if h.SlotNumber != nil {
		size += rlp.IntLenExcludingHead(*h.SlotNumber) + 1
	}
	return size
}

// --- tiny adapters between Erigon's encoder helpers and a []byte
//     writer.

type bufWriter struct{ p *[]byte }

func writerOf(p *[]byte) *bufWriter { return &bufWriter{p: p} }
func (w *bufWriter) Write(b []byte) (int, error) {
	*w.p = append(*w.p, b...)
	return len(b), nil
}

func writeFixed32(w io.Writer, src []byte) error {
	if _, err := w.Write([]byte{128 + 32}); err != nil {
		return err
	}
	_, err := w.Write(src)
	return err
}

func writeFixed20(w io.Writer, src []byte) error {
	if _, err := w.Write([]byte{128 + 20}); err != nil {
		return err
	}
	_, err := w.Write(src)
	return err
}

func writeFixed8(w io.Writer, src []byte) error {
	if _, err := w.Write([]byte{128 + 8}); err != nil {
		return err
	}
	_, err := w.Write(src)
	return err
}

// encodeUint256 + uint256EncodingLen: lib/rlp doesn't ship a
// uint256 encoder, but the wire format is identical to a positive
// big.Int. We bridge by converting to big.Int.
func encodeUint256(v *uint256.Int, w io.Writer, buf []byte) error {
	bi := v.ToBig()
	bw, ok := w.(*bufWriter)
	if !ok {
		// fallback path — copy through a temporary buffer.
		var tmp []byte
		tw := writerOf(&tmp)
		if err := rlp.EncodeBigInt(bi, tw, buf); err != nil {
			return err
		}
		_, err := w.Write(tmp)
		return err
	}
	return rlp.EncodeBigInt(bi, bw, buf)
}

func uint256EncodingLen(v *uint256.Int) int {
	return rlp.BigIntLenExcludingHead(v.ToBig()) + 1
}
