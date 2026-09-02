// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Compact (V2-style) transaction STORAGE codec — companion to the compact
// header codec. The proto encoding averaged ~242 B/tx (5.7 GB BlockTransaction
// table at 12.7M blocks), spending bytes on proto tags and full 32-byte H256
// wrappers for small integers. This codec stores a type byte, a field-presence
// bitmask, uvarint scalars, and minimal-length big integers.
//
// Wire layout:
//
//	[0] 0xFF  — format marker; a valid protobuf message can never begin with
//	            0xFF (field 31 / wire type 7 is illegal), and a typed-RLP or
//	            legacy-RLP Ethereum tx starts with 0x00..0x05 or >=0xc0, so the
//	            marker is unambiguous against every other stored format.
//	[1] 0x01  — codec version
//	[2] txType (LegacyTxType / AccessListTxType / DynamicFeeTxType)
//	[3:5] flags uint16 LE
//	then type-ordered payloads (see encode).
//
// Only the three types that occur on the converted chain are compact-encoded;
// MarshalCompactStorage returns nil for anything else and the writer falls
// back to proto — readers dispatch per record, so tables mix freely. From and
// Sign are preserved exactly (the chain embeds the verified sender; the tx
// hash is RLP-derived from the consensus fields, so storage encoding never
// affects identity).

package transaction

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

const (
	compactTxMarker  = 0xFF
	compactTxVersion = 0x01
)

const (
	tcfTo         = 1 << 0 // To present (20 B); absent = contract creation
	tcfFrom       = 1 << 1 // From present (20 B)
	tcfData       = 1 << 2 // Data non-empty (uvarint len + bytes)
	tcfSign       = 1 << 3 // Sign non-empty (uvarint len + bytes)
	tcfAccessList = 1 << 4 // AccessList non-empty (AL/DynamicFee only)
)

// u256 encoding: 1 length byte then minimal big-endian bytes.
// 0xFF length = nil pointer (distinct from a present zero, length 0).
func appendU256(b []byte, v *uint256.Int) []byte {
	if v == nil {
		return append(b, 0xFF)
	}
	// uint256's Bytes() is Bytes32() re-sliced and returned, so the array
	// escapes and every call allocates. Taking Bytes32 into a local keeps it on
	// the stack: the bytes appended are identical -- ByteLen is what Bytes()
	// slices by -- and this is called five or six times per transaction, which
	// made uint256.Bytes 78% of all objects allocated while writing a block's
	// transactions.
	buf := v.Bytes32()
	n := v.ByteLen()
	b = append(b, byte(n))
	return append(b, buf[32-n:]...)
}

func appendUvarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], v)
	return append(b, tmp[:n]...)
}

type txCompactReader struct {
	p []byte
	// arena backs the uint256 fields of the transaction being decoded.
	//
	// Each field used to get its own new(uint256.Int): up to seven 32-byte
	// allocations per transaction (chain id, two gas prices, value, and the v/r/s
	// signature words), which made this the largest single allocator in a
	// loaded node -- 604 MB of live heap on its own. One reader decodes one
	// transaction, so a single backing array per reader has exactly the
	// lifetime of the transaction it fills: the whole array stays reachable
	// while any of its fields is, and dies with them.
	arena []uint256.Int
}

// u256ArenaSize covers the most uint256 fields any transaction type carries:
// chain id, gas tip cap, gas fee cap, value, v, r, s.
const u256ArenaSize = 7

func (r *txCompactReader) take(n int) ([]byte, error) {
	if len(r.p) < n {
		return nil, fmt.Errorf("compact tx truncated: need %d, have %d", n, len(r.p))
	}
	b := r.p[:n]
	r.p = r.p[n:]
	return b, nil
}

func (r *txCompactReader) uvarint() (uint64, error) {
	v, n := binary.Uvarint(r.p)
	if n <= 0 {
		return 0, fmt.Errorf("compact tx: bad uvarint")
	}
	r.p = r.p[n:]
	return v, nil
}

