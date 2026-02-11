// Copyright 2025 The Erigon Authors
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

package txpoolcfg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/common/fixedgas"
)

func TestZeroDataIntrinsicGas(t *testing.T) {
	assert := assert.New(t)
	gas, floorGas7623, discardReason := CalcIntrinsicGas(0, 0, 0, 0, 0, false, true, true, true, true)
	assert.Equal(discardReason, Success)
	assert.Equal(gas, fixedgas.TxGas)
	assert.Equal(floorGas7623, fixedgas.TxGas)
}

// TestEIP7623FloorGasFormula verifies the EIP-7623 floor gas calculation
// Floor gas = 21000 + 10 * (zero_bytes + 4 * nonzero_bytes)
// Which equals: 21000 + 10 * (dataLen + 3 * nonzero_bytes)
func TestEIP7623FloorGasFormula(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		dataLen        uint64
		dataNonZeroLen uint64
		expectedFloor  uint64
	}{
		{
			name:           "no data",
			dataLen:        0,
			dataNonZeroLen: 0,
			expectedFloor:  fixedgas.TxGas, // 21000
		},
		{
			name:           "100 zero bytes only",
			dataLen:        100,
			dataNonZeroLen: 0,
			// floor = 21000 + 10 * (100 + 3*0) = 21000 + 1000 = 22000
			expectedFloor: 22000,
		},
		{
			name:           "100 nonzero bytes only",
			dataLen:        100,
			dataNonZeroLen: 100,
			// floor = 21000 + 10 * (100 + 3*100) = 21000 + 10 * 400 = 25000
			expectedFloor: 25000,
		},
		{
			name:           "50 zero + 50 nonzero bytes",
			dataLen:        100,
			dataNonZeroLen: 50,
			// floor = 21000 + 10 * (100 + 3*50) = 21000 + 10 * 250 = 23500
			expectedFloor: 23500,
		},
		{
			name:           "1000 bytes mixed (300 nonzero)",
			dataLen:        1000,
			dataNonZeroLen: 300,
			// floor = 21000 + 10 * (1000 + 3*300) = 21000 + 10 * 1900 = 40000
			expectedFloor: 40000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, floorGas, reason := CalcIntrinsicGas(tt.dataLen, tt.dataNonZeroLen, 0, 0, 0, false, true, true, true, true)
			require.Equal(t, Success, reason)
			require.Equal(t, tt.expectedFloor, floorGas, "floor gas mismatch")
		})
	}
}

// TestEIP7623FloorGasVsIntrinsicGas verifies that floor gas is used when it exceeds intrinsic gas
func TestEIP7623FloorGasVsIntrinsicGas(t *testing.T) {
	t.Parallel()

	// Test case where intrinsic gas > floor gas (normal execution gas dominates)
	// 100 nonzero bytes: intrinsic = 21000 + 16*100 = 22600, floor = 21000 + 10*(100+300) = 25000
	// Floor > Intrinsic, so floor should be used
	gas, floorGas, reason := CalcIntrinsicGas(100, 100, 0, 0, 0, false, true, true, true, true)
	require.Equal(t, Success, reason)
	require.Equal(t, uint64(22600), gas, "intrinsic gas mismatch")       // 21000 + 16*100
	require.Equal(t, uint64(25000), floorGas, "floor gas mismatch")      // 21000 + 10*(100+300)
	require.Greater(t, floorGas, gas, "floor should exceed intrinsic for calldata-heavy txs")

	// Test case where intrinsic gas > floor gas (execution dominates)
	// 10 nonzero bytes: intrinsic = 21000 + 16*10 = 21160, floor = 21000 + 10*(10+30) = 21400
	gas2, floorGas2, reason2 := CalcIntrinsicGas(10, 10, 0, 0, 0, false, true, true, true, true)
	require.Equal(t, Success, reason2)
	require.Equal(t, uint64(21160), gas2)
	require.Equal(t, uint64(21400), floorGas2)
}

// TestEIP7623FloorGasNotAppliedPrePrague verifies floor gas is only applied after Prague
func TestEIP7623FloorGasNotAppliedPrePrague(t *testing.T) {
	t.Parallel()

	// Pre-Prague: isPrague = false
	gas, floorGas, reason := CalcIntrinsicGas(100, 100, 0, 0, 0, false, true, true, true, false)
	require.Equal(t, Success, reason)
	require.Equal(t, uint64(22600), gas)                   // 21000 + 16*100
	require.Equal(t, fixedgas.TxGas, floorGas)             // Floor should be just TxGas when not Prague
}

// TestEIP7623TxTotalCostFloorPerToken verifies the constant value
func TestEIP7623TxTotalCostFloorPerToken(t *testing.T) {
	t.Parallel()

	// EIP-7623 specifies TOTAL_COST_FLOOR_PER_TOKEN = 10
	require.Equal(t, uint64(10), fixedgas.TxTotalCostFloorPerToken)
}

