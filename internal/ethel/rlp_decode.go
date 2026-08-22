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
// Handles all forks from genesis through Amsterdam (variable field count).
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

// DecodeUncleHeader decodes a single uncle header from raw RLP bytes.
// Unlike DecodeGethHeader, no Snappy decompression — uncles are nested
// inside the body's RLP list, not stored as standalone freezer entries.
func DecodeUncleHeader(raw []byte) (*block.Header, error) {
	var elems []rlp.RawValue
	if err := rlp.DecodeBytes(raw, &elems); err != nil {
		return nil, fmt.Errorf("decode uncle list: %w", err)
	}
	return decodeHeaderFields(elems)
}

// decodeHeaderFields decodes an already-parsed RLP element list into a Header.
func decodeHeaderFields(elems []rlp.RawValue) (*block.Header, error) {
	n := len(elems)
	if n < 15 {
		return nil, fmt.Errorf("ethel: header has %d fields, need >= 15", n)
	}
	if n > 22 {
		return nil, fmt.Errorf("ethel: header has %d fields, maximum supported is 22", n)
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
	difficulty256, err := uint256FromBig(&difficulty, "difficulty")
	if err != nil {
		return nil, err
	}
	h.Difficulty = difficulty256

	var number big.Int
	if err := rlp.DecodeBytes(elems[8], &number); err != nil {
		return nil, fmt.Errorf("ethel: decode number: %w", err)
	}
	number256, err := uint256FromBig(&number, "number")
	if err != nil {
		return nil, err
	}
	h.Number = number256

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
		baseFee256, err := uint256FromBig(&baseFee, "baseFee")
		if err != nil {
			return nil, err
		}
		h.BaseFee = baseFee256
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
	if n > 21 { // EIP-7928 (Amsterdam): blockAccessListHash
		var balHash types.Hash
		if err := rlp.DecodeBytes(elems[21], &balHash); err != nil {
			return nil, fmt.Errorf("ethel: decode blockAccessListHash: %w", err)
		}
		h.BlockAccessListHash = &balHash
	}

	return h, nil
}

func uint256FromBig(value *big.Int, field string) (*uint256.Int, error) {
	converted, overflow := uint256.FromBig(value)
	if overflow {
		return nil, fmt.Errorf("ethel: %s exceeds 256 bits", field)
	}
	return converted, nil
}

// gethBody is the Geth RLP body structure: [transactions, uncles, withdrawals?]
type gethBody struct {
	Transactions []rlp.RawValue // each tx is type-prefixed RLP
	Uncles       []rlp.RawValue // uncle headers (ignored post-merge)
	// Withdrawals are optional (Shanghai+), handled manually
}

// Withdrawal represents an EIP-4895 validator withdrawal.
type Withdrawal struct {
	Index     uint64
	Validator uint64
	Address   types.Address
	Amount    uint64 // in Gwei
}

// GethBodyResult holds the decoded body components.
type GethBodyResult struct {
	Transactions []*transaction.Transaction
	Uncles       []*block.Header
	// UncleRaw is the raw RLP bytes per uncle, in the same order as Uncles.
	// Populated only by DecodeGethBody; nil for synthetic sources. We keep
	// the bytes because we have no geth-compatible Header RLP encoder, so
	// callers that need byte-identical reconstruction (body_compact) can
	// stash these instead of re-encoding.
	UncleRaw    [][]byte
	Withdrawals []*Withdrawal // nil pre-Shanghai
}

// DecodeRawBlock decodes the canonical Ethereum block RLP used by Hive's
// consume-rlp simulator: [header, transactions, ommers, withdrawals?].
//
// The freezer reader above receives headers and bodies separately (and Snappy
// compressed), while Hive supplies one uncompressed block per file. Keeping
// this decoder here makes both paths use the same transaction and withdrawal
// handling. Ommers are decoded for structural validation but are not attached
// to block.Block: eth-el's payload execution path is post-merge and does not
// process ommer rewards.
func DecodeRawBlock(data []byte) (*block.Block, []*Withdrawal, error) {
	var elems []rlp.RawValue
	if err := rlp.DecodeBytes(data, &elems); err != nil {
		return nil, nil, fmt.Errorf("ethel: decode block list: %w", err)
	}
	if len(elems) < 3 || len(elems) > 4 {
		return nil, nil, fmt.Errorf("ethel: block has %d fields, want 3 or 4", len(elems))
	}

	header, err := DecodeUncleHeader(elems[0])
	if err != nil {
		return nil, nil, fmt.Errorf("ethel: decode block header: %w", err)
	}
	txs, err := decodeEthereumTransactions(elems[1])
	if err != nil {
		return nil, nil, err
	}

	// Decode the ommer list so malformed input is not silently accepted. The
	// header already commits to its hash and modern eth-el fixtures have none.
	var ommers []rlp.RawValue
	if err := rlp.DecodeBytes(elems[2], &ommers); err != nil {
		return nil, nil, fmt.Errorf("ethel: decode block ommers: %w", err)
	}
	if len(ommers) != 0 {
		return nil, nil, fmt.Errorf("ethel: ommer blocks are unsupported by eth-el RLP import")
	}

	var withdrawals []*Withdrawal
	if len(elems) == 4 {
		withdrawals, err = decodeWithdrawals(elems[3])
		if err != nil {
			return nil, nil, err
		}
	}
	return block.NewBlock(header, txs).(*block.Block), withdrawals, nil
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
	if len(elems) < 2 || len(elems) > 3 {
		return nil, fmt.Errorf("ethel: body has %d elements, want 2 or 3", len(elems))
	}

	// Decode transactions list.
	txs, err := decodeEthereumTransactions(elems[0])
	if err != nil {
		return nil, err
	}

	// Decode uncle headers.
	var uncleRaws []rlp.RawValue
	if err := rlp.DecodeBytes(elems[1], &uncleRaws); err != nil {
		return nil, fmt.Errorf("ethel: decode uncles list: %w", err)
	}

	uncles := make([]*block.Header, 0, len(uncleRaws))
	uncleRawCopy := make([][]byte, 0, len(uncleRaws))
	for i, uRaw := range uncleRaws {
		// uRaw aliases the body's RLP buffer which the caller may free —
		// snapshot before stashing for body_compact's byte-identical reuse.
		uncleRawCopy = append(uncleRawCopy, types.CopyBytes(uRaw))
		u, err := DecodeUncleHeader(uRaw)
		if err != nil {
			return nil, fmt.Errorf("ethel: uncle %d: %w", i, err)
		}
		uncles = append(uncles, u)
	}

	// Decode withdrawals (optional, Shanghai+).
	var withdrawals []*Withdrawal
	if len(elems) >= 3 {
		withdrawals, err = decodeWithdrawals(elems[2])
		if err != nil {
			return nil, err
		}
	}

	return &GethBodyResult{Transactions: txs, Uncles: uncles, UncleRaw: uncleRawCopy, Withdrawals: withdrawals}, nil
}

func decodeEthereumTransactions(raw rlp.RawValue) ([]*transaction.Transaction, error) {
	var txRaws []rlp.RawValue
	if err := rlp.DecodeBytes(raw, &txRaws); err != nil {
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
		// EIP-4844 network wrappers carry blobs, commitments, and proofs for
		// transaction propagation. A block body must contain only the canonical
		// blob transaction envelope, never the propagation sidecar.
		if tx.BlobTxSidecar() != nil {
			return nil, fmt.Errorf("ethel: tx %d contains a blob sidecar in block body", i)
		}
		txs = append(txs, tx)
	}
	return txs, nil
}

func decodeWithdrawals(raw rlp.RawValue) ([]*Withdrawal, error) {
	var wdRaws []rlp.RawValue
	if err := rlp.DecodeBytes(raw, &wdRaws); err != nil {
		return nil, fmt.Errorf("ethel: decode withdrawals list: %w", err)
	}
	withdrawals := make([]*Withdrawal, len(wdRaws))
	for i, wRaw := range wdRaws {
		w, err := decodeGethWithdrawal(wRaw)
		if err != nil {
			return nil, fmt.Errorf("ethel: withdrawal %d: %w", i, err)
		}
		withdrawals[i] = w
	}
	return withdrawals, nil
}

func decodeGethWithdrawal(data []byte) (*Withdrawal, error) {
	var elems []rlp.RawValue
	if err := rlp.DecodeBytes(data, &elems); err != nil {
		return nil, fmt.Errorf("decode withdrawal: %w", err)
	}
	if len(elems) != 4 {
		return nil, fmt.Errorf("withdrawal has %d fields, want 4", len(elems))
	}
	w := &Withdrawal{}
	if err := rlp.DecodeBytes(elems[0], &w.Index); err != nil {
		return nil, fmt.Errorf("decode index: %w", err)
	}
	if err := rlp.DecodeBytes(elems[1], &w.Validator); err != nil {
		return nil, fmt.Errorf("decode validator: %w", err)
	}
	if err := rlp.DecodeBytes(elems[2], &w.Address); err != nil {
		return nil, fmt.Errorf("decode address: %w", err)
	}
	if err := rlp.DecodeBytes(elems[3], &w.Amount); err != nil {
		return nil, fmt.Errorf("decode amount: %w", err)
	}
	return w, nil
}

// DecodeGethReceipts decodes Geth ancient receipts (ReceiptForStorage RLP).
// Geth stores receipts as: snappy(rlp([receipt0, receipt1, ...])).
// Each receipt is either:
//   - Legacy: rlp([statusOrPostState, cumulativeGas, bloom, logs])
//   - Typed (EIP-2718): rlp_string(type || rlp([status, cumulativeGas, bloom, logs]))
//
// Log format: rlp([address, topics, data])
func DecodeGethReceipts(data []byte) (block.Receipts, error) {
	raw, err := decompressGeth(data)
	if err != nil {
		return nil, fmt.Errorf("ethel: decompress receipts: %w", err)
	}
	// Empty block — zero receipts.
	if len(raw) == 0 {
		return nil, nil
	}

	// Top-level: list of receipts.
	var receiptRaws []rlp.RawValue
	if err := rlp.DecodeBytes(raw, &receiptRaws); err != nil {
		return nil, fmt.Errorf("ethel: decode receipts list: %w", err)
	}

	receipts := make(block.Receipts, 0, len(receiptRaws))
	for i, rRaw := range receiptRaws {
		r, err := decodeGethReceipt(rRaw)
		if err != nil {
			return nil, fmt.Errorf("ethel: receipt %d: %w", i, err)
		}
		receipts = append(receipts, r)
	}
	return receipts, nil
}

func decodeGethReceipt(data []byte) (*block.Receipt, error) {
	r := &block.Receipt{}

	payload := []byte(data)

	// Typed receipt: RLP string wrapping type || rlp(fields).
	if len(payload) > 0 && payload[0] >= 0x80 && payload[0] < 0xc0 {
		var inner []byte
		if err := rlp.DecodeBytes(payload, &inner); err != nil {
			return nil, fmt.Errorf("unwrap typed receipt: %w", err)
		}
		payload = inner
	}

	// Check for type prefix (non-RLP byte < 0x80).
	if len(payload) > 0 && payload[0] < 0x80 {
		r.Type = payload[0]
		payload = payload[1:]
	}

	// Geth ReceiptForStorage format: [PostStateOrStatus, CumulativeGas, Logs]
	// Note: Bloom is NOT stored (recomputed from logs). Only 3 fields.
	var elems []rlp.RawValue
	if err := rlp.DecodeBytes(payload, &elems); err != nil {
		return nil, fmt.Errorf("decode receipt fields: %w", err)
	}
	if len(elems) < 3 {
		return nil, fmt.Errorf("receipt has %d fields, need >= 3", len(elems))
	}

	// Field 0: status or post-state root.
	var statusBytes []byte
	if err := rlp.DecodeBytes(elems[0], &statusBytes); err != nil {
		return nil, fmt.Errorf("decode status: %w", err)
	}
	if len(statusBytes) <= 1 {
		// Post-Byzantium status.
		if len(statusBytes) == 1 && statusBytes[0] == 1 {
			r.Status = block.ReceiptStatusSuccessful
		} else {
			r.Status = block.ReceiptStatusFailed
		}
	} else {
		// Pre-Byzantium: post-state root (32 bytes). No status concept,
		// all txs "succeed". PostState not stored in compact format
		// (same as Reth) — recoverable via re-execution.
		r.Status = block.ReceiptStatusSuccessful
	}

	// Field 1: cumulativeGasUsed.
	if err := rlp.DecodeBytes(elems[1], &r.CumulativeGasUsed); err != nil {
		return nil, fmt.Errorf("decode cumulativeGas: %w", err)
	}

	// Field 2: logs (Geth ReceiptForStorage stores logs as LogForStorage).
	var logRaws []rlp.RawValue
	if err := rlp.DecodeBytes(elems[2], &logRaws); err != nil {
		return nil, fmt.Errorf("decode logs list: %w", err)
	}
	r.Logs = make([]*block.Log, len(logRaws))
	for i, lRaw := range logRaws {
		l, err := decodeGethLog(lRaw)
		if err != nil {
			return nil, fmt.Errorf("log %d: %w", i, err)
		}
		r.Logs[i] = l
	}

	return r, nil
}

func decodeGethLog(data []byte) (*block.Log, error) {
	// Log: [address, topics, data]
	var elems []rlp.RawValue
	if err := rlp.DecodeBytes(data, &elems); err != nil {
		return nil, fmt.Errorf("decode log: %w", err)
	}
	if len(elems) < 3 {
		return nil, fmt.Errorf("log has %d fields, need 3", len(elems))
	}

	l := &block.Log{}
	if err := rlp.DecodeBytes(elems[0], &l.Address); err != nil {
		return nil, fmt.Errorf("decode log address: %w", err)
	}
	if err := rlp.DecodeBytes(elems[1], &l.Topics); err != nil {
		return nil, fmt.Errorf("decode log topics: %w", err)
	}
	if err := rlp.DecodeBytes(elems[2], &l.Data); err != nil {
		return nil, fmt.Errorf("decode log data: %w", err)
	}
	return l, nil
}