func (r *txCompactReader) u256() (*uint256.Int, error) {
	lb, err := r.take(1)
	if err != nil {
		return nil, err
	}
	if lb[0] == 0xFF {
		return nil, nil
	}
	if lb[0] > 32 {
		return nil, fmt.Errorf("compact tx: u256 length %d > 32", lb[0])
	}
	bs, err := r.take(int(lb[0]))
	if err != nil {
		return nil, err
	}
	if len(r.arena) == 0 {
		r.arena = make([]uint256.Int, u256ArenaSize)
	}
	out := &r.arena[0]
	r.arena = r.arena[1:]
	out.SetBytes(bs)
	return out, nil
}

func (r *txCompactReader) addr() (*types.Address, error) {
	b, err := r.take(20)
	if err != nil {
		return nil, err
	}
	var a types.Address
	copy(a[:], b)
	return &a, nil
}

func (r *txCompactReader) blob() ([]byte, error) {
	n, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	b, err := r.take(int(n))
	if err != nil {
		return nil, err
	}
	out := make([]byte, n)
	copy(out, b)
	return out, nil
}

func appendAccessList(b []byte, al AccessList) []byte {
	b = appendUvarint(b, uint64(len(al)))
	for _, t := range al {
		b = append(b, t.Address[:]...)
		b = appendUvarint(b, uint64(len(t.StorageKeys)))
		for _, k := range t.StorageKeys {
			b = append(b, k[:]...)
		}
	}
	return b
}

func (r *txCompactReader) accessList() (AccessList, error) {
	n, err := r.uvarint()
	if err != nil {
		return nil, err
	}
	al := make(AccessList, n)
	for i := range al {
		b, err := r.take(20)
		if err != nil {
			return nil, err
		}
		copy(al[i].Address[:], b)
		nk, err := r.uvarint()
		if err != nil {
			return nil, err
		}
		al[i].StorageKeys = make([]types.Hash, nk)
		for j := range al[i].StorageKeys {
			kb, err := r.take(32)
			if err != nil {
				return nil, err
			}
			copy(al[i].StorageKeys[j][:], kb)
		}
	}
	return al, nil
}

// MarshalCompactStorage encodes supported tx types in the compact storage
// format; returns nil for unsupported types (caller falls back to proto).
func (tx *Transaction) MarshalCompactStorage() []byte {
	var (
		flags                  uint16
		nonce, gas             uint64
		chainID, p1, p2, value *uint256.Int // p1/p2: gasPrice or tip/feeCap
		to, from               *types.Address
		data, sign             []byte
		al                     AccessList
		v, rr, s               *uint256.Int
		typ                    byte
		hasChainID, hasAL      bool
	)
	switch t := tx.inner.(type) {
	case *LegacyTx:
		typ = LegacyTxType
		nonce, gas, p1, value = t.Nonce, t.Gas, t.GasPrice, t.Value
		to, from, data, sign = t.To, t.From, t.Data, t.Sign
		v, rr, s = t.V, t.R, t.S
	case *AccessListTx:
		typ = AccessListTxType
		hasChainID, hasAL = true, true
		chainID, nonce, gas, p1, value = t.ChainID, t.Nonce, t.Gas, t.GasPrice, t.Value
		to, from, data, al, sign = t.To, t.From, t.Data, t.AccessList, t.Sign
		v, rr, s = t.V, t.R, t.S
	case *DynamicFeeTx:
		typ = DynamicFeeTxType
		hasChainID, hasAL = true, true
		chainID, nonce, gas, p1, p2, value = t.ChainID, t.Nonce, t.Gas, t.GasTipCap, t.GasFeeCap, t.Value
		to, from, data, al, sign = t.To, t.From, t.Data, t.AccessList, t.Sign
		v, rr, s = t.V, t.R, t.S
	default:
		return nil // unsupported type: caller writes proto
	}

	if to != nil {
		flags |= tcfTo
	}
	if from != nil {
		flags |= tcfFrom
	}
	if len(data) > 0 {
		flags |= tcfData
	}
	if len(sign) > 0 {
		flags |= tcfSign
	}
	if hasAL && len(al) > 0 {
		flags |= tcfAccessList
	}

	out := make([]byte, 0, 160+len(data)+len(sign))
	out = append(out, compactTxMarker, compactTxVersion, typ,
		byte(flags), byte(flags>>8))
	if hasChainID {
		out = appendU256(out, chainID)
	}
	out = appendUvarint(out, nonce)
	out = appendUvarint(out, gas)
	out = appendU256(out, p1)
	if typ == DynamicFeeTxType {
		out = appendU256(out, p2)
	}
	out = appendU256(out, value)
	if to != nil {
		out = append(out, to[:]...)
	}
	if from != nil {
		out = append(out, from[:]...)
	}
	if len(data) > 0 {
		out = appendUvarint(out, uint64(len(data)))
		out = append(out, data...)
	}
	if flags&tcfAccessList != 0 {
		out = appendAccessList(out, al)
	}
	if len(sign) > 0 {
		out = appendUvarint(out, uint64(len(sign)))
		out = append(out, sign...)
	}
	out = appendU256(out, v)
	out = appendU256(out, rr)
	out = appendU256(out, s)
	return out
}

