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
	"errors"

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
// delegationPrefixArray is the immutable source for the delegation prefix.
var delegationPrefixArray = [3]byte{0xef, 0x01, 0x00}

// DelegationPrefix returns a copy of the EIP-7702 delegation prefix bytes.
// Returns a fresh slice each time to prevent callers from mutating the constant.
func DelegationPrefix() []byte {
	p := delegationPrefixArray
	return p[:]
}

// Gas costs for EIP-7702
const (
	// PerAuthBaseCost is the base gas cost per authorization tuple
	PerAuthBaseCost = 12500

	// PerEmptyAccountCost is the gas cost for each newly created account
	PerEmptyAccountCost = 25000
)

// HasDelegation checks if the code has the EIP-7702 delegation prefix
func HasDelegation(code []byte) bool {
	return len(code) == 23 && bytes.HasPrefix(code, DelegationPrefix())
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
	copy(code, DelegationPrefix())
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

// BLS precompile addresses aligned with the active Prague/Osaka execution-spec-tests
// layout. Single-scalar and multi-scalar inputs intentionally share the MSM entries.
var (
	BLS12G1AddAddr      = types.BytesToAddress([]byte{0x0b})
	BLS12G1MulAddr      = types.BytesToAddress([]byte{0x0c})
	BLS12G1MultiExpAddr = BLS12G1MulAddr
	BLS12G2AddAddr      = types.BytesToAddress([]byte{0x0d})
	BLS12G2MulAddr      = types.BytesToAddress([]byte{0x0e})
	BLS12G2MultiExpAddr = BLS12G2MulAddr
	BLS12PairingAddr    = types.BytesToAddress([]byte{0x0f})
	BLS12MapG1Addr      = types.BytesToAddress([]byte{0x10})
	BLS12MapG2Addr      = types.BytesToAddress([]byte{0x11})
)

// =============================================================================
// EIP-2935: Historical block hashes in state (Pectra)
// https://eips.ethereum.org/EIPS/eip-2935
// =============================================================================

// HistoryStorageAddress is the address where historical block hashes are stored
// This is the canonical address for the EIP-2935 system contract in Pectra
// Address derived as: rlp([0xfffffffffffffffffffffffffffffffffffffffe, 0])
var HistoryStorageAddress = types.HexToAddress("0x0000F90827F1C53a10CB7A02335B175320002935")

// HistoryServeWindow is the number of block hashes stored in the system contract.
// The contract stores the last HISTORY_SERVE_WINDOW block hashes in a ring buffer.
// Value 8191 per EIP-2935 specification (prime number for optimal distribution).
const HistoryServeWindow = 8191

// HistoryStorageCode is the canonical deployed bytecode of the EIP-2935
// history storage contract used by current EEST Prague/Osaka fixtures.
var HistoryStorageCode = types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe14604657602036036042575f35600143038111604257611fff81430311604257611fff9006545f5260205ff35b5f5ffd5b5f35611fff60014303065500")

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
	DepositRequestType       = 0x00
	WithdrawalRequestType    = 0x01
	ConsolidationRequestType = 0x02
)

// SystemAddress is the system address that can make requests
var SystemAddress = types.HexToAddress("0xfffffffffffffffffffffffffffffffffffffffe")

// WithdrawalRequestsAddress is the address of the withdrawal requests contract (EIP-7002)
// aligned with the active Prague/Osaka execution-spec-tests fixtures.
var WithdrawalRequestsAddress = types.HexToAddress("0x00000961EF480EB55E80D19AD83579A64C007002")

// ConsolidationRequestsAddress is the address of the consolidation requests contract (EIP-7251)
// aligned with the active Prague/Osaka execution-spec-tests fixtures.
var ConsolidationRequestsAddress = types.HexToAddress("0x0000BBDDC7CE488642FB579F8B00F3A590007251")

// WithdrawalRequestQueueCode is the canonical deployed bytecode for the
// Prague EIP-7002 withdrawal request queue contract used by current EEST
// Prague/Osaka fixtures.
var WithdrawalRequestQueueCode = types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe1460cb5760115f54807fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff146101f457600182026001905f5b5f82111560685781019083028483029004916001019190604d565b909390049250505036603814608857366101f457346101f4575f5260205ff35b34106101f457600154600101600155600354806003026004013381556001015f35815560010160203590553360601b5f5260385f601437604c5fa0600101600355005b6003546002548082038060101160df575060105b5f5b8181146101835782810160030260040181604c02815460601b8152601401816001015481526020019060020154807fffffffffffffffffffffffffffffffff00000000000000000000000000000000168252906010019060401c908160381c81600701538160301c81600601538160281c81600501538160201c81600401538160181c81600301538160101c81600201538160081c81600101535360010160e1565b910180921461019557906002556101a0565b90505f6002555f6003555b5f54807fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff14156101cd57505f5b6001546002828201116101e25750505f6101e8565b01600290035b5f555f600155604c025ff35b5f5ffd")

