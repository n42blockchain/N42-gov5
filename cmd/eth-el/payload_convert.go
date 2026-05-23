// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.1.1.b — type conversion between caplin's cltypes.Eth1Block and
// the N42 api.ExecutionPayloadV{1..4} family that backs the real
// internal/api.EngineAPIv4 production code path.
//
// Pure functions; no I/O. Tests in payload_convert_test.go exercise
// every helper with synthetic Eth1Block instances so the converter can
// be tightened without spinning up an EL.

//go:build n42el

package main

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
)

// extractBaseFeePerGasUint64 converts the cltypes BaseFeePerGas (32 B
// little-endian Hash — i.e. high-order bytes at the END) to the
// uint64 form api.ExecutionPayloadV1.BaseFeePerGas expects.
//
// We refuse to silently truncate: a baseFee that doesn't fit in 64 bits
// is treated as a hard error so production fails loud instead of
// quietly applying the wrong fee. EIP-1559 base fees observed on
// mainnet have stayed well under uint64.max (≈18 ETH/gas), so this
// guard never trips in practice.
func extractBaseFeePerGasUint64(b32 [32]byte) (hexutil.Uint64, error) {
	// b32 is little-endian: low byte at index 0, high byte at index 31.
	// Bytes [8..31] (high 24 bytes) must be zero for the value to
	// fit in a uint64.
	for i := 8; i < 32; i++ {
		if b32[i] != 0 {
			return 0, fmt.Errorf("eth-el: baseFeePerGas > uint64.max: %x", b32[:])
		}
	}
	var v uint64
	for i := 7; i >= 0; i-- {
		v = (v << 8) | uint64(b32[i])
	}
	return hexutil.Uint64(v), nil
}

// baseFeePerGasBigInt is the precision-preserving variant of
// extractBaseFeePerGasUint64 — useful when callers want the full 256-bit
// fee for tests or for log formatting. Production NewPayload path
// uses the uint64 form to match api.ExecutionPayloadV1.
func baseFeePerGasBigInt(b32 [32]byte) *big.Int {
	// reverse to big-endian
	be := make([]byte, 32)
	for i := 0; i < 32; i++ {
		be[31-i] = b32[i]
	}
	return new(big.Int).SetBytes(be)
}

// eth1BlockToExecutionPayloadV4 converts caplin's cltypes.Eth1Block
// into the api.ExecutionPayloadV4 form NewPayloadV4 expects (Pectra+).
// Current mainnet (block ≥22,431,084 as of 2026-05) is Pectra, so this
// covers every block the archive replay will hit during catch-up.
//
// Earlier-fork blocks (V1 Paris, V2 Shanghai, V3 Cancun) need separate
// converters — left as Phase 7.1.1.c follow-up.
//
// The function does NOT populate Pectra Deposit/Withdrawal/Consolidation
// request fields — those come from the separate executionRequests
// parameter that Caplin passes alongside the payload and are validated
// by api.validateExecutionRequests inside NewPayloadV4.
func eth1BlockToExecutionPayloadV4(blk *cltypes.Eth1Block) (*api.ExecutionPayloadV4, error) {
	if blk == nil {
		return nil, errors.New("eth1BlockToExecutionPayloadV4: nil block")
	}

	// Raw RLP transactions. Empty list is valid on slots with no txs.
	var rawTxs []hexutil.Bytes
	if blk.Transactions != nil {
		rawList := blk.Transactions.UnderlyngReference()
		rawTxs = make([]hexutil.Bytes, len(rawList))
		for i, raw := range rawList {
			rawTxs[i] = hexutil.Bytes(append([]byte(nil), raw...))
		}
	}

	// Withdrawals. Capella+ ships them inside Eth1Block; pre-Capella
	// blocks have a nil Withdrawals (caller should use V1 converter).
	var ws []*api.Withdrawal
	if blk.Withdrawals != nil {
		ws = make([]*api.Withdrawal, 0, blk.Withdrawals.Len())
		blk.Withdrawals.Range(func(_ int, w *cltypes.Withdrawal, _ int) bool {
			ws = append(ws, &api.Withdrawal{
				Index:          hexutil.Uint64(w.Index),
				ValidatorIndex: hexutil.Uint64(w.Validator),
				Address:        types.Address(w.Address),
				Amount:         hexutil.Uint64(w.Amount),
			})
			return true
		})
	}

	baseFee, err := extractBaseFeePerGasUint64(blk.BaseFeePerGas)
	if err != nil {
		return nil, fmt.Errorf("baseFeePerGas: %w", err)
	}

	var extra []byte
	if blk.Extra != nil {
		// solid.ExtraData.Bytes() returns a borrowed slice; copy so the
		// payload doesn't alias caplin's buffer.
		src := blk.Extra.Bytes()
		extra = append([]byte(nil), src...)
	}

	blobGasUsed := hexutil.Uint64(blk.BlobGasUsed)
	excessBlobGas := hexutil.Uint64(blk.ExcessBlobGas)

	return &api.ExecutionPayloadV4{
		ParentHash:    types.Hash(blk.ParentHash),
		FeeRecipient:  types.Address(blk.FeeRecipient),
		StateRoot:     types.Hash(blk.StateRoot),
		ReceiptsRoot:  types.Hash(blk.ReceiptsRoot),
		LogsBloom:     hexutil.Bytes(append([]byte(nil), blk.LogsBloom[:]...)),
		PrevRandao:    types.Hash(blk.PrevRandao),
		BlockNumber:   hexutil.Uint64(blk.BlockNumber),
		GasLimit:      hexutil.Uint64(blk.GasLimit),
		GasUsed:       hexutil.Uint64(blk.GasUsed),
		Timestamp:     hexutil.Uint64(blk.Time),
		ExtraData:     hexutil.Bytes(extra),
		BaseFeePerGas: baseFee,
		BlockHash:     types.Hash(blk.BlockHash),
		Transactions:  rawTxs,
		Withdrawals:   ws,
		BlobGasUsed:   &blobGasUsed,
		ExcessBlobGas: &excessBlobGas,
		// Pectra request fields are passed alongside via executionRequests
		// parameter to NewPayloadV4; we don't materialise them here.
	}, nil
}
