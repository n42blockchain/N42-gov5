// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

func apiTNewIntraBlockState(t *testing.T) *state.IntraBlockState {
	t.Helper()
	bca := setupStorageTestAPI(t)
	var ibs *state.IntraBlockState
	err := bca.api.db.View(context.Background(), func(tx kv.Tx) error {
		reader := state.NewPlainState(tx, 0)
		ibs = state.New(reader)
		return nil
	})
	if err != nil {
		t.Fatalf("build IntraBlockState: %v", err)
	}
	return ibs
}

func TestStateOverride_Apply_Nil(t *testing.T) {
	var diff *StateOverride
	if err := diff.Apply(nil); err != nil {
		t.Fatalf("Apply(nil diff) error = %v", err)
	}
}

func TestStateOverride_Apply_Fields(t *testing.T) {
	ibs := apiTNewIntraBlockState(t)

	addr := avmcommon.Address(types.HexToAddress("0xcccccccccccccccccccccccccccccccccccccc"))
	nonce := hexutil.Uint64(5)
	code := hexutil.Bytes{0x60, 0x00}
	balanceBig := (*hexutil.Big)(big.NewInt(100))

	diff := StateOverride{
		addr: OverrideAccount{
			Nonce:   &nonce,
			Code:    &code,
			Balance: &balanceBig,
		},
	}
	if err := diff.Apply(ibs); err != nil {
		t.Fatalf("Apply(nonce/code/balance) error = %v", err)
	}
}

func TestStateOverride_Apply_StateAndStateDiffConflict(t *testing.T) {
	ibs := apiTNewIntraBlockState(t)
	addr := avmcommon.Address(types.HexToAddress("0xdddddddddddddddddddddddddddddddddddddd"))

	stateMap := map[avmcommon.Hash]avmcommon.Hash{}
	stateDiffMap := map[avmcommon.Hash]avmcommon.Hash{}

	diff := StateOverride{
		addr: OverrideAccount{
			StatsPrint: &stateMap,
			StateDiff:  &stateDiffMap,
		},
	}
	if err := diff.Apply(ibs); err == nil {
		t.Fatal("Apply(state + stateDiff both set) error = nil, want conflict error")
	}
}

func TestStateOverride_Apply_StateDiffOnly(t *testing.T) {
	ibs := apiTNewIntraBlockState(t)
	addr := avmcommon.Address(types.HexToAddress("0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"))

	key := avmcommon.Hash(types.HexToHash("0x01"))
	val := avmcommon.Hash(types.HexToHash("0x02"))
	stateDiffMap := map[avmcommon.Hash]avmcommon.Hash{key: val}

	diff := StateOverride{
		addr: OverrideAccount{
			StateDiff: &stateDiffMap,
		},
	}
	if err := diff.Apply(ibs); err != nil {
		t.Fatalf("Apply(stateDiff only) error = %v", err)
	}
}

func TestStateOverride_Apply_StatePrintOnly(t *testing.T) {
	ibs := apiTNewIntraBlockState(t)
	addr := avmcommon.Address(types.HexToAddress("0xffffffffffffffffffffffffffffffffffffff"))

	key := avmcommon.Hash(types.HexToHash("0x01"))
	val := avmcommon.Hash(types.HexToHash("0x02"))
	stateMap := map[avmcommon.Hash]avmcommon.Hash{key: val}

	diff := StateOverride{
		addr: OverrideAccount{
			StatsPrint: &stateMap,
		},
	}
	if err := diff.Apply(ibs); err != nil {
		t.Fatalf("Apply(state only) error = %v", err)
	}
}
