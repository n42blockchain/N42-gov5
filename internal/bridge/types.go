package bridge

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// BridgeStatus represents the state of a cross-chain transfer.
type BridgeStatus uint8

const (
	StatusPending   BridgeStatus = 0 // Proof not yet submitted
	StatusProving   BridgeStatus = 1 // ZK proof being generated
	StatusSubmitted BridgeStatus = 2 // Proof submitted to target chain
	StatusVerified  BridgeStatus = 3 // Proof verified on target chain
	StatusCompleted BridgeStatus = 4 // Funds released to recipient
	StatusFailed    BridgeStatus = 5 // Verification failed
)

// Router provides a unified API for cross-chain operations.
// It abstracts the underlying proof generation and submission.
type Router interface {
	// Send initiates a cross-chain transfer from N42 to a target chain.
	// The transfer is backed by ZK proof of the N42 state.
	Send(destChain uint32, recipient types.Address, amount *uint256.Int) (types.Hash, error)

	// VerifyIncoming verifies an incoming cross-chain message using the
	// provided proof against the verified state root.
	VerifyIncoming(proof []byte, stateRoot types.Hash) error

	// Status returns the current status of a cross-chain transfer.
	Status(txHash types.Hash) (BridgeStatus, error)

	// LatestVerifiedBlock returns the latest N42 block verified on the target chain.
	LatestVerifiedBlock(destChain uint32) (uint64, error)
}
