// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestPayloadID_MarshalUnmarshalText(t *testing.T) {
	id := PayloadID{1, 2, 3, 4, 5, 6, 7, 8}
	text, err := id.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText() error = %v", err)
	}

	var got PayloadID
	if err := got.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText() error = %v", err)
	}
	if got != id {
		t.Fatalf("UnmarshalText() = %v, want %v", got, id)
	}

	if err := got.UnmarshalText([]byte("0x01")); err == nil {
		t.Fatal("UnmarshalText(short) error = nil, want errInvalidPayloadID")
	}
}

func TestCalcExcessBlobGasAndFee(t *testing.T) {
	if got := CalcExcessBlobGas(0, 0); got != 0 {
		t.Fatalf("CalcExcessBlobGas(0,0) = %d, want 0", got)
	}
	if got := CalcBlobFee(0); got != 1 {
		t.Fatalf("CalcBlobFee(0) = %d, want 1 (min fee)", got)
	}
}

func TestEngineErrorTypes(t *testing.T) {
	if errPayloadNotFound.Error() != "payload not found" {
		t.Fatalf("errPayloadNotFound.Error() = %q", errPayloadNotFound.Error())
	}
	if errBlobHashCountMismatch.Error() == "" {
		t.Fatal("errBlobHashCountMismatch.Error() empty")
	}
	if errBlobHashMismatch.Error() == "" {
		t.Fatal("errBlobHashMismatch.Error() empty")
	}
	if errInvalidPayloadID.Error() == "" {
		t.Fatal("errInvalidPayloadID.Error() empty")
	}

	ip := &engineInvalidParamsError{"bad params"}
	if ip.Error() != "bad params" || ip.ErrorCode() != -32602 {
		t.Fatalf("engineInvalidParamsError = %q/%d", ip.Error(), ip.ErrorCode())
	}
	tl := &engineTooLargeRequestError{"too large"}
	if tl.Error() != "too large" || tl.ErrorCode() != -38004 {
		t.Fatalf("engineTooLargeRequestError = %q/%d", tl.Error(), tl.ErrorCode())
	}
	fcs := &engineInvalidForkchoiceStateError{"bad fcs"}
	if fcs.Error() != "bad fcs" || fcs.ErrorCode() != -38002 {
		t.Fatalf("engineInvalidForkchoiceStateError = %q/%d", fcs.Error(), fcs.ErrorCode())
	}
	pa := &engineInvalidPayloadAttributesError{"bad attrs"}
	if pa.Error() != "bad attrs" || pa.ErrorCode() != -38003 {
		t.Fatalf("engineInvalidPayloadAttributesError = %q/%d", pa.Error(), pa.ErrorCode())
	}
}

func TestEngineAPIBlob_GetBlobsBundleV1_NotFound(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	api := &API{bc: newPreciseChainStub(cfg, nil, genesis), chainConfig: cfg, engineOverlay: newEngineOverlay()}
	e := NewEngineAPIBlob(NewBlockChainAPI(api))

	if _, err := e.GetBlobsBundleV1(context.Background(), PayloadID{}); err == nil {
		t.Fatal("GetBlobsBundleV1(unknown) error = nil, want errPayloadNotFound")
	}
}
