// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package vm

import (
	"math/big"
	"testing"
)

// =============================================================================
// EIP-7691 Tests: Blob Throughput Increase
// =============================================================================

func TestCancunBlobParams(t *testing.T) {
	params := CancunBlobParams()

	if params.TargetBlobsPerBlock != 3 {
		t.Errorf("Cancun target blobs: expected 3, got %d", params.TargetBlobsPerBlock)
	}
	if params.MaxBlobsPerBlock != 6 {
		t.Errorf("Cancun max blobs: expected 6, got %d", params.MaxBlobsPerBlock)
	}
	if params.BlobGasPerBlob != 131072 {
		t.Errorf("Cancun blob gas per blob: expected 131072, got %d", params.BlobGasPerBlob)
	}
	if params.TargetBlobGasPerBlock != 393216 {
		t.Errorf("Cancun target blob gas: expected 393216, got %d", params.TargetBlobGasPerBlock)
	}
	if params.MaxBlobGasPerBlock != 786432 {
		t.Errorf("Cancun max blob gas: expected 786432, got %d", params.MaxBlobGasPerBlock)
	}
}

func TestPectraBlobParams(t *testing.T) {
	params := PectraBlobParams()

	if params.TargetBlobsPerBlock != 6 {
		t.Errorf("Pectra target blobs: expected 6, got %d", params.TargetBlobsPerBlock)
	}
	if params.MaxBlobsPerBlock != 9 {
		t.Errorf("Pectra max blobs: expected 9, got %d", params.MaxBlobsPerBlock)
	}
	if params.BlobGasPerBlob != 131072 {
		t.Errorf("Pectra blob gas per blob: expected 131072, got %d", params.BlobGasPerBlob)
	}
	if params.TargetBlobGasPerBlock != 786432 {
		t.Errorf("Pectra target blob gas: expected 786432, got %d", params.TargetBlobGasPerBlock)
	}
	if params.MaxBlobGasPerBlock != 1179648 {
		t.Errorf("Pectra max blob gas: expected 1179648, got %d", params.MaxBlobGasPerBlock)
	}
}

func TestGetBlobParams(t *testing.T) {
	// Cancun
	cancun := GetBlobParams(false)
	if cancun.MaxBlobsPerBlock != 6 {
		t.Error("GetBlobParams(false) should return Cancun params")
	}

	// Pectra
	pectra := GetBlobParams(true)
	if pectra.MaxBlobsPerBlock != 9 {
		t.Error("GetBlobParams(true) should return Pectra params")
	}
}

func TestCalcExcessBlobGasEIP7691(t *testing.T) {
	tests := []struct {
		name             string
		parentExcess     uint64
		parentUsed       uint64
		isPectra         bool
		expectedExcess   uint64
	}{
		// Cancun tests (target = 393216)
		{"cancun_zero", 0, 0, false, 0},
		{"cancun_below_target", 0, 393215, false, 0},
		{"cancun_at_target", 0, 393216, false, 0},
		{"cancun_above_target", 0, 500000, false, 106784},
		
		// Pectra tests (target = 786432)
		{"pectra_zero", 0, 0, true, 0},
		{"pectra_below_target", 0, 786431, true, 0},
		{"pectra_at_target", 0, 786432, true, 0},
		{"pectra_above_target", 0, 1000000, true, 213568},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalcExcessBlobGasEIP7691(tt.parentExcess, tt.parentUsed, tt.isPectra)
			if got != tt.expectedExcess {
				t.Errorf("CalcExcessBlobGasEIP7691(%d, %d, %v) = %d, want %d",
					tt.parentExcess, tt.parentUsed, tt.isPectra, got, tt.expectedExcess)
			}
		})
	}
}

func TestVerifyBlobGasEIP7691(t *testing.T) {
	tests := []struct {
		name       string
		blobGas    uint64
		isPectra   bool
		shouldFail bool
	}{
		// Cancun (max = 786432)
		{"cancun_valid", 786432, false, false},
		{"cancun_invalid", 786433, false, true},
		
		// Pectra (max = 1179648)
		{"pectra_valid_old_max", 786432, true, false},
		{"pectra_valid_new_max", 1179648, true, false},
		{"pectra_invalid", 1179649, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifyBlobGasEIP7691(tt.blobGas, tt.isPectra)
			if (err != nil) != tt.shouldFail {
				t.Errorf("VerifyBlobGasEIP7691(%d, %v) error = %v, shouldFail = %v",
					tt.blobGas, tt.isPectra, err, tt.shouldFail)
			}
		})
	}
}

// =============================================================================
// EIP-7623 Tests: Increase Calldata Cost
// =============================================================================