// TestEIP7623FloorGasWithContractCreation tests floor gas calculation for contract creation
func TestEIP7623FloorGasWithContractCreation(t *testing.T) {
	t.Parallel()

	// Contract creation uses TxGasContractCreation (53000) for intrinsic gas
	// but floor gas still uses TxGas (21000) as base
	gas, floorGas, reason := CalcIntrinsicGas(100, 50, 0, 0, 0, true, true, true, true, true)
	require.Equal(t, Success, reason)

	// Intrinsic gas for contract creation:
	// 53000 (TxGasContractCreation) + 4*50 (zero bytes) + 16*50 (nonzero bytes) + 2*4 (InitCodeWordGas for 4 words)
	// = 53000 + 200 + 800 + 8 = 54008
	initCodeWords := toWordSize(100)
	expectedIntrinsic := fixedgas.TxGasContractCreation +
		fixedgas.TxDataZeroGas*50 +
		fixedgas.TxDataNonZeroGasEIP2028*50 +
		fixedgas.InitCodeWordGas*initCodeWords
	require.Equal(t, expectedIntrinsic, gas)

	// Floor gas: 21000 + 10 * (100 + 3*50) = 21000 + 2500 = 23500
	expectedFloor := fixedgas.TxGas + fixedgas.TxTotalCostFloorPerToken*(100+3*50)
	require.Equal(t, expectedFloor, floorGas)
}

// TestEIP7623FloorGasWithAccessList tests floor gas with access list
func TestEIP7623FloorGasWithAccessList(t *testing.T) {
	t.Parallel()

	// Access list gas is added to intrinsic gas but NOT to floor gas
	gas, floorGas, reason := CalcIntrinsicGas(100, 50, 0, 2, 4, false, true, true, true, true)
	require.Equal(t, Success, reason)

	// Intrinsic gas: 21000 + 4*50 + 16*50 + 2*2400 + 4*1900 = 21000 + 200 + 800 + 4800 + 7600 = 34400
	expectedIntrinsic := fixedgas.TxGas +
		fixedgas.TxDataZeroGas*50 +
		fixedgas.TxDataNonZeroGasEIP2028*50 +
		fixedgas.TxAccessListAddressGas*2 +
		fixedgas.TxAccessListStorageKeyGas*4
	require.Equal(t, expectedIntrinsic, gas)

	// Floor gas: 21000 + 10 * (100 + 3*50) = 23500 (access list not counted)
	expectedFloor := fixedgas.TxGas + fixedgas.TxTotalCostFloorPerToken*(100+3*50)
	require.Equal(t, expectedFloor, floorGas)
}

// TestEIP7623FloorGasWithEIP7702Authorizations tests floor gas with EIP-7702
func TestEIP7623FloorGasWithEIP7702Authorizations(t *testing.T) {
	t.Parallel()

	// EIP-7702 authorization gas is added to intrinsic gas but NOT to floor gas
	gas, floorGas, reason := CalcIntrinsicGas(100, 50, 2, 0, 0, false, true, true, true, true)
	require.Equal(t, Success, reason)

	// Intrinsic gas: 21000 + 4*50 + 16*50 + 2*25000 = 21000 + 200 + 800 + 50000 = 72000
	expectedIntrinsic := fixedgas.TxGas +
		fixedgas.TxDataZeroGas*50 +
		fixedgas.TxDataNonZeroGasEIP2028*50 +
		fixedgas.PerEmptyAccountCost*2
	require.Equal(t, expectedIntrinsic, gas)

	// Floor gas: 21000 + 10 * (100 + 3*50) = 23500 (authorizations not counted)
	expectedFloor := fixedgas.TxGas + fixedgas.TxTotalCostFloorPerToken*(100+3*50)
	require.Equal(t, expectedFloor, floorGas)
}

// TestEIP7623LargeCalldata tests floor gas with large calldata
func TestEIP7623LargeCalldata(t *testing.T) {
	t.Parallel()

	// Large calldata transaction (like blob tx alternative)
	// 128KB of data, half nonzero
	dataLen := uint64(128 * 1024)
	nonZeroLen := dataLen / 2

	gas, floorGas, reason := CalcIntrinsicGas(dataLen, nonZeroLen, 0, 0, 0, false, true, true, true, true)
	require.Equal(t, Success, reason)

	// Intrinsic: 21000 + 4*65536 + 16*65536 = 21000 + 262144 + 1048576 = 1331720
	expectedIntrinsic := fixedgas.TxGas +
		fixedgas.TxDataZeroGas*nonZeroLen +
		fixedgas.TxDataNonZeroGasEIP2028*nonZeroLen
	require.Equal(t, expectedIntrinsic, gas)

	// Floor: 21000 + 10 * (131072 + 3*65536) = 21000 + 10 * 327680 = 3297800
	expectedFloor := fixedgas.TxGas + fixedgas.TxTotalCostFloorPerToken*(dataLen+3*nonZeroLen)
	require.Equal(t, expectedFloor, floorGas)

	// For large calldata, floor should exceed intrinsic
	require.Greater(t, floorGas, gas, "floor should exceed intrinsic for large calldata")
}
