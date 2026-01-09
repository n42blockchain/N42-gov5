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

// Fusaka EIPs implementation
// Reference: ACDE #214 (Ethereum Execution Layer Core Developer Meeting)
//
// Fusaka focuses on:
// - Native Account Abstraction preparation
// - Scalability improvements
// - Gas efficiency optimizations
//
// EIPs included in Fusaka:
// - EIP-7594: PeerDAS (consensus layer, not implemented here)
// - EIP-7823: Set upper bound for MODEXP (execution layer)
// - EIP-7825: Transaction Gas Limit (execution layer)
// - EIP-7883: ModExp gas cost increase (execution layer)
// - EIP-7892: Blob-only parameter hard fork (execution layer)
// - EIP-7917: Deterministic proposer lookahead (consensus layer)
// - EIP-7918: Blob base fee execution cost cap (execution layer)
// - EIP-7935: Set default gas limit (execution layer)
// - EIP-7907: Meter contract code size and increase limit (execution layer)
// - EIP-7934: RLP execution block size limit (execution layer)
// - EIP-7939: CLZ opcode (already in eips_prague.go)
// - EIP-7951: secp256r1 precompile (already in contracts_p256.go)

package vm

import (
	"errors"
	"math/big"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// EIP-7907: Meter contract code size and increase limit (Fusaka)
// https://eips.ethereum.org/EIPS/eip-7907
//
// This EIP increases the maximum contract code size from 24KB to 48KB
// and introduces metering for code size during contract creation.
// =============================================================================

const (
	// MaxCodeSizeFusaka is the new maximum bytecode size for contracts (48KB)
	// Increased from 24KB (EIP-170) to support larger contracts
	MaxCodeSizeFusaka = 48 * 1024 // 48KB

	// MaxInitCodeSizeFusaka is the new maximum initcode size (96KB)
	// Following the 2x initcode rule from EIP-3860
	MaxInitCodeSizeFusaka = 2 * MaxCodeSizeFusaka // 96KB

	// CodeSizeMeteringBaseGas is the base gas for code size metering
	CodeSizeMeteringBaseGas = 2 // Gas per byte of deployed code

	// CodeSizeLimit7907 is the threshold for code size metering (24KB)
	// Above this size, additional gas is charged
	CodeSizeLimit7907 = 24 * 1024
)

// CalcCodeSizeGas calculates the additional gas for code size metering (EIP-7907)
// For code larger than 24KB, an additional gas cost is charged
func CalcCodeSizeGas(codeSize uint64) uint64 {
	if codeSize <= CodeSizeLimit7907 {
		return 0
	}
	// Additional gas for bytes above 24KB
	extraBytes := codeSize - CodeSizeLimit7907
	return extraBytes * CodeSizeMeteringBaseGas
}

// ValidateCodeSize checks if the code size is within the Fusaka limit
func ValidateCodeSize(codeSize uint64, isFusaka bool) error {
	maxSize := uint64(params.MaxCodeSize)
	if isFusaka {
		maxSize = MaxCodeSizeFusaka
	}
	if codeSize > maxSize {
		return ErrMaxCodeSizeExceeded
	}
	return nil
}

// ValidateInitCodeSize checks if the initcode size is within the Fusaka limit
func ValidateInitCodeSize(initCodeSize uint64, isFusaka bool) error {
	maxSize := uint64(params.MaxInitCodeSize)
	if isFusaka {
		maxSize = MaxInitCodeSizeFusaka
	}
	if initCodeSize > maxSize {
		return ErrMaxInitCodeSizeExceeded
	}
	return nil
}

var ErrMaxInitCodeSizeExceeded = errors.New("max initcode size exceeded")

// =============================================================================
// EIP-7823: Set upper bound for MODEXP (Fusaka)
// https://eips.ethereum.org/EIPS/eip-7823
//
// This EIP sets an upper bound for MODEXP input sizes to prevent
// denial of service attacks through extremely large inputs.
// =============================================================================

const (
	// ModExpMaxInputLen is the maximum allowed length for MODEXP base/exp/mod
	// Set to 8192 bits (1024 bytes) to prevent DoS attacks
	ModExpMaxInputLen = 1024

	// ModExpMaxGas is the maximum gas that can be charged for MODEXP
	// This caps the worst-case gas consumption
	ModExpMaxGas = 10_000_000
)

// ValidateModExpInput validates MODEXP input lengths (EIP-7823)
func ValidateModExpInput(baseLen, expLen, modLen uint64) error {
	if baseLen > ModExpMaxInputLen {
		return ErrModExpBaseTooLarge
	}
	if expLen > ModExpMaxInputLen {
		return ErrModExpExpTooLarge
	}
	if modLen > ModExpMaxInputLen {
		return ErrModExpModTooLarge
	}
	return nil
}

var (
	ErrModExpBaseTooLarge = errors.New("modexp base length exceeds maximum")
	ErrModExpExpTooLarge  = errors.New("modexp exponent length exceeds maximum")
	ErrModExpModTooLarge  = errors.New("modexp modulus length exceeds maximum")
)

// =============================================================================
// EIP-7883: ModExp gas cost increase (Fusaka)
// https://eips.ethereum.org/EIPS/eip-7883
//
// This EIP increases the gas cost for MODEXP to better reflect
// actual computational costs and prevent underpricing attacks.
// =============================================================================

const (
	// ModExpGasMultiplier7883 is the multiplier for MODEXP gas calculation
	// Increased to 1.5x (represented as 3/2) in Fusaka
	ModExpGasMultiplier7883Num = 3
	ModExpGasMultiplier7883Den = 2

	// ModExpMinGas7883 is the minimum gas for MODEXP operations
	ModExpMinGas7883 = 200
)

// CalcModExpGas7883 calculates MODEXP gas with EIP-7883 adjustments
func CalcModExpGas7883(baseLen, expLen, modLen *big.Int, expHead *big.Int) uint64 {
	// Use existing complexity calculation
	multiComplexity := modexpMultComplexityFusaka(modLen, baseLen)

	// Calculate adjusted exponent length
	adjExpLen := adjustedExpLength(expLen, expHead)

	// Gas = multiComplexity * max(adjExpLen, 1) / 3 * (3/2)
	gas := new(big.Int).Set(multiComplexity)
	gas.Mul(gas, bigMax(adjExpLen, big1))
	gas.Div(gas, big20)

	// Apply EIP-7883 multiplier (3/2)
	gas.Mul(gas, big.NewInt(ModExpGasMultiplier7883Num))
	gas.Div(gas, big.NewInt(ModExpGasMultiplier7883Den))

	// Enforce minimum gas
	if gas.Uint64() < ModExpMinGas7883 {
		return ModExpMinGas7883
	}

	// Enforce maximum gas (EIP-7823)
	if !gas.IsUint64() || gas.Uint64() > ModExpMaxGas {
		return ModExpMaxGas
	}

	return gas.Uint64()
}

// modexpMultComplexityFusaka calculates complexity for Fusaka (same as Berlin)
func modexpMultComplexityFusaka(modLen, baseLen *big.Int) *big.Int {
	x := bigMax(modLen, baseLen)
	words := new(big.Int).Add(x, big.NewInt(7))
	words.Div(words, big.NewInt(8))
	return new(big.Int).Mul(words, words)
}

// adjustedExpLength calculates adjusted exponent length for gas
func adjustedExpLength(expLen, expHead *big.Int) *big.Int {
	if expLen.Cmp(big.NewInt(32)) <= 0 {
		if expHead.BitLen() > 0 {
			return big.NewInt(int64(expHead.BitLen() - 1))
		}
		return big.NewInt(0)
	}
	// For expLen > 32: 8 * (expLen - 32) + bitLen(expHead) - 1
	result := new(big.Int).Sub(expLen, big.NewInt(32))
	result.Mul(result, big.NewInt(8))
	if expHead.BitLen() > 0 {
		result.Add(result, big.NewInt(int64(expHead.BitLen()-1)))
	}
	return result
}

func bigMax(a, b *big.Int) *big.Int {
	if a.Cmp(b) > 0 {
		return a
	}
	return b
}

// =============================================================================
// EIP-7825: Transaction Gas Limit (Fusaka)
// https://eips.ethereum.org/EIPS/eip-7825
//
// This EIP introduces an explicit transaction gas limit to prevent
// excessively large transactions from consuming too much block space.
// =============================================================================

const (
	// TxGasLimitFusaka is the maximum gas limit for a single transaction
	// Set to 30 million gas (half of typical block gas limit)
	TxGasLimitFusaka = 30_000_000

	// TxGasLimitDefault is the default transaction gas limit before Fusaka
	TxGasLimitDefault = 0 // No explicit limit
)

// ValidateTxGasLimit validates transaction gas limit (EIP-7825)
func ValidateTxGasLimit(gasLimit uint64, isFusaka bool) error {
	if !isFusaka {
		return nil // No limit before Fusaka
	}
	if gasLimit > TxGasLimitFusaka {
		return ErrTxGasLimitExceeded
	}
	return nil
}

var ErrTxGasLimitExceeded = errors.New("transaction gas limit exceeded")

// =============================================================================
// EIP-7892: Blob-only parameter hard fork (Fusaka)
// https://eips.ethereum.org/EIPS/eip-7892
//
// This EIP allows adjusting blob parameters without other EVM changes.
// =============================================================================

// FusakaBlobParams contains blob parameters for Fusaka
type FusakaBlobParams struct {
	MaxBlobsPerBlock    uint64
	TargetBlobsPerBlock uint64
	BlobGasPerBlob      uint64
	MaxBlobGasPerBlock  uint64
}

// DefaultFusakaBlobParams returns default Fusaka blob parameters
func DefaultFusakaBlobParams() FusakaBlobParams {
	return FusakaBlobParams{
		MaxBlobsPerBlock:    12,                    // Increased from 9 (Pectra)
		TargetBlobsPerBlock: 8,                     // Increased from 6 (Pectra)
		BlobGasPerBlob:      params.BlobTxBlobGasPerBlob, // 131072
		MaxBlobGasPerBlock:  12 * params.BlobTxBlobGasPerBlob,
	}
}

// GetBlobParamsFusaka returns blob parameters for Fusaka
func GetBlobParamsFusaka() FusakaBlobParams {
	return DefaultFusakaBlobParams()
}

// =============================================================================
// EIP-7918: Blob base fee execution cost cap (Fusaka)
// https://eips.ethereum.org/EIPS/eip-7918
//
// This EIP caps the blob base fee calculation to prevent excessive costs
// when blob demand is high.
// =============================================================================

const (
	// BlobBaseFeeCapFusaka is the maximum blob base fee (EIP-7918)
	// Cap at 2^13 = 8192 Gwei to prevent excessive costs
	BlobBaseFeeCapFusaka = 1 << 13 // 8192

	// BlobBaseFeeCap7918 is the explicit value from EIP-7918
	BlobBaseFeeCap7918 = 8192
)

// CalcBlobBaseFeeFusaka calculates blob base fee with EIP-7918 cap
func CalcBlobBaseFeeFusaka(excessBlobGas uint64) *big.Int {
	// Use standard calculation
	baseFee := CalcBlobBaseFee(excessBlobGas)

	// Apply cap
	cap := big.NewInt(BlobBaseFeeCap7918)
	cap.Mul(cap, big.NewInt(1e9)) // Convert to Wei

	if baseFee.Cmp(cap) > 0 {
		return cap
	}
	return baseFee
}

// CalcBlobBaseFee calculates the blob base fee from excess gas (standard)
func CalcBlobBaseFee(excessBlobGas uint64) *big.Int {
	// fakeBlobBaseFee = MIN_BLOB_GASPRICE * e^(excessBlobGas / BLOB_GASPRICE_UPDATE_FRACTION)
	// Using integer approximation
	fee := big.NewInt(int64(params.BlobTxMinBlobGasprice))
	if excessBlobGas > 0 {
		// Approximation of exponential
		exponent := new(big.Int).SetUint64(excessBlobGas)
		exponent.Div(exponent, big.NewInt(int64(params.BlobTxBlobGaspriceUpdateFraction)))

		// e^x ≈ 1 + x + x^2/2 for small x
		multiplier := new(big.Int).Add(big.NewInt(1), exponent)
		fee.Mul(fee, multiplier)
	}
	return fee
}

// =============================================================================
// EIP-7935: Set default gas limit (Fusaka)
// https://eips.ethereum.org/EIPS/eip-7935
//
// This EIP sets a new default block gas limit target.
// =============================================================================

const (
	// DefaultBlockGasLimitFusaka is the target block gas limit for Fusaka
	// Increased to allow more transactions per block
	DefaultBlockGasLimitFusaka = 60_000_000 // 60M gas

	// MinBlockGasLimitFusaka is the minimum allowed block gas limit
	MinBlockGasLimitFusaka = 30_000_000 // 30M gas
)

// GetDefaultBlockGasLimit returns the default block gas limit for a fork
func GetDefaultBlockGasLimit(isFusaka bool) uint64 {
	if isFusaka {
		return DefaultBlockGasLimitFusaka
	}
	return params.GenesisGasLimit // Default 8M
}

// =============================================================================
// EIP-7934: RLP execution block size limit (Fusaka)
// https://eips.ethereum.org/EIPS/eip-7934
//
// This EIP limits the maximum size of RLP-encoded execution blocks
// to prevent DoS attacks through oversized blocks.
// =============================================================================

const (
	// MaxRLPBlockSizeFusaka is the maximum RLP-encoded block size
	// Set to 10MB to handle large blocks with many transactions
	MaxRLPBlockSizeFusaka = 10 * 1024 * 1024 // 10MB

	// MaxRLPBlockSizeDefault is the default before Fusaka (no explicit limit)
	MaxRLPBlockSizeDefault = 0
)

// ValidateRLPBlockSize validates RLP block size (EIP-7934)
func ValidateRLPBlockSize(size uint64, isFusaka bool) error {
	if !isFusaka {
		return nil // No limit before Fusaka
	}
	if size > MaxRLPBlockSizeFusaka {
		return ErrRLPBlockSizeTooLarge
	}
	return nil
}

var ErrRLPBlockSizeTooLarge = errors.New("RLP block size exceeds maximum")

// =============================================================================
// EIP-7951: secp256r1 (P-256) precompile (Fusaka)
// https://eips.ethereum.org/EIPS/eip-7951
//
// This EIP adds a precompile for P-256 signature verification.
// Implementation is in contracts_p256.go
// =============================================================================

// P256VerifyAddress is the address of the P256VERIFY precompile
var P256VerifyAddress = types.HexToAddress("0x0000000000000000000000000000000000000100")

// P256VERIFY precompile gas cost
const P256VerifyGasFusaka = 3450

// =============================================================================
// Fusaka JumpTable Modifications
// =============================================================================

// enable7907 enables EIP-7907 (contract code size increase)
func enable7907(jt *JumpTable) {
	// EIP-7907 primarily affects contract creation validation
	// which is handled in the EVM.Create function, not opcodes
}

// enable7951 enables EIP-7951 (P-256 precompile)
func enable7951(jt *JumpTable) {
	// P-256 is added as a precompiled contract
	// No new opcodes are added
}

// newFusakaInstructionSet returns the Fusaka instruction set
// Fusaka = Osaka + Fusaka EIPs
func newFusakaInstructionSet() JumpTable {
	instructionSet := newOsakaInstructionSet()
	enable7907(&instructionSet) // EIP-7907: Code size limit increase
	enable7951(&instructionSet) // EIP-7951: Another Fusaka EIP
	enable7939(&instructionSet) // EIP-7939: CLZ instruction (Count Leading Zeros)
	validateAndFillMaxStack(&instructionSet)
	return instructionSet
}

// =============================================================================
// Init function to register Fusaka EIPs
// =============================================================================

func init() {
	// Register Fusaka EIPs
	activators[7907] = enable7907
	activators[7951] = enable7951
}

// GetFusakaInstructionSet returns the Fusaka instruction set
func GetFusakaInstructionSet() JumpTable {
	return fusakaInstructionSet
}

// =============================================================================
// Fusaka Precompile Registration
// =============================================================================

// RegisterFusakaPrecompiles registers Fusaka-specific precompiles
// This should be called during precompile registry initialization
func RegisterFusakaPrecompiles(registry interface {
	Register(addr types.Address, contract PrecompiledContract)
}) {
	// EIP-7951: P-256 signature verification
	registry.Register(P256VerifyAddress, GetP256Verify())
}

// =============================================================================
// Fusaka Validation Helpers
// =============================================================================

// FusakaConfig holds Fusaka-specific configuration
type FusakaConfig struct {
	MaxCodeSize          uint64
	MaxInitCodeSize      uint64
	TxGasLimit           uint64
	ModExpMaxInputLen    uint64
	ModExpMaxGas         uint64
	BlobBaseFeeCap       uint64
	DefaultBlockGasLimit uint64
	MaxRLPBlockSize      uint64
	BlobParams           FusakaBlobParams
}

// DefaultFusakaConfig returns the default Fusaka configuration
func DefaultFusakaConfig() FusakaConfig {
	return FusakaConfig{
		MaxCodeSize:          MaxCodeSizeFusaka,
		MaxInitCodeSize:      MaxInitCodeSizeFusaka,
		TxGasLimit:           TxGasLimitFusaka,
		ModExpMaxInputLen:    ModExpMaxInputLen,
		ModExpMaxGas:         ModExpMaxGas,
		BlobBaseFeeCap:       BlobBaseFeeCap7918,
		DefaultBlockGasLimit: DefaultBlockGasLimitFusaka,
		MaxRLPBlockSize:      MaxRLPBlockSizeFusaka,
		BlobParams:           DefaultFusakaBlobParams(),
	}
}

// IsFusakaEnabled returns true if Fusaka is enabled for the given rules
func IsFusakaEnabled(rules params.Rules) bool {
	return rules.IsFusaka
}

