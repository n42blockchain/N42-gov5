// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package ethel implements the Ethereum Execution Layer replay engine.
// It reads Geth ancient data and re-executes blocks to produce state,
// receipts, changesets, and MPT roots.

package ethel

import (
	"fmt"
	"math/big"

	"github.com/golang/snappy"
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

// decompressGeth decompresses Snappy-encoded Geth freezer data.
// Geth uses Snappy block encoding for all freezer table entries.
func decompressGeth(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("ethel: empty data (corrupt freezer entry)")
	}
	return snappy.Decode(nil, data)
}

// DecodeGethHeader decodes a Geth-format RLP-encoded Ethereum header.
// Handles all forks from genesis through Pectra (variable field count).
func DecodeGethHeader(data []byte) (*block.Header, error) {
	raw, err := decompressGeth(data)
	if err != nil {
		return nil, fmt.Errorf("ethel: decompress header: %w", err)
	}
	var elems []rlp.RawValue
	if err := rlp.DecodeBytes(raw, &elems); err != nil {
		return nil, fmt.Errorf("ethel: decode header list: %w", err)
	}
	return decodeHeaderFields(elems)
}

// decodeHeaderFields decodes an already-parsed RLP element list into a Header.
func decodeHeaderFields(elems []rlp.RawValue) (*block.Header, error) {
	n := len(elems)
	if n < 15 {
		return nil, fmt.Errorf("ethel: header has %d fields, need >= 15", n)
	}

	h := &block.Header{}

	if err := rlp.DecodeBytes(elems[0], &h.ParentHash); err != nil {
		return nil, fmt.Errorf("ethel: decode parentHash: %w", err)
	}
	if err := rlp.DecodeBytes(elems[1], &h.UncleHash); err != nil {
		return nil, fmt.Errorf("ethel: decode uncleHash: %w", err)
	}
	if err := rlp.DecodeBytes(elems[2], &h.Coinbase); err != nil {
		return nil, fmt.Errorf("ethel: decode coinbase: %w", err)
	}
	if err := rlp.DecodeBytes(elems[3], &h.Root); err != nil {
		return nil, fmt.Errorf("ethel: decode stateRoot: %w", err)
	}
	if err := rlp.DecodeBytes(elems[4], &h.TxHash); err != nil {
		return nil, fmt.Errorf("ethel: decode txHash: %w", err)
	}
	if err := rlp.DecodeBytes(elems[5], &h.ReceiptHash); err != nil {
		return nil, fmt.Errorf("ethel: decode receiptHash: %w", err)
	}
	if err := rlp.DecodeBytes(elems[6], &h.Bloom); err != nil {
		return nil, fmt.Errorf("ethel: decode bloom: %w", err)
	}

	// Difficulty and Number: decode as big.Int then convert.
	var difficulty big.Int
	if err := rlp.DecodeBytes(elems[7], &difficulty); err != nil {
		return nil, fmt.Errorf("ethel: decode difficulty: %w", err)
	}
	h.Difficulty = uint256.MustFromBig(&difficulty)

	var number big.Int
	if err := rlp.DecodeBytes(elems[8], &number); err != nil {
		return nil, fmt.Errorf("ethel: decode number: %w", err)
	}
	h.Number = uint256.MustFromBig(&number)

	if err := rlp.DecodeBytes(elems[9], &h.GasLimit); err != nil {
		return nil, fmt.Errorf("ethel: decode gasLimit: %w", err)
	}
	if err := rlp.DecodeBytes(elems[10], &h.GasUsed); err != nil {
		return nil, fmt.Errorf("ethel: decode gasUsed: %w", err)
	}
	if err := rlp.DecodeBytes(elems[11], &h.Time); err != nil {
		return nil, fmt.Errorf("ethel: decode time: %w", err)
	}
	if err := rlp.DecodeBytes(elems[12], &h.Extra); err != nil {
		return nil, fmt.Errorf("ethel: decode extra: %w", err)
	}
	if err := rlp.DecodeBytes(elems[13], &h.MixDigest); err != nil {
		return nil, fmt.Errorf("ethel: decode mixDigest: %w", err)
	}
	if err := rlp.DecodeBytes(elems[14], &h.Nonce); err != nil {
		return nil, fmt.Errorf("ethel: decode nonce: %w", err)
	}

	// Optional fields by fork.
	if n > 15 { // EIP-1559 (London): baseFee
		var baseFee big.Int
		if err := rlp.DecodeBytes(elems[15], &baseFee); err != nil {
			return nil, fmt.Errorf("ethel: decode baseFee: %w", err)
		}
		h.BaseFee = uint256.MustFromBig(&baseFee)
	}
	if n > 16 { // EIP-4895 (Shanghai): withdrawalsHash
		var wh types.Hash
		if err := rlp.DecodeBytes(elems[16], &wh); err != nil {
			return nil, fmt.Errorf("ethel: decode withdrawalsHash: %w", err)
		}
		h.WithdrawalsHash = &wh
	}
	if n > 17 { // EIP-4844 (Cancun): blobGasUsed
		var bgu uint64
		if err := rlp.DecodeBytes(elems[17], &bgu); err != nil {
			return nil, fmt.Errorf("ethel: decode blobGasUsed: %w", err)
		}
		h.BlobGasUsed = &bgu
	}
	if n > 18 { // EIP-4844 (Cancun): excessBlobGas
		var ebg uint64
		if err := rlp.DecodeBytes(elems[18], &ebg); err != nil {
			return nil, fmt.Errorf("ethel: decode excessBlobGas: %w", err)
		}
		h.ExcessBlobGas = &ebg
	}
	if n > 19 { // EIP-4788 (Cancun): parentBeaconBlockRoot
		var pbr types.Hash
		if err := rlp.DecodeBytes(elems[19], &pbr); err != nil {
			return nil, fmt.Errorf("ethel: decode parentBeaconRoot: %w", err)
		}
		h.ParentBeaconRoot = &pbr
	}
	if n > 20 { // EIP-7685 (Pectra): requestsHash
		var rh types.Hash
		if err := rlp.DecodeBytes(elems[20], &rh); err != nil {
			return nil, fmt.Errorf("ethel: decode requestsHash: %w", err)
		}
		h.RequestsHash = &rh
	}

	return h, nil
}

