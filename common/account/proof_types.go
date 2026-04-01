package account

import "github.com/n42blockchain/N42/common/types"

// AccProofResult holds Merkle proof for an account (eth_getProof).
type AccProofResult struct {
	Address      types.Address
	AccountProof [][]byte
	Balance      string
	CodeHash     types.Hash
	Nonce        uint64
	StorageHash  types.Hash
	StorageProof []StorProofResult
}

// StorProofResult holds Merkle proof for a storage slot.
type StorProofResult struct {
	Key   types.Hash
	Value string
	Proof [][]byte
}