// ConsolidationRequestQueueCode is the canonical deployed bytecode for the
// Prague EIP-7251 consolidation request queue contract used by current EEST
// Prague/Osaka fixtures.
var ConsolidationRequestQueueCode = types.Hex2Bytes("3373fffffffffffffffffffffffffffffffffffffffe1460d35760115f54807fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff1461019a57600182026001905f5b5f82111560685781019083028483029004916001019190604d565b9093900492505050366060146088573661019a573461019a575f5260205ff35b341061019a57600154600101600155600354806004026004013381556001015f358155600101602035815560010160403590553360601b5f5260605f60143760745fa0600101600355005b6003546002548082038060021160e7575060025b5f5b8181146101295782810160040260040181607402815460601b815260140181600101548152602001816002015481526020019060030154905260010160e9565b910180921461013b5790600255610146565b90505f6002555f6003555b5f54807fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff141561017357505f5b6001546001828201116101885750505f61018e565b01600190035b5f555f6001556074025ff35b5f5ffd")

// =============================================================================
// EIP-7002: Execution Layer Triggerable Withdrawals (Pectra)
// https://eips.ethereum.org/EIPS/eip-7002
// =============================================================================

// WithdrawalRequestFee is the base fee for withdrawal requests
const WithdrawalRequestFee = 1 // 1 Gwei minimum fee

// MaxWithdrawalRequestsPerBlock is the maximum withdrawal requests per block
const MaxWithdrawalRequestsPerBlock = 16

// WithdrawalRequest represents a validator withdrawal request (EIP-7002)
type WithdrawalRequest struct {
	SourceAddress   types.Address // Address that requested the withdrawal
	ValidatorPubkey [48]byte      // BLS public key of the validator
	Amount          uint64        // Amount to withdraw in Gwei (0 = full exit)
}

// WithdrawalRequestSize is the size of a serialized withdrawal request
const WithdrawalRequestSize = 20 + 48 + 8 // 76 bytes

// SerializeWithdrawalRequest serializes a withdrawal request
func (w *WithdrawalRequest) Serialize() []byte {
	buf := make([]byte, WithdrawalRequestSize)
	copy(buf[0:20], w.SourceAddress[:])
	copy(buf[20:68], w.ValidatorPubkey[:])
	// Amount in little-endian
	for i := 0; i < 8; i++ {
		buf[68+i] = byte(w.Amount >> (8 * i))
	}
	return buf
}

// DeserializeWithdrawalRequest deserializes a withdrawal request
func DeserializeWithdrawalRequest(data []byte) (*WithdrawalRequest, error) {
	if len(data) != WithdrawalRequestSize {
		return nil, errInvalidWithdrawalRequest
	}
	req := &WithdrawalRequest{}
	copy(req.SourceAddress[:], data[0:20])
	copy(req.ValidatorPubkey[:], data[20:68])
	// Amount in little-endian
	for i := 0; i < 8; i++ {
		req.Amount |= uint64(data[68+i]) << (8 * i)
	}
	return req, nil
}

var errInvalidWithdrawalRequest = errors.New("invalid withdrawal request")

// =============================================================================
// EIP-7251: Increase MAX_EFFECTIVE_BALANCE (Pectra)
// https://eips.ethereum.org/EIPS/eip-7251
// =============================================================================

// ConsolidationRequest represents a validator consolidation request (EIP-7251)
type ConsolidationRequest struct {
	SourceAddress types.Address // Address initiating the consolidation
	SourcePubkey  [48]byte      // Source validator BLS public key
	TargetPubkey  [48]byte      // Target validator BLS public key
}

// ConsolidationRequestSize is the size of a serialized consolidation request
const ConsolidationRequestSize = 20 + 48 + 48 // 116 bytes

// MaxConsolidationRequestsPerBlock is the maximum consolidation requests per block
const MaxConsolidationRequestsPerBlock = 1