// IsCompactTx reports whether data is in the compact tx storage format.
func IsCompactTx(data []byte) bool {
	return len(data) >= 5 && data[0] == compactTxMarker && data[1] == compactTxVersion
}

// unmarshalCompactStorage decodes the compact storage format.
func (tx *Transaction) unmarshalCompactStorage(data []byte) error {
	if !IsCompactTx(data) {
		return fmt.Errorf("not a compact tx")
	}
	typ := data[2]
	flags := uint16(data[3]) | uint16(data[4])<<8
	r := &txCompactReader{p: data[5:]}

	var (
		chainID, p1, p2, value, v, rr, s *uint256.Int
		nonce, gas                       uint64
		to, from                         *types.Address
		dataB, sign                      []byte
		al                               AccessList
		err                              error
	)
	if typ == AccessListTxType || typ == DynamicFeeTxType {
		if chainID, err = r.u256(); err != nil {
			return err
		}
	}
	if nonce, err = r.uvarint(); err != nil {
		return err
	}
	if gas, err = r.uvarint(); err != nil {
		return err
	}
	if p1, err = r.u256(); err != nil {
		return err
	}
	if typ == DynamicFeeTxType {
		if p2, err = r.u256(); err != nil {
			return err
		}
	}
	if value, err = r.u256(); err != nil {
		return err
	}
	if flags&tcfTo != 0 {
		if to, err = r.addr(); err != nil {
			return err
		}
	}
	if flags&tcfFrom != 0 {
		if from, err = r.addr(); err != nil {
			return err
		}
	}
	if flags&tcfData != 0 {
		if dataB, err = r.blob(); err != nil {
			return err
		}
	}
	if flags&tcfAccessList != 0 {
		if al, err = r.accessList(); err != nil {
			return err
		}
	}
	if flags&tcfSign != 0 {
		if sign, err = r.blob(); err != nil {
			return err
		}
	}
	if v, err = r.u256(); err != nil {
		return err
	}
	if rr, err = r.u256(); err != nil {
		return err
	}
	if s, err = r.u256(); err != nil {
		return err
	}

	var inner TxData
	switch typ {
	case LegacyTxType:
		inner = &LegacyTx{
			Nonce: nonce, Gas: gas, GasPrice: p1, Value: value,
			To: to, From: from, Data: dataB, Sign: sign, V: v, R: rr, S: s,
		}
	case AccessListTxType:
		inner = &AccessListTx{
			ChainID: chainID, Nonce: nonce, Gas: gas, GasPrice: p1, Value: value,
			To: to, From: from, Data: dataB, AccessList: al, Sign: sign, V: v, R: rr, S: s,
		}
	case DynamicFeeTxType:
		inner = &DynamicFeeTx{
			ChainID: chainID, Nonce: nonce, Gas: gas, GasTipCap: p1, GasFeeCap: p2, Value: value,
			To: to, From: from, Data: dataB, AccessList: al, Sign: sign, V: v, R: rr, S: s,
		}
	default:
		return fmt.Errorf("compact tx: unsupported type %d", typ)
	}
	tx.setDecoded(inner, 0)
	return nil
}
