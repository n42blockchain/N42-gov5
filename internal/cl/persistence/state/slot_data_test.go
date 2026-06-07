// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

// Ported/adapted from erigon cl/persistence/state/slot_data_test.go. The
// upstream tests of ReadSlotData (DB path) depend on state_accessors.go +
// initial_state + chainspec, which arrive in a later step of the Caplin merge
// (#31). These cover the version-gated WriteTo/ReadFrom serialization directly —
// the part this leaf file owns. SlotData.ReadFrom ignores its cfg argument, so a
// nil config is fine here.

package state_accessors

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
)

func TestSlotDataElectraRoundTrip(t *testing.T) {
	m := &SlotData{
		Version:                       clparams.ElectraVersion,
		Eth1Data:                      &cltypes.Eth1Data{},
		Eth1DepositIndex:              1,
		NextWithdrawalIndex:           2,
		NextWithdrawalValidatorIndex:  3,
		DepositRequestsStartIndex:     4,
		DepositBalanceToConsume:       5,
		ExitBalanceToConsume:          6,
		EarliestExitEpoch:             7,
		ConsolidationBalanceToConsume: 8,
		EarliestConsolidationEpoch:    9,
		Fork:                          &cltypes.Fork{Epoch: 12},
	}
	var b bytes.Buffer
	require.NoError(t, m.WriteTo(&b))

	m2 := &SlotData{}
	require.NoError(t, m2.ReadFrom(&b, nil))
	require.Equal(t, m, m2)
}

func TestSlotDataGloasRoundTrip(t *testing.T) {
	m := &SlotData{
		Version:                       clparams.GloasVersion,
		Eth1Data:                      &cltypes.Eth1Data{},
		Eth1DepositIndex:              1,
		NextWithdrawalIndex:           2,
		NextWithdrawalValidatorIndex:  3,
		DepositRequestsStartIndex:     4,
		DepositBalanceToConsume:       5,
		ExitBalanceToConsume:          6,
		EarliestExitEpoch:             7,
		ConsolidationBalanceToConsume: 8,
		EarliestConsolidationEpoch:    9,
		NextWithdrawalBuilderIndex:    42,
		LatestBlockHash:               common.HexToHash("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"),
		Fork:                          &cltypes.Fork{Epoch: 12},
	}
	var b bytes.Buffer
	require.NoError(t, m.WriteTo(&b))

	m2 := &SlotData{}
	require.NoError(t, m2.ReadFrom(&b, nil))
	require.Equal(t, m, m2)
}

// Pre-GLOAS serialization must omit the GLOAS fields even if set on the struct;
// after round-trip through a pre-GLOAS version they must be zero.
func TestSlotDataPreGloasOmitsGloasFields(t *testing.T) {
	for _, version := range []clparams.StateVersion{clparams.ElectraVersion, clparams.FuluVersion} {
		m := &SlotData{
			Version:                    version,
			Eth1Data:                   &cltypes.Eth1Data{},
			Eth1DepositIndex:           1,
			NextWithdrawalBuilderIndex: 42,
			LatestBlockHash:            common.HexToHash("0xdead"),
			Fork:                       &cltypes.Fork{Epoch: 12},
		}
		var b bytes.Buffer
		require.NoError(t, m.WriteTo(&b))

		m2 := &SlotData{}
		require.NoError(t, m2.ReadFrom(&b, nil))
		require.Equal(t, uint64(0), m2.NextWithdrawalBuilderIndex)
		require.Equal(t, common.Hash{}, m2.LatestBlockHash)
		require.Equal(t, version, m2.Version)
		require.Equal(t, uint64(1), m2.Eth1DepositIndex)
	}
}

// GLOAS adds NextWithdrawalBuilderIndex (8B) + LatestBlockHash (32B) = 40 bytes
// over Electra.
func TestSlotDataGloasIs40BytesLargerThanElectra(t *testing.T) {
	base := func(v clparams.StateVersion) *SlotData {
		return &SlotData{
			Version:                    v,
			Eth1Data:                   &cltypes.Eth1Data{},
			Eth1DepositIndex:           1,
			NextWithdrawalBuilderIndex: 42,
			LatestBlockHash:            common.HexToHash("0xaa"),
			Fork:                       &cltypes.Fork{Epoch: 12},
		}
	}
	var electraBuf, gloasBuf bytes.Buffer
	require.NoError(t, base(clparams.ElectraVersion).WriteTo(&electraBuf))
	require.NoError(t, base(clparams.GloasVersion).WriteTo(&gloasBuf))
	require.Equal(t, 40, gloasBuf.Len()-electraBuf.Len())
}