// gethBody is the Geth RLP body structure: [transactions, uncles, withdrawals?]
type gethBody struct {
	Transactions []rlp.RawValue // each tx is type-prefixed RLP
	Uncles       []rlp.RawValue // uncle headers (ignored post-merge)
	// Withdrawals are optional (Shanghai+), handled manually
}

// GethBodyResult holds the decoded body components.
type GethBodyResult struct {
	Transactions []*transaction.Transaction
	Uncles       []*block.Header
}

// DecodeGethBody decodes a Geth-format RLP-encoded body.
// Returns transactions and uncle headers.
func DecodeGethBody(data []byte) (*GethBodyResult, error) {
	raw, err := decompressGeth(data)
	if err != nil {
		return nil, fmt.Errorf("ethel: decompress body: %w", err)
	}
	// Body is an RLP list: [txs_list, uncles_list, withdrawals_list?]
	var elems []rlp.RawValue
	if err := rlp.DecodeBytes(raw, &elems); err != nil {
		return nil, fmt.Errorf("ethel: decode body list: %w", err)
	}
	if len(elems) < 2 {
		return nil, fmt.Errorf("ethel: body has %d elements, need >= 2", len(elems))
	}

	// Decode transactions list.
	var txRaws []rlp.RawValue
	if err := rlp.DecodeBytes(elems[0], &txRaws); err != nil {
		return nil, fmt.Errorf("ethel: decode txs list: %w", err)
	}

	txs := make([]*transaction.Transaction, 0, len(txRaws))
	for i, txRaw := range txRaws {
		// In Geth's body RLP, typed transactions (EIP-2718) are stored as
		// RLP strings: rlp_string(type || rlp(tx_payload)). Legacy txs are
		// stored as RLP lists. We need to unwrap the string for typed txs.
		data := []byte(txRaw)
		if len(data) > 0 && data[0] >= 0x80 && data[0] < 0xc0 {
			// RLP string wrapper — unwrap to get type || rlp(payload)
			var inner []byte
			if err := rlp.DecodeBytes(data, &inner); err != nil {
				return nil, fmt.Errorf("ethel: unwrap tx %d: %w", i, err)
			}
			data = inner
		}
		tx, err := transaction.DecodeEthereumTransaction(data)
		if err != nil {
			return nil, fmt.Errorf("ethel: decode tx %d: %w", i, err)
		}
		txs = append(txs, tx)
	}

	// Decode uncle headers.
	var uncleRaws []rlp.RawValue
	if err := rlp.DecodeBytes(elems[1], &uncleRaws); err != nil {
		return nil, fmt.Errorf("ethel: decode uncles list: %w", err)
	}

	uncles := make([]*block.Header, 0, len(uncleRaws))
	for i, uRaw := range uncleRaws {
		// Uncle headers are NOT snappy-compressed (they're nested in the body).
		var uElems []rlp.RawValue
		if err := rlp.DecodeBytes(uRaw, &uElems); err != nil {
			return nil, fmt.Errorf("ethel: decode uncle %d: %w", i, err)
		}
		u, err := decodeHeaderFields(uElems)
		if err != nil {
			return nil, fmt.Errorf("ethel: decode uncle header %d: %w", i, err)
		}
		uncles = append(uncles, u)
	}

	return &GethBodyResult{Transactions: txs, Uncles: uncles}, nil
}

