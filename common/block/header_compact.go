// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Compact (V2-style) header STORAGE codec. The proto Marshal stores every field
// in full — ~775 B/header including a 256-byte all-zero Bloom on empty blocks
// and well-known constant hashes spelled out — which made the Headers table the
// single largest item of a converted chain (9.8 GB of a 38 GB target at 12.7M
// blocks). This codec applies the same idea as eth-el's columnar headers and
// Erigon's V2 account encoding, but row-oriented so MDBX point reads keep
// working: a field-presence bitmask plus only the non-default field payloads.
// Defaults omitted: zero Bloom/Coinbase/MixDigest/Nonce/GasUsed/Difficulty,
// empty-list TxHash/ReceiptHash (EmptyRootHash), EmptyUncleHash, nil optionals,
// the all-zero 32-byte vanity Extra, and constant WithdrawalsHash/RequestsHash.
//
// Wire layout:
//
//	[0]   0xFF       — format marker. A valid protobuf message can never begin
//	                   with 0xFF (it decodes as field 31 / wire type 7, which is
//	                   illegal), so Header.Unmarshal dispatches on it safely.
//	[1]   0x01       — codec version
//	[2:5] flags      — uint24 LE presence bitmask (hcf* bits below)
//	then, in bit order, only the present payloads.
//
// This is a STORAGE encoding only: the P2P wire format stays proto, and the
// header hash is RLP-derived, so the stored encoding never affects consensus.
// Header.Unmarshal accepts both formats, so tables may mix freely (old chains,
// resumed conversions, nodes appending proto headers to a compact chain).

package block

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

const (
	compactHeaderMarker  = 0xFF
	compactHeaderVersion = 0x01
)

// presence bits (uint24, little-endian on the wire)
const (
	hcfCoinbase         = 1 << 0  // Coinbase != zero address (20 B)
	hcfUncleHash        = 1 << 1  // UncleHash != EmptyUncleHash (32 B)
	hcfTxHash           = 1 << 2  // TxHash != EmptyRootHash (32 B)
	hcfReceiptHash      = 1 << 3  // ReceiptHash != EmptyRootHash (32 B)
	hcfBloom            = 1 << 4  // Bloom != zero (256 B)
	hcfDifficulty       = 1 << 5  // Difficulty != nil && != 0 (1B len + minimal BE bytes)
	hcfGasUsed          = 1 << 6  // GasUsed != 0 (uvarint)
	hcfExtra            = 1 << 7  // len(Extra) > 0 and not the 32-zero vanity (uvarint len + bytes)
	hcfExtra32Zero      = 1 << 8  // Extra is exactly 32 zero bytes (no payload)
	hcfMixDigest        = 1 << 9  // MixDigest != zero (32 B)
	hcfNonce            = 1 << 10 // Nonce != 0 (8 B)
	hcfBaseFee          = 1 << 11 // BaseFee != nil (1B len + minimal BE bytes; len 0 == zero)
	hcfWithdrawalsHash  = 1 << 12 // WithdrawalsHash != nil and not the empty-list constant (32 B)
	hcfWithdrawalsEmpty = 1 << 13 // WithdrawalsHash != nil and == DeriveSha(empty) constant (no payload)
	hcfBlobGasUsed      = 1 << 14 // BlobGasUsed != nil (uvarint)
	hcfExcessBlobGas    = 1 << 15 // ExcessBlobGas != nil (uvarint)
	hcfParentBeaconRoot = 1 << 16 // ParentBeaconRoot != nil (32 B)
	hcfRequestsHash     = 1 << 17 // RequestsHash != nil and != EmptyRootHash (32 B)
	hcfRequestsEmpty    = 1 << 18 // RequestsHash != nil and == EmptyRootHash (no payload)
	// The compact codec must carry EVERY consensus header field, or the stored
	// header's hash diverges from the RLP-derived head hash and block import
	// fails with "unknown ancestor". These two trailing optionals were added to
	// the proto/trailer Marshal but originally missed here: BlockAccessListHash
	// (EIP-7928, dormant everywhere) never bit, but MobileRegistryRoot broke
	// solo production the moment the MobileAnchor fork stamped it.
	hcfBlockAccessListHash = 1 << 19 // BlockAccessListHash != nil (32 B)
	hcfMobileRegistryRoot  = 1 << 20 // MobileRegistryRoot != nil (32 B)
)

// emptyListRoot is DeriveSha of an empty list — the value of TxHash/ReceiptHash/
// WithdrawalsHash/RequestsHash on blocks with nothing in them (keccak256(rlp([]))).
var emptyListRoot = hash.EmptyRootHash

func putUvarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}

