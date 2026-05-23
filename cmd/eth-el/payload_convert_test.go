// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.1.1.b converter tests — Eth1Block → api.ExecutionPayloadV4
// field-by-field round-trip checks. Pure functions; no I/O. These tests
// pin every byte of the wire-shape conversion so a regression in
// caplin's cltypes layout (extra fields, renamed slot, type aliasing
// drift) breaks the build instead of silently producing a malformed
// payload.

//go:build n42el

package main

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/phase1/execution_client"
)

func TestExtractBaseFeePerGasUint64_Roundtrip(t *testing.T) {
	cases := []uint64{
		0,
		1,
		1_000_000_000,        // 1 gwei
		100_000_000_000_000,  // 100 mwei-of-gas-style scale
		^uint64(0),           // max uint64
	}
	for _, want := range cases {
		var b32 [32]byte
		// little-endian: low byte first
		b32[0] = byte(want)
		b32[1] = byte(want >> 8)
		b32[2] = byte(want >> 16)
		b32[3] = byte(want >> 24)
		b32[4] = byte(want >> 32)
		b32[5] = byte(want >> 40)
		b32[6] = byte(want >> 48)
		b32[7] = byte(want >> 56)

		got, err := extractBaseFeePerGasUint64(b32)
		if err != nil {
			t.Errorf("want=%d: err=%v", want, err)
			continue
		}
		if uint64(got) != want {
			t.Errorf("want=%d got=%d", want, uint64(got))
		}
	}
}

func TestExtractBaseFeePerGasUint64_OverflowRejected(t *testing.T) {
	var b32 [32]byte
	b32[0] = 0xff
	b32[8] = 0x01 // bit 64 set → must reject
	if _, err := extractBaseFeePerGasUint64(b32); err == nil {
		t.Fatalf("expected overflow error; got nil")
	}
}

func TestEth1BlockToExecutionPayloadV4_NilRejected(t *testing.T) {
	if _, err := eth1BlockToExecutionPayloadV4(nil); err == nil {
		t.Fatalf("expected error on nil input")
	}
}

