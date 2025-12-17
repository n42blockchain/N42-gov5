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

// Pectra Blob Upgrades
// This file implements blob-related EIPs for the Pectra hard fork:
// - EIP-7691: Blob throughput increase
// - EIP-7623: Increase calldata cost
// - EIP-7840: Add blob schedule to EL config files

package vm

import (
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
)

// =============================================================================
// EIP-7691: Blob Throughput Increase (Pectra)
// https://eips.ethereum.org/EIPS/eip-7691
// =============================================================================

// Cancun blob parameters (pre-Pectra)
const (
	// CancunTargetBlobsPerBlock is the target number of blobs per block in Cancun
	CancunTargetBlobsPerBlock = 3

	// CancunMaxBlobsPerBlock is the maximum number of blobs per block in Cancun
	CancunMaxBlobsPerBlock = 6

	// CancunBlobGasPerBlob is gas consumed per blob in Cancun
	CancunBlobGasPerBlob = 1 << 17 // 131072

	// CancunTargetBlobGasPerBlock is the target blob gas per block in Cancun
	CancunTargetBlobGasPerBlock = CancunTargetBlobsPerBlock * CancunBlobGasPerBlob // 393216

	// CancunMaxBlobGasPerBlock is the maximum blob gas per block in Cancun
	CancunMaxBlobGasPerBlock = CancunMaxBlobsPerBlock * CancunBlobGasPerBlob // 786432
)

// Pectra blob parameters (EIP-7691)
const (
	// PectraTargetBlobsPerBlock is the target number of blobs per block in Pectra
	PectraTargetBlobsPerBlock = 6

	// PectraMaxBlobsPerBlock is the maximum number of blobs per block in Pectra
	PectraMaxBlobsPerBlock = 9

	// PectraBlobGasPerBlob is gas consumed per blob in Pectra (unchanged)
	PectraBlobGasPerBlob = CancunBlobGasPerBlob // 131072

	// PectraTargetBlobGasPerBlock is the target blob gas per block in Pectra
	PectraTargetBlobGasPerBlock = PectraTargetBlobsPerBlock * PectraBlobGasPerBlob // 786432

	// PectraMaxBlobGasPerBlock is the maximum blob gas per block in Pectra
	PectraMaxBlobGasPerBlock = PectraMaxBlobsPerBlock * PectraBlobGasPerBlob // 1179648
)

// BlobParams holds blob-related parameters for a specific fork
type BlobParams struct {
	TargetBlobsPerBlock    uint64 // Target number of blobs
	MaxBlobsPerBlock       uint64 // Maximum number of blobs
	BlobGasPerBlob         uint64 // Gas per blob
	TargetBlobGasPerBlock  uint64 // Target blob gas per block
	MaxBlobGasPerBlock     uint64 // Maximum blob gas per block
	MinBlobGasprice        uint64 // Minimum blob gas price
	BlobGaspriceUpdateFrac uint64 // Update fraction for blob gas price
}

// CancunBlobParams returns blob parameters for Cancun
func CancunBlobParams() *BlobParams {
	return &BlobParams{
		TargetBlobsPerBlock:    CancunTargetBlobsPerBlock,
		MaxBlobsPerBlock:       CancunMaxBlobsPerBlock,
		BlobGasPerBlob:         CancunBlobGasPerBlob,
		TargetBlobGasPerBlock:  CancunTargetBlobGasPerBlock,
		MaxBlobGasPerBlock:     CancunMaxBlobGasPerBlock,
		MinBlobGasprice:        transaction.BlobTxMinBlobGasprice,
		BlobGaspriceUpdateFrac: transaction.BlobTxBlobGaspriceUpdateFraction,
	}
}

// PectraBlobParams returns blob parameters for Pectra (EIP-7691)
func PectraBlobParams() *BlobParams {
	return &BlobParams{
		TargetBlobsPerBlock:    PectraTargetBlobsPerBlock,
		MaxBlobsPerBlock:       PectraMaxBlobsPerBlock,
		BlobGasPerBlob:         PectraBlobGasPerBlob,
		TargetBlobGasPerBlock:  PectraTargetBlobGasPerBlock,
		MaxBlobGasPerBlock:     PectraMaxBlobGasPerBlock,
		MinBlobGasprice:        transaction.BlobTxMinBlobGasprice,
		BlobGaspriceUpdateFrac: transaction.BlobTxBlobGaspriceUpdateFraction,
	}
}

// GetBlobParams returns blob parameters based on fork
func GetBlobParams(isPectra bool) *BlobParams {
	if isPectra {
		return PectraBlobParams()
	}
	return CancunBlobParams()
}