// SerializeConsolidationRequest serializes a consolidation request
func (c *ConsolidationRequest) Serialize() []byte {
	buf := make([]byte, ConsolidationRequestSize)
	copy(buf[0:20], c.SourceAddress[:])
	copy(buf[20:68], c.SourcePubkey[:])
	copy(buf[68:116], c.TargetPubkey[:])
	return buf
}

// DeserializeConsolidationRequest deserializes a consolidation request
func DeserializeConsolidationRequest(data []byte) (*ConsolidationRequest, error) {
	if len(data) != ConsolidationRequestSize {
		return nil, errInvalidConsolidationRequest
	}
	req := &ConsolidationRequest{}
	copy(req.SourceAddress[:], data[0:20])
	copy(req.SourcePubkey[:], data[20:68])
	copy(req.TargetPubkey[:], data[68:116])
	return req, nil
}

var errInvalidConsolidationRequest = errors.New("invalid consolidation request")

// MinActivationBalance is the minimum balance for validator activation (32 ETH)
var MinActivationBalance = new(uint256.Int).Mul(
	uint256.NewInt(32),
	uint256.NewInt(1e18),
)

// MaxEffectiveBalanceElectra is an alias for MaxEffectiveBalanceEIP7251 (2048 ETH)
var MaxEffectiveBalanceElectra = MaxEffectiveBalanceEIP7251

// MIN_PER_EPOCH_CHURN_LIMIT_ELECTRA is the minimum churn limit per epoch
const MinPerEpochChurnLimitElectra = 128_000_000_000 // 128 Gwei

// MAX_PER_EPOCH_ACTIVATION_EXIT_CHURN_LIMIT is the maximum churn for activations/exits
const MaxPerEpochActivationExitChurnLimit = 256_000_000_000 // 256 Gwei

// =============================================================================
// EIP-6110: Supply validator deposits on chain (Pectra)
// https://eips.ethereum.org/EIPS/eip-6110
// =============================================================================

// DepositContractAddress is the address of the beacon chain deposit contract
var DepositContractAddress = types.HexToAddress("0x00000000219ab540356cBB839Cbe05303d7705Fa")

// Deposit event signature for EIP-6110
// DepositEvent(bytes pubkey, bytes withdrawal_credentials, bytes amount, bytes signature, bytes index)
var DepositEventSignature = types.HexToHash("0x649bbc62d0e31342afea4e5cd82d4049e7e1ee912fc0889aa790803be39038c5")

// DepositRequest represents a validator deposit from logs (EIP-6110)
type DepositRequest struct {
	Pubkey                [48]byte // BLS public key
	WithdrawalCredentials [32]byte // Withdrawal credentials
	Amount                uint64   // Amount in Gwei
	Signature             [96]byte // BLS signature
	Index                 uint64   // Deposit index
}

// DepositRequestSize is the size of a serialized deposit request
const DepositRequestSize = 48 + 32 + 8 + 96 + 8 // 192 bytes