func TestEth1BlockToExecutionPayloadV4_FieldMapping(t *testing.T) {
	beaconCfg := &clparams.MainnetBeaconConfig
	blk := cltypes.NewEth1Block(clparams.ElectraVersion, beaconCfg)
	blk.ParentHash = depcommon.Hash{0x11}
	blk.FeeRecipient = depcommon.Address{0x22}
	blk.StateRoot = depcommon.Hash{0x33}
	blk.ReceiptsRoot = depcommon.Hash{0x44}
	for i := range blk.LogsBloom {
		blk.LogsBloom[i] = byte(i & 0xff)
	}
	blk.PrevRandao = depcommon.Hash{0x55}
	blk.BlockNumber = 25_101_867
	blk.GasLimit = 30_000_000
	blk.GasUsed = 12_345_678
	blk.Time = 1747_000_000
	blk.Extra = solid.NewExtraData()
	blk.Extra.SetBytes([]byte("n42-eth-el"))
	// BaseFeePerGas: 1 gwei in little-endian.
	blk.BaseFeePerGas[0] = 0x00
	blk.BaseFeePerGas[1] = 0xca
	blk.BaseFeePerGas[2] = 0x9a
	blk.BaseFeePerGas[3] = 0x3b
	blk.BlockHash = depcommon.Hash{0x66}
	blk.Transactions = solid.NewTransactionsSSZFromTransactions(
		[][]byte{{0xaa, 0xbb, 0xcc}, {0xde, 0xad}},
	)
	blk.BlobGasUsed = 131072
	blk.ExcessBlobGas = 0

	payload, err := eth1BlockToExecutionPayloadV4(blk)
	if err != nil {
		t.Fatalf("conversion: %v", err)
	}

	if payload.ParentHash != (types.Hash{0x11}) {
		t.Errorf("ParentHash mismatch: got %x", payload.ParentHash)
	}
	if payload.FeeRecipient != (types.Address{0x22}) {
		t.Errorf("FeeRecipient mismatch: got %x", payload.FeeRecipient)
	}
	if payload.StateRoot != (types.Hash{0x33}) {
		t.Errorf("StateRoot mismatch: got %x", payload.StateRoot)
	}
	if payload.ReceiptsRoot != (types.Hash{0x44}) {
		t.Errorf("ReceiptsRoot mismatch: got %x", payload.ReceiptsRoot)
	}
	if payload.PrevRandao != (types.Hash{0x55}) {
		t.Errorf("PrevRandao mismatch: got %x", payload.PrevRandao)
	}
	if uint64(payload.BlockNumber) != 25_101_867 {
		t.Errorf("BlockNumber: got %d", uint64(payload.BlockNumber))
	}
	if uint64(payload.GasLimit) != 30_000_000 {
		t.Errorf("GasLimit: got %d", uint64(payload.GasLimit))
	}
	if uint64(payload.GasUsed) != 12_345_678 {
		t.Errorf("GasUsed: got %d", uint64(payload.GasUsed))
	}
	if uint64(payload.Timestamp) != 1747_000_000 {
		t.Errorf("Timestamp: got %d", uint64(payload.Timestamp))
	}
	if string(payload.ExtraData) != "n42-eth-el" {
		t.Errorf("ExtraData: got %q", string(payload.ExtraData))
	}
	if uint64(payload.BaseFeePerGas) != 1_000_000_000 {
		t.Errorf("BaseFeePerGas: got %d (want 1 gwei)", uint64(payload.BaseFeePerGas))
	}
	if payload.BlockHash != (types.Hash{0x66}) {
		t.Errorf("BlockHash mismatch: got %x", payload.BlockHash)
	}
	if len(payload.Transactions) != 2 {
		t.Fatalf("Transactions count: got %d, want 2", len(payload.Transactions))
	}
	if !bytes.Equal(payload.Transactions[0], []byte{0xaa, 0xbb, 0xcc}) {
		t.Errorf("Transactions[0]: got %x", []byte(payload.Transactions[0]))
	}
	if !bytes.Equal(payload.Transactions[1], []byte{0xde, 0xad}) {
		t.Errorf("Transactions[1]: got %x", []byte(payload.Transactions[1]))
	}

	// LogsBloom must be a 256-byte copy with monotonic content
	if len(payload.LogsBloom) != 256 {
		t.Fatalf("LogsBloom length: got %d, want 256", len(payload.LogsBloom))
	}
	for i, b := range payload.LogsBloom {
		if b != byte(i&0xff) {
			t.Errorf("LogsBloom[%d]: got %x", i, b)
		}
	}

	if payload.BlobGasUsed == nil || uint64(*payload.BlobGasUsed) != 131072 {
		t.Errorf("BlobGasUsed mismatch: %v", payload.BlobGasUsed)
	}
	if payload.ExcessBlobGas == nil || uint64(*payload.ExcessBlobGas) != 0 {
		t.Errorf("ExcessBlobGas mismatch: %v", payload.ExcessBlobGas)
	}

	// Ensure ExtraData is owned (not aliased) — mutating the source must
	// not corrupt the output.
	blk.Extra.SetBytes([]byte("CORRUPTED"))
	if string(payload.ExtraData) != "n42-eth-el" {
		t.Errorf("ExtraData was aliased to source; mutation leaked: %q", string(payload.ExtraData))
	}
}

func TestEth1BlockToExecutionPayloadV4_NoTransactions(t *testing.T) {
	beaconCfg := &clparams.MainnetBeaconConfig
	blk := cltypes.NewEth1Block(clparams.ElectraVersion, beaconCfg)
	blk.Transactions = solid.NewTransactionsSSZFromTransactions(nil)

	payload, err := eth1BlockToExecutionPayloadV4(blk)
	if err != nil {
		t.Fatalf("conversion: %v", err)
	}
	if len(payload.Transactions) != 0 {
		t.Errorf("expected 0 txs; got %d", len(payload.Transactions))
	}
}

func TestN42HeaderToDepshim_NilInput(t *testing.T) {
	if got := n42HeaderToDepshim(nil); got != nil {
		t.Errorf("expected nil output for nil input; got %v", got)
	}
}

