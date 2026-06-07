// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Licensed under the GNU Lesser General Public License v3. See the upstream
// erigon header in state_accessors.go.

// Adapted from erigon cl/persistence/state/slot_data_test.go's ReadSlotData
// path, using a mock GetValFn instead of initial_state/chainspec so the test is
// self-contained (the cfg arg is unused by SlotData.ReadFrom).

package state_accessors

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/persistence/base_encoding"
	"github.com/n42blockchain/N42/lib/kv"
)

func TestReadSlotDataRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		sd   *SlotData
	}{
		{
			name: "Electra",
			sd: &SlotData{
				Version:                       clparams.ElectraVersion,
				Eth1Data:                      &cltypes.Eth1Data{},
				Eth1DepositIndex:              10,
				NextWithdrawalIndex:           20,
				NextWithdrawalValidatorIndex:  30,
				DepositRequestsStartIndex:     40,
				DepositBalanceToConsume:       50,
				ExitBalanceToConsume:          60,
				EarliestExitEpoch:             70,
				ConsolidationBalanceToConsume: 80,
				EarliestConsolidationEpoch:    90,
				Fork:                          &cltypes.Fork{Epoch: 1},
			},
		},
		{
			name: "Gloas",
			sd: &SlotData{
				Version:                    clparams.GloasVersion,
				Eth1Data:                   &cltypes.Eth1Data{},
				Eth1DepositIndex:           10,
				NextWithdrawalBuilderIndex: 123456,
				LatestBlockHash:            common.HexToHash("0xabcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"),
				Fork:                       &cltypes.Fork{Epoch: 2},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			require.NoError(t, tc.sd.WriteTo(&buf))

			slot := uint64(1000)
			key := base_encoding.Encode64ToBytes4(slot)
			serialized := buf.Bytes()

			getFn := func(table string, k []byte) ([]byte, error) {
				if table == kv.SlotData && bytes.Equal(k, key) {
					return serialized, nil
				}
				return nil, nil
			}

			got, err := ReadSlotData(getFn, slot, nil)
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Equal(t, tc.sd, got)
		})
	}
}

// A miss (no row) returns (nil, nil) — the empty-snapshot/DB path.
func TestReadSlotDataMissReturnsNil(t *testing.T) {
	getFn := func(table string, k []byte) ([]byte, error) { return nil, nil }
	got, err := ReadSlotData(getFn, 42, nil)
	require.NoError(t, err)
	require.Nil(t, got)
}
