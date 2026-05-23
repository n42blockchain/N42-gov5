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
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/engineapi/engine_types"
	dephexutil "github.com/n42blockchain/N42/internal/cl/depshim/hexutil"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
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

// depBlockToExecutionPayloadV4 converts caplin's depshim/types.Block
// (used by the InsertBlock(s) historical-import path) into the
// api.ExecutionPayloadV4 form NewPayloadV4 expects.
//
// The conversion mirrors eth1BlockToExecutionPayloadV4 but pulls data
// from a depshim.Block's HeaderField + Transactions + Withdrawals
// rather than a cltypes.Eth1Block. Caplin's block_collector ships
// historical blocks in this shape during backfill.
//
// Pre-Cancun blocks miss BlobGasUsed / ExcessBlobGas at the header
// level; we leave those as zero in the payload (NewPayloadV4 doesn't
// reject zero blob gas, only mismatched).
func depBlockToExecutionPayloadV4(blk *deptypes.Block) (*api.ExecutionPayloadV4, error) {
	if blk == nil {
		return nil, errors.New("depBlockToExecutionPayloadV4: nil block")
	}
	h := blk.Header()
	if h == nil {
		return nil, errors.New("depBlockToExecutionPayloadV4: nil header")
	}

	// Raw txs
	rawTxs := make([]hexutil.Bytes, len(blk.Transactions))
	for i, raw := range blk.Transactions {
		rawTxs[i] = hexutil.Bytes(append([]byte(nil), raw...))
	}

	// Withdrawals
	var ws []*api.Withdrawal
	if len(blk.Withdrawals) > 0 {
		ws = make([]*api.Withdrawal, len(blk.Withdrawals))
		for i, w := range blk.Withdrawals {
			ws[i] = &api.Withdrawal{
				Index:          hexutil.Uint64(w.Index),
				ValidatorIndex: hexutil.Uint64(w.Validator),
				Address:        types.Address(w.Address),
				Amount:         hexutil.Uint64(w.Amount),
			}
		}
	}

	// BaseFee — depshim uses *uint256.Int (pointer); fits uint64 in
	// practice (verified in extractBaseFeePerGasUint64 path).
	var baseFee hexutil.Uint64
	if h.BaseFee != nil {
		if !h.BaseFee.IsUint64() {
			return nil, fmt.Errorf("baseFeePerGas > uint64.max: %s", h.BaseFee.String())
		}
		baseFee = hexutil.Uint64(h.BaseFee.Uint64())
	}

	// BlobGasUsed / ExcessBlobGas — Cancun+ only.
	var blobGasUsed, excessBlobGas *hexutil.Uint64
	if h.BlobGasUsed != nil {
		v := hexutil.Uint64(*h.BlobGasUsed)
		blobGasUsed = &v
	}
	if h.ExcessBlobGas != nil {
		v := hexutil.Uint64(*h.ExcessBlobGas)
		excessBlobGas = &v
	}

	return &api.ExecutionPayloadV4{
		ParentHash:    types.Hash(h.ParentHash),
		FeeRecipient:  types.Address(h.Coinbase),
		StateRoot:     types.Hash(h.Root),
		ReceiptsRoot:  types.Hash(h.ReceiptHash),
		LogsBloom:     hexutil.Bytes(append([]byte(nil), h.Bloom[:]...)),
		PrevRandao:    types.Hash(h.MixDigest),
		BlockNumber:   hexutil.Uint64(h.Number.Uint64()),
		GasLimit:      hexutil.Uint64(h.GasLimit),
		GasUsed:       hexutil.Uint64(h.GasUsed),
		Timestamp:     hexutil.Uint64(h.Time),
		ExtraData:     hexutil.Bytes(append([]byte(nil), h.Extra...)),
		BaseFeePerGas: baseFee,
		BlockHash:     types.Hash(h.Hash()),
		Transactions:  rawTxs,
		Withdrawals:   ws,
		BlobGasUsed:   blobGasUsed,
		ExcessBlobGas: excessBlobGas,
	}, nil
}

// --- Phase 7.4 reverse converters (api → caplin) -------------------------

// depAttrsToAPIv3 converts the depshim PayloadAttributes that Caplin
// passes to UpdateForkchoice into the api.PayloadAttributesV3 form
// EngineAPIBlob.ForkchoiceUpdatedV3 expects.
//
// V3 requires ParentBeaconBlockRoot non-nil; we trust the caller to
// pass it for Cancun+ slots (Caplin enforces this upstream).
// Withdrawals shape carries 1:1 because depshim.Withdrawal and
// api.Withdrawal mirror the EIP-4895 layout.
func depAttrsToAPIv3(in *engine_types.PayloadAttributes) *api.PayloadAttributesV3 {
	if in == nil {
		return nil
	}
	out := &api.PayloadAttributesV3{
		Timestamp:             hexutil.Uint64(in.Timestamp),
		PrevRandao:            types.Hash(in.PrevRandao),
		SuggestedFeeRecipient: types.Address(in.SuggestedFeeRecipient),
	}
	if in.Withdrawals != nil {
		out.Withdrawals = make([]*api.Withdrawal, len(in.Withdrawals))
		for i, w := range in.Withdrawals {
			out.Withdrawals[i] = &api.Withdrawal{
				Index:          hexutil.Uint64(w.Index),
				ValidatorIndex: hexutil.Uint64(w.ValidatorIndex),
				Address:        types.Address(w.Address),
				Amount:         hexutil.Uint64(w.Amount),
			}
		}
	}
	if in.ParentBeaconBlockRoot != nil {
		h := types.Hash(*in.ParentBeaconBlockRoot)
		out.ParentBeaconBlockRoot = &h
	}
	return out
}