func TestMapPayloadStatus_AllBranches(t *testing.T) {
	cases := []struct {
		name   string
		input  *api.PayloadStatusV1
		want   execution_client.PayloadStatus
	}{
		{"nil", nil, execution_client.PayloadStatusNotValidated},
		{"VALID", &api.PayloadStatusV1{Status: api.PayloadStatusValid}, execution_client.PayloadStatusValidated},
		{"INVALID", &api.PayloadStatusV1{Status: api.PayloadStatusInvalid}, execution_client.PayloadStatusInvalidated},
		{"SYNCING", &api.PayloadStatusV1{Status: api.PayloadStatusSyncing}, execution_client.PayloadStatusNotValidated},
		{"ACCEPTED", &api.PayloadStatusV1{Status: api.PayloadStatusAccepted}, execution_client.PayloadStatusNotValidated},
		{"unknown", &api.PayloadStatusV1{Status: "WAT"}, execution_client.PayloadStatusNone},
	}
	for _, c := range cases {
		if got := mapPayloadStatus(c.input); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestN42HeaderToDepshim_FieldMapping(t *testing.T) {
	wRoot := types.Hash{0x77}
	parentBeacon := types.Hash{0x88}
	reqHash := types.Hash{0x99}
	blobGas := uint64(131072)
	excess := uint64(0)
	in := &block.Header{
		ParentHash:       types.Hash{0x11},
		UncleHash:        types.Hash{0x12},
		Coinbase:         types.Address{0x13},
		Root:             types.Hash{0x14},
		TxHash:           types.Hash{0x15},
		ReceiptHash:      types.Hash{0x16},
		Bloom:            block.Bloom{0x17},
		Difficulty:       uint256.NewInt(0),
		Number:           uint256.NewInt(25_101_866),
		GasLimit:         30_000_000,
		GasUsed:          12_345,
		Time:             1747_000_000,
		Extra:            []byte("vanity"),
		MixDigest:        types.Hash{0x18},
		Nonce:            block.EncodeNonce(0),
		BaseFee:          uint256.NewInt(7_500_000_000),
		WithdrawalsHash:  &wRoot,
		BlobGasUsed:      &blobGas,
		ExcessBlobGas:    &excess,
		ParentBeaconRoot: &parentBeacon,
		RequestsHash:     &reqHash,
	}

	out := n42HeaderToDepshim(in)
	if out == nil {
		t.Fatalf("got nil; want a Header")
	}
	if depcommon.Hash(in.ParentHash) != out.ParentHash {
		t.Errorf("ParentHash mismatch")
	}
	if depcommon.Address(in.Coinbase) != out.Coinbase {
		t.Errorf("Coinbase mismatch: %x vs %x", in.Coinbase, out.Coinbase)
	}
	if out.GasLimit != 30_000_000 || out.GasUsed != 12_345 || out.Time != 1747_000_000 {
		t.Errorf("gas/time mismatch")
	}
	if string(out.Extra) != "vanity" {
		t.Errorf("Extra: %q", out.Extra)
	}
	if out.Number.Uint64() != 25_101_866 {
		t.Errorf("Number: %d", out.Number.Uint64())
	}
	if out.BaseFee == nil || out.BaseFee.Uint64() != 7_500_000_000 {
		t.Errorf("BaseFee: %v", out.BaseFee)
	}
	if out.WithdrawalsHash == nil || depcommon.Hash(*in.WithdrawalsHash) != *out.WithdrawalsHash {
		t.Errorf("WithdrawalsHash mismatch")
	}
	if out.BlobGasUsed == nil || *out.BlobGasUsed != 131072 {
		t.Errorf("BlobGasUsed: %v", out.BlobGasUsed)
	}
	if out.ParentBeaconBlockRoot == nil || depcommon.Hash(*in.ParentBeaconRoot) != *out.ParentBeaconBlockRoot {
		t.Errorf("ParentBeaconBlockRoot mismatch")
	}
	if out.RequestsHash == nil || depcommon.Hash(*in.RequestsHash) != *out.RequestsHash {
		t.Errorf("RequestsHash mismatch")
	}

	// Extra ownership: mutating source must not corrupt output.
	in.Extra[0] = 'X'
	if string(out.Extra) == "Xanity" {
		t.Errorf("Extra was aliased to source slice")
	}
}

func TestEthELBackend_ExecutePayload_ExecProviderMissing(t *testing.T) {
	// nil executionProvider must surface errExecutionLayerNotReady,
	// NOT panic. This is the Phase 7.1.1.b safety net for code paths
	// that only wire the read-only chaindbProvider.
	b := newEthELBackend(nil) // no exec provider
	if err := b.initAPIs(); err == nil {
		t.Fatalf("expected error when execution provider missing; got nil")
	}
}