// CalcExcessBlobGasEIP7691 calculates excess blob gas with Pectra parameters
func CalcExcessBlobGasEIP7691(parentExcessBlobGas, parentBlobGasUsed uint64, isPectra bool) uint64 {
	params := GetBlobParams(isPectra)
	excessBlobGas := parentExcessBlobGas + parentBlobGasUsed
	if excessBlobGas < params.TargetBlobGasPerBlock {
		return 0
	}
	return excessBlobGas - params.TargetBlobGasPerBlock
}

// VerifyBlobGasEIP7691 verifies blob gas with Pectra parameters
func VerifyBlobGasEIP7691(blobGasUsed uint64, isPectra bool) error {
	params := GetBlobParams(isPectra)
	if blobGasUsed > params.MaxBlobGasPerBlock {
		return transaction.ErrBlobGasLimitExceeded
	}
	return nil
}

// =============================================================================
// EIP-7623: Increase Calldata Cost (Pectra)
// https://eips.ethereum.org/EIPS/eip-7623
// =============================================================================

// EIP-7623 constants
const (
	// Pre-Pectra calldata costs
	TxDataNonZeroGasEIP2028 uint64 = 16 // Per byte of non-zero data (EIP-2028)
	TxDataZeroGas           uint64 = 4  // Per byte of zero data

	// Pectra calldata costs (EIP-7623)
	// Floor calldata cost when transactions contain significant calldata
	TxDataNonZeroGasEIP7623 uint64 = 68 // Increased cost per non-zero byte
	TxDataZeroGasEIP7623    uint64 = 10 // Increased cost per zero byte

	// Threshold for applying floor price
	// If calldata cost > floor, use floor; otherwise use standard
	CalldataFloorThreshold uint64 = 4 * 1024 // 4KB
)

// CalcCalldataCostEIP7623 calculates the calldata cost with EIP-7623 rules
// The cost is max(standard_cost, floor_cost) where:
// - standard_cost = 16 * nonzero_bytes + 4 * zero_bytes
// - floor_cost = 68 * nonzero_bytes + 10 * zero_bytes (only for large calldata)
func CalcCalldataCostEIP7623(data []byte, isPectra bool) uint64 {
	if len(data) == 0 {
		return 0
	}

	var zeroBytes, nonZeroBytes uint64
	for _, b := range data {
		if b == 0 {
			zeroBytes++
		} else {
			nonZeroBytes++
		}
	}

	// Standard cost (pre-Pectra or base cost)
	standardCost := nonZeroBytes*TxDataNonZeroGasEIP2028 + zeroBytes*TxDataZeroGas

	if !isPectra {
		return standardCost
	}

	// EIP-7623: Apply floor cost for large calldata
	totalBytes := uint64(len(data))
	if totalBytes > CalldataFloorThreshold {
		// Floor cost with increased rates
		floorCost := nonZeroBytes*TxDataNonZeroGasEIP7623 + zeroBytes*TxDataZeroGasEIP7623
		if floorCost > standardCost {
			return floorCost
		}
	}

	return standardCost
}

// IntrinsicGasEIP7623 calculates intrinsic gas with EIP-7623 calldata pricing
func IntrinsicGasEIP7623(data []byte, accessList transaction.AccessList, isContractCreation, isPectra bool) uint64 {
	// Base gas
	var gas uint64
	if isContractCreation {
		gas = 53000 // TxGasContractCreation
	} else {
		gas = 21000 // TxGas
	}

	// Calldata gas with EIP-7623
	gas += CalcCalldataCostEIP7623(data, isPectra)

	// Access list gas (EIP-2930)
	if len(accessList) > 0 {
		gas += uint64(len(accessList)) * 2400 // TxAccessListAddressGas
		for _, tuple := range accessList {
			gas += uint64(len(tuple.StorageKeys)) * 1900 // TxAccessListStorageKeyGas
		}
	}

	return gas
}

// =============================================================================
// EIP-7840: Add Blob Schedule to EL Config Files (Pectra)
// https://eips.ethereum.org/EIPS/eip-7840
// =============================================================================

// BlobSchedule defines configurable blob parameters for a chain
type BlobSchedule struct {
	// Target parameters
	TargetBlobsPerBlock   uint64 `json:"targetBlobsPerBlock"`
	TargetBlobGasPerBlock uint64 `json:"targetBlobGasPerBlock"`

	// Maximum parameters
	MaxBlobsPerBlock   uint64 `json:"maxBlobsPerBlock"`
	MaxBlobGasPerBlock uint64 `json:"maxBlobGasPerBlock"`

	// Gas pricing
	BlobGasPerBlob         uint64 `json:"blobGasPerBlob"`
	MinBlobGasprice        uint64 `json:"minBlobGasprice"`
	BlobGaspriceUpdateFrac uint64 `json:"blobGaspriceUpdateFraction"`

	// Fork activation
	CancunTime *big.Int `json:"cancunTime,omitempty"`
	PectraTime *big.Int `json:"pectraTime,omitempty"`
}

