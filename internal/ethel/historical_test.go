// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	_ "github.com/n42blockchain/N42/modules" // register tables
	"github.com/n42blockchain/N42/params"
)

func TestHistoricalStateGetBalanceUnknownAccountIsZero(t *testing.T) {
	db := memdb.NewTestDB(t)
	hs := NewHistoricalState(db, params.EthereumMainnetChainConfig, nil)

	bal, err := hs.GetBalance(types.HexToAddress("0xabcd"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if bal == nil || !bal.IsZero() {
		t.Fatalf("GetBalance for unknown account = %v, want zero", bal)
	}
}

func TestHistoricalStateGetStorageAtUnknownAccountIsNil(t *testing.T) {
	db := memdb.NewTestDB(t)
	hs := NewHistoricalState(db, params.EthereumMainnetChainConfig, nil)

	val, err := hs.GetStorageAt(types.HexToAddress("0xabcd"), types.HexToHash("0x01"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Fatalf("GetStorageAt for unknown account = %x, want nil", val)
	}
}

func TestHistoricalStateGetAccountUnknownIsNil(t *testing.T) {
	db := memdb.NewTestDB(t)
	hs := NewHistoricalState(db, params.EthereumMainnetChainConfig, nil)

	acc, err := hs.GetAccount(types.HexToAddress("0xabcd"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if acc != nil {
		t.Fatalf("GetAccount for unknown account = %+v, want nil", acc)
	}
}

func TestHistoricalStateCallHeaderNotFoundErrors(t *testing.T) {
	db := memdb.NewTestDB(t)
	hs := NewHistoricalState(db, params.EthereumMainnetChainConfig, nil)

	_, _, err := hs.Call(types.HexToAddress("0x01"), nil, 0, nil, nil, 5)
	if err == nil {
		t.Fatal("expected error for a block with no canonical header")
	}
}
