// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package ethmpt wraps canonical Ethereum MPT proof code from go-ethereum
// behind a small N42-facing API. The runtime package is self-contained so it
// can be linked into the N42 binary without pulling in geth's secp256k1 stack.
package ethmpt

import (
	"errors"
	"fmt"
	"hash"

	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

var errNilProver = errors.New("nil Ethereum MPT prover")

var (
	emptyRootHash = types.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	emptyCodeHash = keccak256Hash(nil)
)

type keccakState interface {
	hash.Hash
	Read([]byte) (int, error)
}

// ProofWriter is the minimal sink needed to collect encoded trie nodes.
type ProofWriter interface {
	Put(key []byte, value []byte) error
}

// Prover is the minimal trie surface needed to construct canonical Ethereum MPT
// proofs. Concrete adapters can wrap geth or any other authoritative trie.
type Prover interface {
	Prove(key []byte, proofDb ProofWriter) error
}

// StateAccount is the canonical Ethereum consensus representation stored in the
// account trie.
type StateAccount struct {
	Nonce    uint64
	Balance  *uint256.Int
	Root     types.Hash
	CodeHash []byte
}

// NewEmptyStateAccount constructs an empty canonical account.
func NewEmptyStateAccount() *StateAccount {
	return &StateAccount{
		Balance:  new(uint256.Int),
		Root:     emptyRootHash,
		CodeHash: emptyCodeHash.Bytes(),
	}
}

// DecodedAccount is the canonical Ethereum state-account payload extracted from
// a verified account trie leaf.
type DecodedAccount struct {
	Nonce       uint64
	Balance     *uint256.Int
	StorageRoot types.Hash
	CodeHash    types.Hash
}

// ProofList collects raw proof nodes as hex strings, matching eth_getProof.
type ProofList []string

func (n *ProofList) Put(_ []byte, value []byte) error {
	*n = append(*n, hexutil.Encode(value))
	return nil
}

// ProveHashedKey generates a proof for an already-keccak-hashed trie key.
func ProveHashedKey(prover Prover, hashedKey []byte) ([]string, error) {
	if prover == nil {
		return nil, errNilProver
	}
	var proof ProofList
	if err := prover.Prove(hashedKey, &proof); err != nil {
		return nil, err
	}
	return []string(proof), nil
}

// ProveAccount generates a canonical account proof using keccak(address).
func ProveAccount(prover Prover, address types.Address) ([]string, error) {
	return ProveHashedKey(prover, keccak256(address.Bytes()))
}

// ProveStorage generates a canonical storage proof using keccak(slot).
func ProveStorage(prover Prover, slot types.Hash) ([]string, error) {
	return ProveHashedKey(prover, keccak256(slot.Bytes()))
}

// DecodeHexProof decodes a JSON-RPC proof into raw node blobs.
func DecodeHexProof(encoded []string) ([][]byte, error) {
	decoded := make([][]byte, len(encoded))
	for i, node := range encoded {
		raw, err := hexutil.Decode(node)
		if err != nil {
			return nil, fmt.Errorf("decode proof node %d: %w", i, err)
		}
		decoded[i] = raw
	}
	return decoded, nil
}

// VerifyHashedKey verifies a canonical proof for an already-keccak-hashed trie
// key and returns the raw leaf payload stored in the trie.
func VerifyHashedKey(root types.Hash, hashedKey []byte, proof [][]byte) ([]byte, error) {
	return verifyProof(root, hashedKey, proof)
}

// VerifyAccountProof verifies a canonical account proof and returns the full
// RLP-encoded state account.
func VerifyAccountProof(root types.Hash, address types.Address, proof [][]byte) ([]byte, error) {
	return VerifyHashedKey(root, keccak256(address.Bytes()), proof)
}

// VerifyStorageProof verifies a canonical storage proof and returns the decoded
// raw slot value contained in the trie leaf.
func VerifyStorageProof(root types.Hash, slot types.Hash, proof [][]byte) ([]byte, error) {
	encoded, err := VerifyHashedKey(root, keccak256(slot.Bytes()), proof)
	if err != nil || encoded == nil {
		return encoded, err
	}
	var value []byte
	if err := rlp.DecodeBytes(encoded, &value); err != nil {
		return nil, fmt.Errorf("decode storage leaf: %w", err)
	}
	return value, nil
}

// DecodeStateAccount decodes a canonical state-account leaf.
func DecodeStateAccount(accountRLP []byte) (*DecodedAccount, error) {
	if len(accountRLP) == 0 {
		return nil, errors.New("empty account RLP")
	}
	var account StateAccount
	if err := rlp.DecodeBytes(accountRLP, &account); err != nil {
		return nil, fmt.Errorf("decode state account: %w", err)
	}
	decoded := &DecodedAccount{
		Nonce:       account.Nonce,
		Balance:     new(uint256.Int),
		StorageRoot: account.Root,
		CodeHash:    types.BytesToHash(account.CodeHash),
	}
	if account.Balance != nil {
		decoded.Balance = new(uint256.Int).Set(account.Balance)
	}
	return decoded, nil
}

// StorageRootFromAccountRLP extracts the canonical storage trie root from an
// RLP-encoded Ethereum state account.
func StorageRootFromAccountRLP(accountRLP []byte) (types.Hash, error) {
	account, err := DecodeStateAccount(accountRLP)
	if err != nil {
		return types.Hash{}, err
	}
	return account.StorageRoot, nil
}

func keccak256(data ...[]byte) []byte {
	sum := make([]byte, 32)
	d := sha3.NewLegacyKeccak256().(keccakState)
	for _, chunk := range data {
		_, _ = d.Write(chunk)
	}
	_, _ = d.Read(sum)
	return sum
}

func keccak256Hash(data ...[]byte) (h types.Hash) {
	d := sha3.NewLegacyKeccak256().(keccakState)
	for _, chunk := range data {
		_, _ = d.Write(chunk)
	}
	_, _ = d.Read(h[:])
	return h
}
