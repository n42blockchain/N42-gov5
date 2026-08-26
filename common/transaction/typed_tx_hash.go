// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// typed_tx_hash.go — direct RLP for the EIP-2930 / EIP-1559 transaction
// envelopes, mirroring appendLegacyTxRLP in legacy_tx.go.
//
// The reflection-based PrefixedRlpHash([]interface{}{...}) path cost 1.74% of
// all replay CPU on dense mainnet blocks, 87% of it from DynamicFeeTx — the
// dominant type since London — and allocated ~10 GiB per 200k blocks. The
// envelope is a fixed field list, so it is written out field by field into a
// pooled buffer and hashed once. Identity fields only: From and Sign are
// storage-side and never part of the hash, exactly as in the reflection path.

package transaction

import (
	"sync"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

var typedHashBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 512)
		return &b
	},
}

// hashTypedEnvelope keccaks prefix || rlp, reusing a pooled buffer. The bound
// on what goes back into the pool matches legacy_tx.go: one huge calldata
// must not pin a buffer for the whole process.
func hashTypedEnvelope(prefix byte, appendRLP func(dst []byte) []byte) types.Hash {
	bufp := typedHashBufferPool.Get().(*[]byte)
	encoded := append((*bufp)[:0], prefix)
	encoded = appendRLP(encoded)
	h := crypto.Keccak256Hash(encoded)
	if cap(encoded) <= 1<<20 {
		*bufp = encoded[:0]
		typedHashBufferPool.Put(bufp)
	}
	return h
}

func (tx *DynamicFeeTx) hash() types.Hash {
	return hashTypedEnvelope(DynamicFeeTxType, func(dst []byte) []byte {
		return appendDynamicFeeTxRLP(dst, tx)
	})
}

func (tx *AccessListTx) hash() types.Hash {
	return hashTypedEnvelope(AccessListTxType, func(dst []byte) []byte {
		return appendAccessListTxRLP(dst, tx)
	})
}

// appendDynamicFeeTxRLP writes
// [chainId, nonce, maxPriorityFeePerGas, maxFeePerGas, gasLimit, to, value,
// data, accessList, v, r, s].
func appendDynamicFeeTxRLP(dst []byte, tx *DynamicFeeTx) []byte {
	contentSize := rlpUint256Size(tx.ChainID) + uint64(rlp.IntSize(tx.Nonce)) +
		rlpUint256Size(tx.GasTipCap) + rlpUint256Size(tx.GasFeeCap) +
		uint64(rlp.IntSize(tx.Gas)) + rlpAddressSize(tx.To) +
		rlpUint256Size(tx.Value) + rlp.BytesSize(tx.Data) +
		accessListRLPSize(tx.AccessList) +
		rlpUint256Size(tx.V) + rlpUint256Size(tx.R) + rlpUint256Size(tx.S)
	dst = appendRLPCollectionPrefix(dst, 0xc0, 0xf7, contentSize)
	dst = appendRLPUint256(dst, tx.ChainID)
	dst = rlp.AppendUint64(dst, tx.Nonce)
	dst = appendRLPUint256(dst, tx.GasTipCap)
	dst = appendRLPUint256(dst, tx.GasFeeCap)
	dst = rlp.AppendUint64(dst, tx.Gas)
	dst = appendRLPAddressPtr(dst, tx.To)
	dst = appendRLPUint256(dst, tx.Value)
	dst = appendRLPBytes(dst, tx.Data)
	dst = appendAccessListRLP(dst, tx.AccessList)
	dst = appendRLPUint256(dst, tx.V)
	dst = appendRLPUint256(dst, tx.R)
	return appendRLPUint256(dst, tx.S)
}

// appendAccessListTxRLP writes
// [chainId, nonce, gasPrice, gasLimit, to, value, data, accessList, v, r, s].
func appendAccessListTxRLP(dst []byte, tx *AccessListTx) []byte {
	contentSize := rlpUint256Size(tx.ChainID) + uint64(rlp.IntSize(tx.Nonce)) +
		rlpUint256Size(tx.GasPrice) + uint64(rlp.IntSize(tx.Gas)) +
		rlpAddressSize(tx.To) + rlpUint256Size(tx.Value) + rlp.BytesSize(tx.Data) +
		accessListRLPSize(tx.AccessList) +
		rlpUint256Size(tx.V) + rlpUint256Size(tx.R) + rlpUint256Size(tx.S)
	dst = appendRLPCollectionPrefix(dst, 0xc0, 0xf7, contentSize)
	dst = appendRLPUint256(dst, tx.ChainID)
	dst = rlp.AppendUint64(dst, tx.Nonce)
	dst = appendRLPUint256(dst, tx.GasPrice)
	dst = rlp.AppendUint64(dst, tx.Gas)
	dst = appendRLPAddressPtr(dst, tx.To)
	dst = appendRLPUint256(dst, tx.Value)
	dst = appendRLPBytes(dst, tx.Data)
	dst = appendAccessListRLP(dst, tx.AccessList)
	dst = appendRLPUint256(dst, tx.V)
	dst = appendRLPUint256(dst, tx.R)
	return appendRLPUint256(dst, tx.S)
}

func appendRLPAddressPtr(dst []byte, addr *types.Address) []byte {
	if addr == nil {
		return append(dst, 0x80)
	}
	dst = append(dst, 0x94)
	return append(dst, addr[:]...)
}

// An access tuple is [address, [key, key, ...]]: a 21-byte address string and
// a list of 33-byte key strings. Keys are fixed 32 bytes so every key encodes
// as 0xa0 || key; there is no short-string case to consider.
func accessTupleContentSize(t *AccessTuple) uint64 {
	return 21 + rlp.ListSize(uint64(len(t.StorageKeys))*33)
}

func accessListRLPSize(al AccessList) uint64 {
	var content uint64
	for i := range al {
		content += rlp.ListSize(accessTupleContentSize(&al[i]))
	}
	return rlp.ListSize(content)
}

func appendAccessListRLP(dst []byte, al AccessList) []byte {
	var content uint64
	for i := range al {
		content += rlp.ListSize(accessTupleContentSize(&al[i]))
	}
	dst = appendRLPCollectionPrefix(dst, 0xc0, 0xf7, content)
	for i := range al {
		t := &al[i]
		dst = appendRLPCollectionPrefix(dst, 0xc0, 0xf7, accessTupleContentSize(t))
		dst = append(dst, 0x94)
		dst = append(dst, t.Address[:]...)
		dst = appendRLPCollectionPrefix(dst, 0xc0, 0xf7, uint64(len(t.StorageKeys))*33)
		for k := range t.StorageKeys {
			dst = append(dst, 0xa0)
			dst = append(dst, t.StorageKeys[k][:]...)
		}
	}
	return dst
}