// MarshalCompact encodes the header in the compact storage format.
func (h *Header) MarshalCompact() []byte {
	var flags uint32
	// Conservative capacity: fixed part + every optional present.
	out := make([]byte, 0, 5+32+32+5*binary.MaxVarintLen64+20+32*7+256+8+2*33)
	out = append(out, compactHeaderMarker, compactHeaderVersion, 0, 0, 0)

	// Fixed fields (always present).
	out = append(out, h.ParentHash[:]...)
	out = append(out, h.Root[:]...)
	var num uint64
	if h.Number != nil {
		num = h.Number.Uint64()
	}
	out = putUvarint(out, num)
	out = putUvarint(out, h.GasLimit)
	out = putUvarint(out, h.Time)

	// Optional fields, in bit order.
	if h.Coinbase != (types.Address{}) {
		flags |= hcfCoinbase
		out = append(out, h.Coinbase[:]...)
	}
	if h.UncleHash != hash.EmptyUncleHash {
		flags |= hcfUncleHash
		out = append(out, h.UncleHash[:]...)
	}
	if h.TxHash != emptyListRoot {
		flags |= hcfTxHash
		out = append(out, h.TxHash[:]...)
	}
	if h.ReceiptHash != emptyListRoot {
		flags |= hcfReceiptHash
		out = append(out, h.ReceiptHash[:]...)
	}
	if h.Bloom != (Bloom{}) {
		flags |= hcfBloom
		out = append(out, h.Bloom[:]...)
	}
	if h.Difficulty != nil && !h.Difficulty.IsZero() {
		flags |= hcfDifficulty
		b := h.Difficulty.Bytes()
		out = append(out, byte(len(b)))
		out = append(out, b...)
	}
	if h.GasUsed != 0 {
		flags |= hcfGasUsed
		out = putUvarint(out, h.GasUsed)
	}
	if len(h.Extra) > 0 {
		if isZero32(h.Extra) {
			flags |= hcfExtra32Zero
		} else {
			flags |= hcfExtra
			out = putUvarint(out, uint64(len(h.Extra)))
			out = append(out, h.Extra...)
		}
	}
	if h.MixDigest != (types.Hash{}) {
		flags |= hcfMixDigest
		out = append(out, h.MixDigest[:]...)
	}
	if h.Nonce != (BlockNonce{}) {
		flags |= hcfNonce
		out = append(out, h.Nonce[:]...)
	}
	if h.BaseFee != nil {
		flags |= hcfBaseFee
		b := h.BaseFee.Bytes()
		out = append(out, byte(len(b)))
		out = append(out, b...)
	}
	if h.WithdrawalsHash != nil {
		if *h.WithdrawalsHash == emptyListRoot {
			flags |= hcfWithdrawalsEmpty
		} else {
			flags |= hcfWithdrawalsHash
			out = append(out, h.WithdrawalsHash[:]...)
		}
	}
	if h.BlobGasUsed != nil {
		flags |= hcfBlobGasUsed
		out = putUvarint(out, *h.BlobGasUsed)
	}
	if h.ExcessBlobGas != nil {
		flags |= hcfExcessBlobGas
		out = putUvarint(out, *h.ExcessBlobGas)
	}
	if h.ParentBeaconRoot != nil {
		flags |= hcfParentBeaconRoot
		out = append(out, h.ParentBeaconRoot[:]...)
	}
	if h.RequestsHash != nil {
		if *h.RequestsHash == emptyListRoot {
			flags |= hcfRequestsEmpty
		} else {
			flags |= hcfRequestsHash
			out = append(out, h.RequestsHash[:]...)
		}
	}
	if h.BlockAccessListHash != nil {
		flags |= hcfBlockAccessListHash
		out = append(out, h.BlockAccessListHash[:]...)
	}
	if h.MobileRegistryRoot != nil {
		flags |= hcfMobileRegistryRoot
		out = append(out, h.MobileRegistryRoot[:]...)
	}

	out[2] = byte(flags)
	out[3] = byte(flags >> 8)
	out[4] = byte(flags >> 16)
	return out
}

// IsCompactHeader reports whether data is in the compact storage format.
func IsCompactHeader(data []byte) bool {
	return len(data) >= 5 && data[0] == compactHeaderMarker && data[1] == compactHeaderVersion
}

