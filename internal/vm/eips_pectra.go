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
// This is the canonical address for the EIP-2935 system contract in Pectra
var HistoryStorageAddress = types.HexToAddress("0x0F792be4B0c0cb4DAE440Ef133E90C0eCD48CCCC")

// HistoryServeWindow is the number of block hashes stored in the system contract
// The contract stores the last HISTORY_SERVE_WINDOW block hashes in a ring buffer
const HistoryServeWindow = 8192

// HistoryStorageCode is the deployed bytecode of the EIP-2935 system contract
// This contract stores block hashes at slot = blockNumber % HISTORY_SERVE_WINDOW
// and allows reading via BLOCKHASH opcode for blocks within the serve window
var HistoryStorageCode = types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe1460575767ffffffffffffffff5765015150a06020527f0167ffffffffffffffff8111615058578060005b905b60008360408203523390523661003f57610000565b6020357f806101c557610000576000604035523290")

// SetHistoryStorageSlotGas is the gas cost for the system call to store a block hash
const SetHistoryStorageSlotGas = 21000

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
// EIP-2935 extends BLOCKHASH to serve block hashes beyond the 256 block limit
// by reading from a system contract that stores historical block hashes
func opBlockhash2935(pc *uint64, interpreter *EVMInterpreter, scope *ScopeContext) ([]byte, error) {
	num := scope.Stack.Peek()
	num64, overflow := num.Uint64WithOverflow()
	if overflow {
		num.Clear()
		return nil, nil
	}

	currentBlock := interpreter.evm.Context().BlockNumber
	if currentBlock < 1 {
		num.Clear()
		return nil, nil
	}

	// Block number must be less than current block
	if num64 >= currentBlock {
		num.Clear()
		return nil, nil
	}

	// Check if within standard 256 block window (original BLOCKHASH behavior)
	if currentBlock-num64 <= 256 {
		hash := interpreter.evm.Context().GetHash(num64)
		num.SetBytes(hash.Bytes())
		return nil, nil
	}

	// EIP-2935: Check history storage for older blocks (Pectra)
	if interpreter.evm.ChainRules().IsPectra {
		// Check if within the history serve window
		if currentBlock-num64 > HistoryServeWindow {
			num.Clear()
			return nil, nil
		}

		// Read from history storage contract
		// Slot is calculated as blockNumber % HISTORY_SERVE_WINDOW
		slotNum := new(uint256.Int).SetUint64(num64 % HistoryServeWindow)
		slot := types.Hash{}
		slotNum.WriteToSlice(slot[:])

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
// EIP-2935 System Contract Helpers
// =============================================================================

// StoreParentBlockHash stores the parent block hash in the EIP-2935 history contract
// This should be called at the beginning of each block's execution
// The hash is stored at slot = parentBlockNumber % HISTORY_SERVE_WINDOW
func StoreParentBlockHash(statedb StateDB, parentNumber uint64, parentHash types.Hash) {
	if parentNumber == 0 {
		return // Skip genesis block
	}

	// Calculate storage slot: parentNumber % HISTORY_SERVE_WINDOW
	slot := types.Hash{}
	slotNum := new(uint256.Int).SetUint64(parentNumber % HistoryServeWindow)
	slotNum.WriteToSlice(slot[:])

	// Convert hash to uint256 for storage
	hashVal := new(uint256.Int).SetBytes(parentHash[:])

	// Store the hash in the history contract
	statedb.SetState(HistoryStorageAddress, &slot, *hashVal)
}

// EnsureHistoryContractDeployed ensures the EIP-2935 history contract is deployed
// This should be called during the Pectra fork transition
func EnsureHistoryContractDeployed(statedb StateDB) {
	// Check if contract is already deployed
	if len(statedb.GetCode(HistoryStorageAddress)) > 0 {
		return
	}

	// Deploy the history storage contract
	statedb.SetCode(HistoryStorageAddress, HistoryStorageCode)
}

// StateDB interface for EIP-2935 (subset of full IntraBlockState)
type StateDB interface {
	GetState(addr types.Address, key *types.Hash, value *uint256.Int)
	SetState(addr types.Address, key *types.Hash, value uint256.Int)
	GetCode(addr types.Address) []byte
	SetCode(addr types.Address, code []byte) error
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