// ParseDepositLog parses a deposit log into a DepositRequest
func ParseDepositLog(topics []types.Hash, data []byte) (*DepositRequest, error) {
	// Verify event signature
	if len(topics) < 1 || topics[0] != DepositEventSignature {
		return nil, errInvalidDepositLog
	}

	// Minimum data length check
	if len(data) < 576 { // 5 * 32 (offsets) + minimum data
		return nil, errInvalidDepositLog
	}

	deposit := &DepositRequest{}

	// ABI-encoded data: skip 5 offset values (32 bytes each)
	offset := 160

	// Parse pubkey (48 bytes)
	if offset+32 > len(data) {
		return nil, errInvalidDepositLog
	}
	pubkeyLen, overflow := new(uint256.Int).SetBytes(data[offset : offset+32]).Uint64WithOverflow()
	if overflow {
		return nil, errInvalidDepositLog
	}
	offset += 32
	if pubkeyLen != 48 || offset+48 > len(data) {
		return nil, errInvalidDepositLog
	}
	copy(deposit.Pubkey[:], data[offset:offset+48])
	offset += 48
	// Align to 32 bytes
	offset = ((offset + 31) / 32) * 32

	// Parse withdrawal_credentials (32 bytes)
	if offset+32 > len(data) {
		return nil, errInvalidDepositLog
	}
	credLen, overflow := new(uint256.Int).SetBytes(data[offset : offset+32]).Uint64WithOverflow()
	if overflow {
		return nil, errInvalidDepositLog
	}
	offset += 32
	if credLen != 32 || offset+32 > len(data) {
		return nil, errInvalidDepositLog
	}
	copy(deposit.WithdrawalCredentials[:], data[offset:offset+32])
	offset += 32

	// Parse amount (8 bytes, little-endian)
	if offset+32 > len(data) {
		return nil, errInvalidDepositLog
	}
	amountLen, overflow := new(uint256.Int).SetBytes(data[offset : offset+32]).Uint64WithOverflow()
	if overflow {
		return nil, errInvalidDepositLog
	}
	offset += 32
	if amountLen != 8 || offset+8 > len(data) {
		return nil, errInvalidDepositLog
	}
	// Amount is little-endian in Gwei
	for i := 0; i < 8; i++ {
		deposit.Amount |= uint64(data[offset+i]) << (8 * i)
	}
	offset += 8
	offset = ((offset + 31) / 32) * 32

	// Parse signature (96 bytes)
	if offset+32 > len(data) {
		return nil, errInvalidDepositLog
	}
	sigLen, overflow := new(uint256.Int).SetBytes(data[offset : offset+32]).Uint64WithOverflow()
	if overflow {
		return nil, errInvalidDepositLog
	}
	offset += 32
	if sigLen != 96 || offset+96 > len(data) {
		return nil, errInvalidDepositLog
	}
	copy(deposit.Signature[:], data[offset:offset+96])
	offset += 96
	offset = ((offset + 31) / 32) * 32

	// Parse index (8 bytes, little-endian)
	if offset+32 > len(data) {
		return nil, errInvalidDepositLog
	}
	indexLen, overflow := new(uint256.Int).SetBytes(data[offset : offset+32]).Uint64WithOverflow()
	if overflow {
		return nil, errInvalidDepositLog
	}
	offset += 32
	if indexLen != 8 || offset+8 > len(data) {
		return nil, errInvalidDepositLog
	}
	for i := 0; i < 8; i++ {
		deposit.Index |= uint64(data[offset+i]) << (8 * i)
	}

	return deposit, nil
}

// SerializeDepositRequest serializes a deposit request for inclusion in block
func (d *DepositRequest) Serialize() []byte {
	buf := make([]byte, DepositRequestSize)
	copy(buf[0:48], d.Pubkey[:])
	copy(buf[48:80], d.WithdrawalCredentials[:])
	// Amount in little-endian
	for i := 0; i < 8; i++ {
		buf[80+i] = byte(d.Amount >> (8 * i))
	}
	copy(buf[88:184], d.Signature[:])
	// Index in little-endian
	for i := 0; i < 8; i++ {
		buf[184+i] = byte(d.Index >> (8 * i))
	}
	return buf
}

var errInvalidDepositLog = errors.New("invalid deposit log")

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

	// EIP-2935: Check history storage for older blocks (Prague)
	if interpreter.evm.ChainRules().IsPrague {
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

// StoreParentBlockHash stores the parent block hash in the EIP-2935 history contract.
// This should be called at the beginning of each block's execution, including block 1
// where the genesis hash must be written to slot 0.
func StoreParentBlockHash(statedb StateDB, parentNumber uint64, parentHash types.Hash) {
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
	SetCode(addr types.Address, code []byte)
}

// =============================================================================
// Gas calculation helpers for EIP-7702
// =============================================================================

// CalcAuthorizationGas calculates the gas cost for authorization list processing.
// Returns the gas cost and whether an overflow occurred (true = overflow).
func CalcAuthorizationGas(authCount int, newAccountCount int) (uint64, bool) {
	authGas, ok := SafeMulUint64(uint64(authCount), PerAuthBaseCost)
	if !ok {
		return 0, true
	}
	accountGas, ok := SafeMulUint64(uint64(newAccountCount), PerEmptyAccountCost)
	if !ok {
		return 0, true
	}
	total, ok := SafeAddUint64(authGas, accountGas)
	if !ok {
		return 0, true
	}
	return total, false
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

// =============================================================================
// Execution Requests Helpers (EIP-7685)
// =============================================================================

// EncodeExecutionRequest encodes a request with its type prefix
func EncodeExecutionRequest(reqType byte, data []byte) []byte {
	result := make([]byte, 1+len(data))
	result[0] = reqType
	copy(result[1:], data)
	return result
}

// DecodeExecutionRequest decodes a request and returns its type and data
func DecodeExecutionRequest(data []byte) (byte, []byte, error) {
	if len(data) < 1 {
		return 0, nil, errEmptyRequest
	}
	return data[0], data[1:], nil
}

var errEmptyRequest = errors.New("empty execution request")