// unmarshalCompact decodes the compact storage format into h (full reset).
func (h *Header) unmarshalCompact(data []byte) error {
	if !IsCompactHeader(data) {
		return fmt.Errorf("not a compact header")
	}
	flags := uint32(data[2]) | uint32(data[3])<<8 | uint32(data[4])<<16
	p := data[5:]

	take := func(n int) ([]byte, error) {
		if len(p) < n {
			return nil, fmt.Errorf("compact header truncated: need %d, have %d", n, len(p))
		}
		b := p[:n]
		p = p[n:]
		return b, nil
	}
	uvarint := func() (uint64, error) {
		v, n := binary.Uvarint(p)
		if n <= 0 {
			return 0, fmt.Errorf("compact header: bad uvarint")
		}
		p = p[n:]
		return v, nil
	}

	*h = Header{} // full reset, defaults below

	b, err := take(32)
	if err != nil {
		return err
	}
	copy(h.ParentHash[:], b)
	if b, err = take(32); err != nil {
		return err
	}
	copy(h.Root[:], b)
	num, err := uvarint()
	if err != nil {
		return err
	}
	h.Number = uint256.NewInt(num)
	if h.GasLimit, err = uvarint(); err != nil {
		return err
	}
	if h.Time, err = uvarint(); err != nil {
		return err
	}

	// Defaults for omitted fields.
	h.UncleHash = hash.EmptyUncleHash
	h.TxHash = emptyListRoot
	h.ReceiptHash = emptyListRoot
	h.Difficulty = uint256.NewInt(0)

	if flags&hcfCoinbase != 0 {
		if b, err = take(20); err != nil {
			return err
		}
		copy(h.Coinbase[:], b)
	}
	if flags&hcfUncleHash != 0 {
		if b, err = take(32); err != nil {
			return err
		}
		copy(h.UncleHash[:], b)
	}
	if flags&hcfTxHash != 0 {
		if b, err = take(32); err != nil {
			return err
		}
		copy(h.TxHash[:], b)
	}
	if flags&hcfReceiptHash != 0 {
		if b, err = take(32); err != nil {
			return err
		}
		copy(h.ReceiptHash[:], b)
	}
	if flags&hcfBloom != 0 {
		if b, err = take(BloomByteLength); err != nil {
			return err
		}
		copy(h.Bloom[:], b)
	}
	if flags&hcfDifficulty != 0 {
		if b, err = take(1); err != nil {
			return err
		}
		n := int(b[0])
		if b, err = take(n); err != nil {
			return err
		}
		h.Difficulty = new(uint256.Int).SetBytes(b)
	}
	if flags&hcfGasUsed != 0 {
		if h.GasUsed, err = uvarint(); err != nil {
			return err
		}
	}
	if flags&hcfExtra != 0 {
		n, e := uvarint()
		if e != nil {
			return e
		}
		if b, err = take(int(n)); err != nil {
			return err
		}
		h.Extra = make([]byte, n)
		copy(h.Extra, b)
	} else if flags&hcfExtra32Zero != 0 {
		h.Extra = make([]byte, 32)
	}
	if flags&hcfMixDigest != 0 {
		if b, err = take(32); err != nil {
			return err
		}
		copy(h.MixDigest[:], b)
	}
	if flags&hcfNonce != 0 {
		if b, err = take(8); err != nil {
			return err
		}
		copy(h.Nonce[:], b)
	}
	if flags&hcfBaseFee != 0 {
		if b, err = take(1); err != nil {
			return err
		}
		n := int(b[0])
		if b, err = take(n); err != nil {
			return err
		}
		h.BaseFee = new(uint256.Int).SetBytes(b)
	}
	if flags&hcfWithdrawalsHash != 0 {
		if b, err = take(32); err != nil {
			return err
		}
		var wh types.Hash
		copy(wh[:], b)
		h.WithdrawalsHash = &wh
	} else if flags&hcfWithdrawalsEmpty != 0 {
		wh := emptyListRoot
		h.WithdrawalsHash = &wh
	}
	if flags&hcfBlobGasUsed != 0 {
		v, e := uvarint()
		if e != nil {
			return e
		}
		h.BlobGasUsed = &v
	}
	if flags&hcfExcessBlobGas != 0 {
		v, e := uvarint()
		if e != nil {
			return e
		}
		h.ExcessBlobGas = &v
	}
	if flags&hcfParentBeaconRoot != 0 {
		if b, err = take(32); err != nil {
			return err
		}
		var pbr types.Hash
		copy(pbr[:], b)
		h.ParentBeaconRoot = &pbr
	}
	if flags&hcfRequestsHash != 0 {
		if b, err = take(32); err != nil {
			return err
		}
		var rh types.Hash
		copy(rh[:], b)
		h.RequestsHash = &rh
	} else if flags&hcfRequestsEmpty != 0 {
		rh := emptyListRoot
		h.RequestsHash = &rh
	}
	if flags&hcfBlockAccessListHash != 0 {
		if b, err = take(32); err != nil {
			return err
		}
		var bah types.Hash
		copy(bah[:], b)
		h.BlockAccessListHash = &bah
	}
	if flags&hcfMobileRegistryRoot != 0 {
		if b, err = take(32); err != nil {
			return err
		}
		var mrr types.Hash
		copy(mrr[:], b)
		h.MobileRegistryRoot = &mrr
	}
	return nil
}

func isZero32(b []byte) bool {
	if len(b) != 32 {
		return false
	}
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}