// uint64ToBaseFeeBytes32 is the inverse of extractBaseFeePerGasUint64:
// converts a single base-fee uint64 back into the 32-byte
// little-endian wire form that cltypes.Eth1Block.BaseFeePerGas uses.
func uint64ToBaseFeeBytes32(v uint64) [32]byte {
	var out [32]byte
	out[0] = byte(v)
	out[1] = byte(v >> 8)
	out[2] = byte(v >> 16)
	out[3] = byte(v >> 24)
	out[4] = byte(v >> 32)
	out[5] = byte(v >> 40)
	out[6] = byte(v >> 48)
	out[7] = byte(v >> 56)
	return out
}

// executionPayloadV3ToEth1Block converts the api.ExecutionPayloadV3
// that EngineAPIBlob.GetPayloadV3 returns into a cltypes.Eth1Block,
// the form Caplin's builder cache stores.
//
// This is the inverse direction of eth1BlockToExecutionPayloadV4 (used
// during NewPayload). Used by GetAssembledBlock so the builder result
// flows back through Caplin's proposer path.
//
// LogsBloom + Transactions are deep-copied so the returned block
// doesn't alias the api response's buffers; downstream consumers may
// retain the Eth1Block while the api response goes out of scope.
func executionPayloadV3ToEth1Block(p *api.ExecutionPayloadV3) (*cltypes.Eth1Block, error) {
	if p == nil {
		return nil, errors.New("executionPayloadV3ToEth1Block: nil payload")
	}
	// V3 is Cancun (Deneb on the CL side).
	beaconCfg := &clparams.MainnetBeaconConfig
	blk := cltypes.NewEth1Block(clparams.DenebVersion, beaconCfg)
	blk.ParentHash = depcommon.Hash(p.ParentHash)
	blk.FeeRecipient = depcommon.Address(p.FeeRecipient)
	blk.StateRoot = depcommon.Hash(p.StateRoot)
	blk.ReceiptsRoot = depcommon.Hash(p.ReceiptsRoot)
	if len(p.LogsBloom) != len(blk.LogsBloom) {
		return nil, fmt.Errorf("logsBloom: got %d bytes, want %d", len(p.LogsBloom), len(blk.LogsBloom))
	}
	copy(blk.LogsBloom[:], p.LogsBloom)
	blk.PrevRandao = depcommon.Hash(p.PrevRandao)
	blk.BlockNumber = uint64(p.BlockNumber)
	blk.GasLimit = uint64(p.GasLimit)
	blk.GasUsed = uint64(p.GasUsed)
	blk.Time = uint64(p.Timestamp)
	blk.Extra = solid.NewExtraData()
	if len(p.ExtraData) > 0 {
		blk.Extra.SetBytes(append([]byte(nil), p.ExtraData...))
	}
	blk.BaseFeePerGas = uint64ToBaseFeeBytes32(uint64(p.BaseFeePerGas))
	blk.BlockHash = depcommon.Hash(p.BlockHash)

	// Transactions: deep-copy each entry so the SSZ wrapper owns
	// the bytes.
	rawTxs := make([][]byte, len(p.Transactions))
	for i, raw := range p.Transactions {
		rawTxs[i] = append([]byte(nil), raw...)
	}
	blk.Transactions = solid.NewTransactionsSSZFromTransactions(rawTxs)

	// Withdrawals: cltypes uses *solid.ListSSZ[*Withdrawal]; for
	// MainnetBeaconConfig the max is 16 per slot post-Capella.
	if p.Withdrawals != nil {
		const withdrawalsListSize = 16
		const withdrawalRecordSize = 44 // 8 + 8 + 20 + 8
		wlist := solid.NewStaticListSSZ[*cltypes.Withdrawal](withdrawalsListSize, withdrawalRecordSize)
		for _, w := range p.Withdrawals {
			wlist.Append(&cltypes.Withdrawal{
				Index:     uint64(w.Index),
				Validator: uint64(w.ValidatorIndex),
				Address:   depcommon.Address(w.Address),
				Amount:    uint64(w.Amount),
			})
		}
		blk.Withdrawals = wlist
	}

	if p.BlobGasUsed != nil {
		blk.BlobGasUsed = uint64(*p.BlobGasUsed)
	}
	if p.ExcessBlobGas != nil {
		blk.ExcessBlobGas = uint64(*p.ExcessBlobGas)
	}
	return blk, nil
}

// apiBlobsBundleToDepshim converts the api.BlobsBundleV1 produced by
// GetPayloadV3 to the depshim engine_types.BlobsBundle Caplin uses.
// The two structs share the wire layout but are distinct named types.
// Nil-safe.
func apiBlobsBundleToDepshim(in *api.BlobsBundleV1) *engine_types.BlobsBundle {
	if in == nil {
		return nil
	}
	out := &engine_types.BlobsBundle{
		Commitments: make([]dephexutil.Bytes, len(in.Commitments)),
		Proofs:      make([]dephexutil.Bytes, len(in.Proofs)),
		Blobs:       make([]dephexutil.Bytes, len(in.Blobs)),
	}
	for i, c := range in.Commitments {
		out.Commitments[i] = dephexutil.Bytes(append([]byte(nil), c...))
	}
	for i, p := range in.Proofs {
		out.Proofs[i] = dephexutil.Bytes(append([]byte(nil), p...))
	}
	for i, b := range in.Blobs {
		out.Blobs[i] = dephexutil.Bytes(append([]byte(nil), b...))
	}
	return out
}
