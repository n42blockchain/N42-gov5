package evmtypes

import (
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	libcommon "github.com/n42blockchain/N42/common/types"
)

// BlockContext provides the EVM with auxiliary information. Once provided
// it shouldn't be modified.
type BlockContext struct {
	// CanTransfer returns whether the account contains
	// sufficient ether to transfer the value
	CanTransfer CanTransferFunc
	// Transfer transfers ether from one account to the other
	Transfer TransferFunc
	// GetHash returns the hash corresponding to n
	GetHash GetHashFunc

	// Block information
	Coinbase    libcommon.Address // Provides information for COINBASE
	GasLimit    uint64            // Provides information for GASLIMIT
	MaxGasLimit bool              // Use GasLimit override for 2^256-1 (to be compatible with OpenEthereum's trace_call)
	BlockNumber uint64            // Provides information for NUMBER
	Time        uint64            // Provides information for TIME
	Difficulty  *big.Int          // Provides information for DIFFICULTY
	BaseFee     *uint256.Int      // Provides information for BASEFEE
	PrevRanDao  *libcommon.Hash   // Provides information for PREVRANDAO

	// EIP-4844: Blob gas fields (Cancun)
	BlobBaseFee   *uint256.Int // Provides information for BLOBBASEFEE
	ExcessBlobGas uint64       // Excess blob gas for EIP-4844
}

// TxContext provides the EVM with information about a transaction.
// All fields can change between transactions.
type TxContext struct {
	// Message information
	TxHash   libcommon.Hash
	Origin   libcommon.Address // Provides information for ORIGIN
	GasPrice *uint256.Int      // Provides information for GASPRICE

	// EIP-4844: Blob transaction fields (Cancun)
	BlobHashes []libcommon.Hash // Versioned blob hashes for BLOBHASH opcode
}

type (
	// CanTransferFunc is the signature of a transfer guard function
	CanTransferFunc func(IntraBlockState, libcommon.Address, *uint256.Int) bool
	// TransferFunc is the signature of a transfer function
	TransferFunc func(IntraBlockState, libcommon.Address, libcommon.Address, *uint256.Int, bool)
	// GetHashFunc returns the nth block hash in the blockchain
	// and is used by the BLOCKHASH EVM op code.
	GetHashFunc func(uint64) libcommon.Hash
)

// IntraBlockState is an EVM database for full state querying.
// This is a type alias for common.StateDB to ensure consistency
// across the codebase while maintaining backward compatibility.
//
// The actual implementation is modules/state.IntraBlockState.
// All EVM operations should use this interface for state access.
type IntraBlockState = common.StateDB

// Deprecated type aliases for backward compatibility.
// These ensure existing code continues to work without modification.
// New code should use the types from common package directly.
var (
	_ IntraBlockState = (common.StateDB)(nil) // Type check
)

// Legacy interface kept for documentation purposes.
// The actual interface is now defined in common/state_types.go as StateDB.
//
// Methods include:
//   - Account: CreateAccount, Exist, Empty
//   - Balance: SubBalance, AddBalance, GetBalance
//   - Nonce: GetNonce, SetNonce
//   - Code: GetCodeHash, GetCode, SetCode, GetCodeSize
//   - Refund: AddRefund, SubRefund, GetRefund
//   - Storage: GetCommittedState, GetState, SetState
//   - Self-destruct: Selfdestruct, HasSelfdestructed
//   - Access List (EIP-2930): PrepareAccessList, AddressInAccessList, SlotInAccessList, AddAddressToAccessList, AddSlotToAccessList
//   - Snapshot: Snapshot, RevertToSnapshot
//   - Logging: AddLog
//   - Transient Storage (EIP-1153): GetTransientState, SetTransientState

// Re-export block.Log for convenience
type Log = block.Log

// Re-export transaction.AccessList for convenience
type AccessList = transaction.AccessList