func TestCalcCalldataCostEIP7623(t *testing.T) {
	tests := []struct {
		name       string
		data       []byte
		isPectra   bool
		expectCost uint64
	}{
		// Empty data
		{"empty", []byte{}, false, 0},
		{"empty_pectra", []byte{}, true, 0},
		
		// Small data (below threshold, standard pricing)
		{"small_standard", make([]byte, 100), false, 400},        // 100 * 4 (zero bytes)
		{"small_pectra", make([]byte, 100), true, 400},           // Below threshold, standard pricing
		
		// Non-zero data
		{"nonzero_standard", []byte{1, 2, 3, 4}, false, 64},      // 4 * 16
		{"nonzero_pectra", []byte{1, 2, 3, 4}, true, 64},         // Below threshold
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalcCalldataCostEIP7623(tt.data, tt.isPectra)
			if got != tt.expectCost {
				t.Errorf("CalcCalldataCostEIP7623 = %d, want %d", got, tt.expectCost)
			}
		})
	}
}

func TestCalcCalldataCostEIP7623_LargeData(t *testing.T) {
	// Create large data (above 4KB threshold)
	largeZeroData := make([]byte, 5000)
	largeNonZeroData := make([]byte, 5000)
	for i := range largeNonZeroData {
		largeNonZeroData[i] = 0xFF
	}

	// Standard (pre-Pectra): 5000 * 4 = 20000 (zero), 5000 * 16 = 80000 (non-zero)
	standardZero := CalcCalldataCostEIP7623(largeZeroData, false)
	if standardZero != 20000 {
		t.Errorf("Standard zero cost = %d, want 20000", standardZero)
	}

	standardNonZero := CalcCalldataCostEIP7623(largeNonZeroData, false)
	if standardNonZero != 80000 {
		t.Errorf("Standard non-zero cost = %d, want 80000", standardNonZero)
	}

	// Pectra with floor: 5000 * 10 = 50000 (zero), 5000 * 68 = 340000 (non-zero)
	pectraZero := CalcCalldataCostEIP7623(largeZeroData, true)
	if pectraZero != 50000 {
		t.Errorf("Pectra zero cost = %d, want 50000 (floor)", pectraZero)
	}

	pectraNonZero := CalcCalldataCostEIP7623(largeNonZeroData, true)
	if pectraNonZero != 340000 {
		t.Errorf("Pectra non-zero cost = %d, want 340000 (floor)", pectraNonZero)
	}
}

func TestIntrinsicGasEIP7623(t *testing.T) {
	tests := []struct {
		name               string
		data               []byte
		isContractCreation bool
		isPectra           bool
		minExpected        uint64
	}{
		{"simple_tx", nil, false, false, 21000},
		{"contract_creation", nil, true, false, 53000},
		{"with_small_data", make([]byte, 100), false, false, 21400},
		{"with_small_data_pectra", make([]byte, 100), false, true, 21400},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IntrinsicGasEIP7623(tt.data, nil, tt.isContractCreation, tt.isPectra)
			if got < tt.minExpected {
				t.Errorf("IntrinsicGasEIP7623 = %d, want >= %d", got, tt.minExpected)
			}
		})
	}
}

// =============================================================================
// EIP-7840 Tests: Blob Schedule Configuration
// =============================================================================

func TestDefaultCancunBlobSchedule(t *testing.T) {
	schedule := DefaultCancunBlobSchedule()

	if schedule.TargetBlobsPerBlock != 3 {
		t.Errorf("Target blobs: expected 3, got %d", schedule.TargetBlobsPerBlock)
	}
	if schedule.MaxBlobsPerBlock != 6 {
		t.Errorf("Max blobs: expected 6, got %d", schedule.MaxBlobsPerBlock)
	}
}

func TestDefaultPectraBlobSchedule(t *testing.T) {
	schedule := DefaultPectraBlobSchedule()

	if schedule.TargetBlobsPerBlock != 6 {
		t.Errorf("Target blobs: expected 6, got %d", schedule.TargetBlobsPerBlock)
	}
	if schedule.MaxBlobsPerBlock != 9 {
		t.Errorf("Max blobs: expected 9, got %d", schedule.MaxBlobsPerBlock)
	}
}

func TestGetBlobSchedule(t *testing.T) {
	// Nil schedule should return Cancun params
	params := GetBlobSchedule(nil, 0)
	if params.MaxBlobsPerBlock != 6 {
		t.Error("Nil schedule should return Cancun params")
	}

	// Schedule with Pectra time
	schedule := &BlobSchedule{
		TargetBlobsPerBlock:    6,
		MaxBlobsPerBlock:       9,
		BlobGasPerBlob:         131072,
		TargetBlobGasPerBlock:  786432,
		MaxBlobGasPerBlock:     1179648,
		MinBlobGasprice:        1,
		BlobGaspriceUpdateFrac: 3338477,
		PectraTime:             big.NewInt(1000),
	}

	// Before Pectra
	params = GetBlobSchedule(schedule, 999)
	if params.MaxBlobsPerBlock != 6 {
		t.Error("Before Pectra should return Cancun params")
	}

	// At Pectra
	params = GetBlobSchedule(schedule, 1000)
	if params.MaxBlobsPerBlock != 9 {
		t.Error("At Pectra should return Pectra params")
	}
}