// DefaultCancunBlobSchedule returns the default blob schedule for Cancun
func DefaultCancunBlobSchedule() *BlobSchedule {
	return &BlobSchedule{
		TargetBlobsPerBlock:    CancunTargetBlobsPerBlock,
		TargetBlobGasPerBlock:  CancunTargetBlobGasPerBlock,
		MaxBlobsPerBlock:       CancunMaxBlobsPerBlock,
		MaxBlobGasPerBlock:     CancunMaxBlobGasPerBlock,
		BlobGasPerBlob:         CancunBlobGasPerBlob,
		MinBlobGasprice:        transaction.BlobTxMinBlobGasprice,
		BlobGaspriceUpdateFrac: transaction.BlobTxBlobGaspriceUpdateFraction,
	}
}

// DefaultPectraBlobSchedule returns the default blob schedule for Pectra
func DefaultPectraBlobSchedule() *BlobSchedule {
	return &BlobSchedule{
		TargetBlobsPerBlock:    PectraTargetBlobsPerBlock,
		TargetBlobGasPerBlock:  PectraTargetBlobGasPerBlock,
		MaxBlobsPerBlock:       PectraMaxBlobsPerBlock,
		MaxBlobGasPerBlock:     PectraMaxBlobGasPerBlock,
		BlobGasPerBlob:         PectraBlobGasPerBlob,
		MinBlobGasprice:        transaction.BlobTxMinBlobGasprice,
		BlobGaspriceUpdateFrac: transaction.BlobTxBlobGaspriceUpdateFraction,
	}
}

// GetBlobSchedule returns the blob schedule for a given timestamp
func GetBlobSchedule(schedule *BlobSchedule, timestamp uint64) *BlobParams {
	if schedule == nil {
		return CancunBlobParams()
	}

	// Check if Pectra is active
	if schedule.PectraTime != nil && timestamp >= schedule.PectraTime.Uint64() {
		return &BlobParams{
			TargetBlobsPerBlock:    schedule.TargetBlobsPerBlock,
			MaxBlobsPerBlock:       schedule.MaxBlobsPerBlock,
			BlobGasPerBlob:         schedule.BlobGasPerBlob,
			TargetBlobGasPerBlock:  schedule.TargetBlobGasPerBlock,
			MaxBlobGasPerBlock:     schedule.MaxBlobGasPerBlock,
			MinBlobGasprice:        schedule.MinBlobGasprice,
			BlobGaspriceUpdateFrac: schedule.BlobGaspriceUpdateFrac,
		}
	}

	// Default to Cancun
	return CancunBlobParams()
}

// ValidateBlobSchedule validates a blob schedule configuration
func ValidateBlobSchedule(schedule *BlobSchedule) error {
	if schedule == nil {
		return nil
	}

	// Target must be <= Max
	if schedule.TargetBlobsPerBlock > schedule.MaxBlobsPerBlock {
		return errInvalidBlobSchedule
	}

	// Gas calculations must be consistent
	if schedule.TargetBlobGasPerBlock != schedule.TargetBlobsPerBlock*schedule.BlobGasPerBlob {
		return errInvalidBlobSchedule
	}
	if schedule.MaxBlobGasPerBlock != schedule.MaxBlobsPerBlock*schedule.BlobGasPerBlob {
		return errInvalidBlobSchedule
	}

	return nil
}

// =============================================================================
// Blob Fee Calculation with Pectra Parameters
// =============================================================================

// CalcBlobFeeEIP7691 calculates blob fee with configurable parameters
func CalcBlobFeeEIP7691(excessBlobGas uint64, isPectra bool) *uint256.Int {
	params := GetBlobParams(isPectra)
	return fakeExponentialEIP7691(
		uint256.NewInt(params.MinBlobGasprice),
		uint256.NewInt(excessBlobGas),
		uint256.NewInt(params.BlobGaspriceUpdateFrac),
	)
}

// fakeExponentialEIP7691 approximates exp(numerator/denominator) * factor
func fakeExponentialEIP7691(factor, numerator, denominator *uint256.Int) *uint256.Int {
	i := uint256.NewInt(1)
	output := uint256.NewInt(0)
	numeratorAccum := new(uint256.Int).Mul(factor, denominator)

	for {
		output.Add(output, numeratorAccum)

		numeratorAccum.Mul(numeratorAccum, numerator)
		numeratorAccum.Div(numeratorAccum, denominator)
		numeratorAccum.Div(numeratorAccum, i)

		i.AddUint64(i, 1)

		if numeratorAccum.IsZero() {
			break
		}
	}

	return output.Div(output, denominator)
}

// =============================================================================
// Errors
// =============================================================================

var (
	errInvalidBlobSchedule = &blobScheduleError{"invalid blob schedule configuration"}
)

type blobScheduleError struct {
	msg string
}

func (e *blobScheduleError) Error() string {
	return e.msg
}

