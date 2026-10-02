// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestOtterscanAPI_GetApiLevelAndNew(t *testing.T) {
	bca := setupStorageTestAPI(t)
	o := NewOtterscanAPI(bca.api)
	if o.GetApiLevel() != OtterscanAPILevel {
		t.Fatalf("GetApiLevel() = %d, want %d", o.GetApiLevel(), OtterscanAPILevel)
	}
}

func TestOtterscanAPI_HasCode(t *testing.T) {
	bca := setupStorageTestAPI(t)
	o := NewOtterscanAPI(bca.api)

	contract := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	bnh := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)

	has, err := o.HasCode(context.Background(), contract, bnh)
	if err != nil {
		t.Fatalf("HasCode() error = %v", err)
	}
	// Genesis alloc has no code for this address, only storage.
	if has {
		t.Fatal("HasCode() = true, want false (no code set)")
	}
}

func TestOtterscanAPI_GetBlockDetails(t *testing.T) {
	bca := setupStorageTestAPI(t)
	o := NewOtterscanAPI(bca.api)

	details, err := o.GetBlockDetails(context.Background(), jsonrpc.BlockNumber(0))
	if err != nil {
		t.Fatalf("GetBlockDetails(0) error = %v", err)
	}
	if details == nil || details.Block == nil {
		t.Fatal("GetBlockDetails(0) returned nil block map")
	}
	if got, ok := details.Block["number"].(hexutil.Uint64); !ok || uint64(got) != 0 {
		t.Fatalf("GetBlockDetails(0) number = %v, want 0", details.Block["number"])
	}
}
