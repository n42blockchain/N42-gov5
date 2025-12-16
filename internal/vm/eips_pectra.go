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

// Pectra EIPs implementation
// Reference: go-ethereum and erigon implementations

package vm

import (
	"bytes"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// EIP-7702: Set EOA account code (Pectra)
// https://eips.ethereum.org/EIPS/eip-7702
// =============================================================================

// DelegationPrefix is the prefix bytes for EIP-7702 delegated accounts
// An account with code starting with this prefix (0xef0100) is considered delegated
var DelegationPrefix = []byte{0xef, 0x01, 0x00}

// Gas costs for EIP-7702
const (
	// PerAuthBaseCost is the base gas cost per authorization tuple
	PerAuthBaseCost = 2500

	// PER_EMPTY_ACCOUNT_COST is the gas cost for each newly created account
	PerEmptyAccountCost = 25000
)

// HasDelegation checks if the code has the EIP-7702 delegation prefix
func HasDelegation(code []byte) bool {
	return len(code) == 23 && bytes.HasPrefix(code, DelegationPrefix)
}

// ParseDelegation parses the delegation address from code
// Returns the delegated address and true if successful
func ParseDelegation(code []byte) (types.Address, bool) {
	if !HasDelegation(code) {
		return types.Address{}, false
	}
	return types.BytesToAddress(code[3:23]), true
}

// AddressToDelegation creates delegation code from an address
func AddressToDelegation(addr types.Address) []byte {
	code := make([]byte, 23)
	copy(code, DelegationPrefix)
	copy(code[3:], addr[:])
	return code
}

// ResolveDelegation resolves delegation chains for EIP-7702
// If the given address has delegated code, returns the delegated address
// Otherwise returns the original address
func ResolveDelegation(evm VMInterpreter, addr types.Address) types.Address {
	code := evm.IntraBlockState().GetCode(addr)
	if delegated, ok := ParseDelegation(code); ok {
		return delegated
	}
	return addr
}

// =============================================================================
// EIP-2537: BLS12-381 curve operations (Pectra)
// https://eips.ethereum.org/EIPS/eip-2537
// =============================================================================

// BLS precompile addresses (0x0b - 0x12)
var (
	BLS12G1AddAddr      = types.BytesToAddress([]byte{0x0b})
	BLS12G1MulAddr      = types.BytesToAddress([]byte{0x0c})
	BLS12G1MultiExpAddr = types.BytesToAddress([]byte{0x0d})
	BLS12G2AddAddr      = types.BytesToAddress([]byte{0x0e})
	BLS12G2MulAddr      = types.BytesToAddress([]byte{0x0f})
	BLS12G2MultiExpAddr = types.BytesToAddress([]byte{0x10})
	BLS12PairingAddr    = types.BytesToAddress([]byte{0x11})
	BLS12MapG1Addr      = types.BytesToAddress([]byte{0x12})
	BLS12MapG2Addr      = types.BytesToAddress([]byte{0x13})
)

// =============================================================================
// EIP-2935: Historical block hashes in state (Pectra)
// https://eips.ethereum.org/EIPS/eip-2935
// =============================================================================

// HistoryStorageAddress is the address where historical block hashes are stored
var HistoryStorageAddress = types.HexToAddress("0x0aae40965e6800cd9b1f4b05ff21581047e3f91e")

// HistoryServeWindow is the number of block hashes stored in the system contract
const HistoryServeWindow = 8192

// =============================================================================
// EIP-7251: Increase the MAX_EFFECTIVE_BALANCE (Pectra)
// https://eips.ethereum.org/EIPS/eip-7251
// =============================================================================

// MaxEffectiveBalance for validators (increased from 32 ETH to 2048 ETH)
var MaxEffectiveBalanceEIP7251 = new(uint256.Int).Mul(
	uint256.NewInt(2048),
	uint256.NewInt(1e18),
)

// =============================================================================
// EIP-7685: General purpose execution layer requests (Pectra)
// https://eips.ethereum.org/EIPS/eip-7685
// =============================================================================

// Request types for EIP-7685
const (
	DepositRequestType    = 0x00
	WithdrawalRequestType = 0x01
	ConsolidationRequestType = 0x02
)

// SystemAddress is the system address that can make requests
var SystemAddress = types.HexToAddress("0xfffffffffffffffffffffffffffffffffffffffe")

// WithdrawalRequestsAddress is the address of the withdrawal requests contract
var WithdrawalRequestsAddress = types.HexToAddress("0x00A3ca265EBcb825B45F985A16CEFB49958cE017")

// ConsolidationRequestsAddress is the address of the consolidation requests contract
var ConsolidationRequestsAddress = types.HexToAddress("0x00b42dbF2194e931E80326D950320f7d9Dbeac02")

// =============================================================================
// EIP-6110: Supply validator deposits on chain (Pectra)
// https://eips.ethereum.org/EIPS/eip-6110
// =============================================================================

// DepositContractAddress is the address of the beacon chain deposit contract
var DepositContractAddress = types.HexToAddress("0x00000000219ab540356cBB839Cbe05303d7705Fa")

// =============================================================================
// Pectra JumpTable modifications
// =============================================================================

// enable7702 applies EIP-7702 "Set EOA account code"
// This EIP allows EOAs to temporarily set their code to a contract address
// The actual code execution is handled in the EVM call functions
func enable7702(jt *JumpTable) {
	// EIP-7702 primarily affects transaction processing and state changes,
	// not new opcodes. The main changes are:
	// 1. New transaction type (SetCodeTxType = 0x04)
	// 2. Authorization list processing
	// 3. Delegation code pattern (0xef0100 + address)

	// EXTCODESIZE, EXTCODECOPY, EXTCODEHASH need to resolve delegation
	// This is handled in the EVM call layer, not in opcodes

	// Gas cost adjustments for CALL operations to delegated accounts
	// These are handled in the gas calculation functions
}

// enable2537 applies EIP-2537 "BLS12-381 curve operations"
// Adds precompiled contracts for BLS12-381 operations
func enable2537(jt *JumpTable) {
	// BLS operations are added as precompiled contracts
	// No new opcodes are added
}

// enable2935 applies EIP-2935 "Historical block hashes in state"
// Saves historical block hashes in a system contract
func enable2935(jt *JumpTable) {
	// BLOCKHASH now reads from the history storage contract
	// for blocks older than 256 blocks
	jt[BLOCKHASH].execute = opBlockhash2935
}

// opBlockhash2935 implements BLOCKHASH with EIP-2935 support
func opBlockhash2935(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	num := scope.Stack.Peek()
	num64, overflow := num.Uint64WithOverflow()
	if overflow {
		num.Clear()
		return nil, nil
	}

	var upper, lower uint64
	upper = interpreter.evm.Context().BlockNumber
	if upper < 1 {
		num.Clear()
		return nil, nil
	}
	upper--

	// Check if within 256 block window (standard BLOCKHASH behavior)
	if upper > 256 {
		lower = upper - 256
	}
	if num64 >= lower && num64 <= upper {
		hash := interpreter.evm.Context().GetHash(num64)
		num.SetBytes(hash.Bytes())
		return nil, nil
	}

	// EIP-2935: Check history storage for older blocks
	if interpreter.evm.ChainRules().IsPrague {
		// For blocks within the history serve window
		if upper >= HistoryServeWindow && num64 < upper-HistoryServeWindow {
			num.Clear()
			return nil, nil
		}

		// Read from history storage contract
		slot := types.Hash{}
		slot.SetBytes(new(uint256.Int).Mod(num, uint256.NewInt(HistoryServeWindow)).Bytes())
		var hashVal uint256.Int
		interpreter.evm.IntraBlockState().GetState(HistoryStorageAddress, &slot, &hashVal)
		if !hashVal.IsZero() {
			num.Set(&hashVal)
			return nil, nil
		}
	}

	num.Clear()
	return nil, nil
}

// newPectraInstructionSet returns the Pectra instruction set
// Pectra = Prague + additional EIPs
func newPectraInstructionSet() JumpTable {
	instructionSet := newPragueInstructionSet()
	enable7702(&instructionSet)
	enable2935(&instructionSet)
	// enable2537(&instructionSet) - BLS operations are precompiles, not opcodes
	validateAndFillMaxStack(&instructionSet)
	return instructionSet
}

// =============================================================================
// Gas calculation helpers for EIP-7702
// =============================================================================

// CalcAuthorizationGas calculates the gas cost for authorization list processing
func CalcAuthorizationGas(authCount int, newAccountCount int) uint64 {
	gas := uint64(authCount) * PerAuthBaseCost
	gas += uint64(newAccountCount) * PerEmptyAccountCost
	return gas
}

// =============================================================================
// Init function to register Pectra EIPs
// =============================================================================

func init() {
	// Register Pectra EIPs
	activators[7702] = enable7702
	activators[2537] = enable2537
	activators[2935] = enable2935

	// Gas constants for Pectra
	params.PerAuthBaseCost = PerAuthBaseCost
	params.PerEmptyAccountCost = PerEmptyAccountCost
}