func TestValidateBlobSchedule(t *testing.T) {
	tests := []struct {
		name       string
		schedule   *BlobSchedule
		shouldFail bool
	}{
		{"nil_schedule", nil, false},
		{
			"valid_schedule",
			&BlobSchedule{
				TargetBlobsPerBlock:   3,
				MaxBlobsPerBlock:      6,
				BlobGasPerBlob:        131072,
				TargetBlobGasPerBlock: 393216,
				MaxBlobGasPerBlock:    786432,
			},
			false,
		},
		{
			"target_exceeds_max",
			&BlobSchedule{
				TargetBlobsPerBlock: 10,
				MaxBlobsPerBlock:    6,
			},
			true,
		},
		{
			"inconsistent_gas",
			&BlobSchedule{
				TargetBlobsPerBlock:   3,
				MaxBlobsPerBlock:      6,
				BlobGasPerBlob:        131072,
				TargetBlobGasPerBlock: 100000, // Wrong
				MaxBlobGasPerBlock:    786432,
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBlobSchedule(tt.schedule)
			if (err != nil) != tt.shouldFail {
				t.Errorf("ValidateBlobSchedule() error = %v, shouldFail = %v", err, tt.shouldFail)
			}
		})
	}
}

// =============================================================================
// Blob Fee Calculation Tests
// =============================================================================

func TestCalcBlobFeeEIP7691(t *testing.T) {
	tests := []struct {
		name          string
		excessBlobGas uint64
		isPectra      bool
		minFee        uint64
	}{
		{"cancun_zero_excess", 0, false, 1},
		{"cancun_some_excess", 100000, false, 1},
		{"pectra_zero_excess", 0, true, 1},
		{"pectra_some_excess", 100000, true, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fee := CalcBlobFeeEIP7691(tt.excessBlobGas, tt.isPectra)
			if fee.Uint64() < tt.minFee {
				t.Errorf("CalcBlobFeeEIP7691(%d, %v) = %d, want >= %d",
					tt.excessBlobGas, tt.isPectra, fee.Uint64(), tt.minFee)
			}
		})
	}
}

// =============================================================================
// Constants Tests
// =============================================================================

func TestEIP7691Constants(t *testing.T) {
	// Verify constant relationships
	if CancunTargetBlobGasPerBlock != CancunTargetBlobsPerBlock*CancunBlobGasPerBlob {
		t.Error("Cancun target blob gas calculation mismatch")
	}
	if CancunMaxBlobGasPerBlock != CancunMaxBlobsPerBlock*CancunBlobGasPerBlob {
		t.Error("Cancun max blob gas calculation mismatch")
	}
	if PectraTargetBlobGasPerBlock != PectraTargetBlobsPerBlock*PectraBlobGasPerBlob {
		t.Error("Pectra target blob gas calculation mismatch")
	}
	if PectraMaxBlobGasPerBlock != PectraMaxBlobsPerBlock*PectraBlobGasPerBlob {
		t.Error("Pectra max blob gas calculation mismatch")
	}
}

func TestEIP7623Constants(t *testing.T) {
	// Verify floor costs are higher than standard
	if TxDataNonZeroGasEIP7623 <= TxDataNonZeroGasEIP2028 {
		t.Error("EIP-7623 non-zero cost should be higher than EIP-2028")
	}
	if TxDataZeroGasEIP7623 <= TxDataZeroGas {
		t.Error("EIP-7623 zero cost should be higher than standard")
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkCalcExcessBlobGasEIP7691_Cancun(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalcExcessBlobGasEIP7691(100000, 500000, false)
	}
}

func BenchmarkCalcExcessBlobGasEIP7691_Pectra(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalcExcessBlobGasEIP7691(100000, 1000000, true)
	}
}

func BenchmarkCalcCalldataCostEIP7623_Small(b *testing.B) {
	data := make([]byte, 1000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcCalldataCostEIP7623(data, true)
	}
}

func BenchmarkCalcCalldataCostEIP7623_Large(b *testing.B) {
	data := make([]byte, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CalcCalldataCostEIP7623(data, true)
	}
}

func BenchmarkCalcBlobFeeEIP7691(b *testing.B) {
	for i := 0; i < b.N; i++ {
		CalcBlobFeeEIP7691(500000, true)
	}
}

func BenchmarkIntrinsicGasEIP7623(b *testing.B) {
	data := make([]byte, 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IntrinsicGasEIP7623(data, nil, false, true)
	}
}

