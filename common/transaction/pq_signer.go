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

// Post-Quantum Signer Implementation
// This signer handles post-quantum transaction signature verification
// supporting Falcon-512, SQIsign, and Dilithium algorithms.

package transaction

import (
	"errors"
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/dilithium/mode2"
	"github.com/n42blockchain/N42/crypto/dilithium/mode3"
	"github.com/n42blockchain/N42/crypto/falcon"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// Compile-time assertions: ensure transaction size constants match the Dilithium library.
var _ [mode2.PublicKeySize]byte = [Dilithium2PublicKeySize]byte{}
var _ [mode2.SignatureSize]byte = [Dilithium2SignatureSize]byte{}
var _ [mode3.PublicKeySize]byte = [Dilithium3PublicKeySize]byte{}
var _ [mode3.SignatureSize]byte = [Dilithium3SignatureSize]byte{}

var (
	// ErrUnsupportedPQAlgorithm is returned when the PQ algorithm is not supported
	ErrUnsupportedPQAlgorithm = errors.New("pq: unsupported signature algorithm")

	// ErrPQSignatureVerificationFailed is returned when PQ signature verification fails
	ErrPQSignatureVerificationFailed = errors.New("pq: signature verification failed")

	// ErrPQPublicKeyRequired is returned when full public key is required but hash is provided
	ErrPQPublicKeyRequired = errors.New("pq: full public key required for first transaction")

	// ErrPQInvalidPubKeySize is returned when public key size doesn't match algorithm
	ErrPQInvalidPubKeySize = errors.New("pq: invalid public key size for algorithm")

	// ErrNotPostQuantumTx is returned when trying to use PQ signer on non-PQ transaction
	ErrNotPostQuantumTx = errors.New("pq: not a post-quantum transaction")
)

// PostQuantumSigner implements the Signer interface for post-quantum transactions.
// It wraps the londonSigner to provide backward compatibility with legacy transaction types
// while adding support for post-quantum signature verification.
type PostQuantumSigner struct {
	londonSigner // Embedded London signer for backward compatibility
	chainId      *big.Int
}

// NewPostQuantumSigner returns a new signer that supports:
// - Post-quantum transactions (Type 0x05) with Falcon-512, SQIsign, and Dilithium
// - EIP-1559 dynamic fee transactions
// - EIP-2930 access list transactions
// - EIP-155 replay protected transactions
// - Legacy Homestead transactions
func NewPostQuantumSigner(chainId *big.Int) Signer {
	return PostQuantumSigner{
		londonSigner: NewLondonSigner(chainId).(londonSigner),
		chainId:      chainId,
	}
}

// ChainID returns the chain ID of this signer
func (s PostQuantumSigner) ChainID() *big.Int {
	return s.chainId
}

// Equal returns true if the given signer is the same as the receiver
func (s PostQuantumSigner) Equal(s2 Signer) bool {
	pqSigner, ok := s2.(PostQuantumSigner)
	if !ok {
		return false
	}
	return s.chainId.Cmp(pqSigner.chainId) == 0
}

// Sender returns the sender address derived from the transaction signature.
// For post-quantum transactions, the address is derived from the PQ public key.
// For other transaction types, it delegates to the embedded London signer.
func (s PostQuantumSigner) Sender(tx *Transaction) (types.Address, error) {
	if tx.Type() != PostQuantumTxType {
		return s.londonSigner.Sender(tx)
	}

	pqTx, ok := tx.inner.(*PostQuantumTx)
	if !ok {
		return types.Address{}, ErrNotPostQuantumTx
	}

	// Verify chain ID
	chainIdU256, _ := uint256.FromBig(s.chainId)
	if pqTx.ChainID.Cmp(chainIdU256) != 0 {
		return types.Address{}, ErrInvalidChainId
	}

	// Get the signing hash
	signingHash := pqTx.SigningHash()

	// Verify the post-quantum signature
	pubKey, err := s.verifyPQSignature(pqTx, signingHash[:])
	if err != nil {
		return types.Address{}, err
	}

	// Derive address from public key: Keccak256(pubKey)[12:]
	return s.derivePQAddress(pubKey), nil
}

// verifyPQSignature verifies the post-quantum signature and returns the public key bytes
func (s PostQuantumSigner) verifyPQSignature(tx *PostQuantumTx, msg []byte) ([]byte, error) {
	// Ensure we have a full public key (not just a hash)
	// For hash mode (mode 1), the full key needs to be looked up from a registry
	// This is handled at a higher level; here we require the full key
	if tx.PubKeyMode == 1 {
		// In hash mode, the caller must have resolved the full public key
		// and placed it in PubKeyData before calling Sender
		// If still a hash (32 bytes), we cannot verify
		if len(tx.PubKeyData) == PQPublicKeyHashSize {
			return nil, ErrPQPublicKeyRequired
		}
	}

	pubKeyData := tx.PubKeyData
	sig := tx.PQSignature

	switch tx.SigAlgo {
	case PQAlgoFalcon512:
		return s.verifyFalconSignature(pubKeyData, msg, sig)
	case PQAlgoSQIsign:
		return s.verifySQIsignSignature(pubKeyData, msg, sig)
	case PQAlgoDilithium2:
		return s.verifyDilithium2Signature(pubKeyData, msg, sig)
	case PQAlgoDilithium3:
		return s.verifyDilithium3Signature(pubKeyData, msg, sig)
	default:
		return nil, ErrUnsupportedPQAlgorithm
	}
}

// verifyFalconSignature verifies a Falcon-512 signature
func (s PostQuantumSigner) verifyFalconSignature(pubKey, msg, sig []byte) ([]byte, error) {
	if len(pubKey) != falcon.PublicKeySize {
		return nil, ErrPQInvalidPubKeySize
	}

	pk, err := falcon.PublicKeyFromBytes(pubKey)
	if err != nil {
		return nil, ErrInvalidPQPublicKey
	}

	if !falcon.Verify(pk, msg, sig) {
		return nil, ErrPQSignatureVerificationFailed
	}

	return pubKey, nil
}

// verifySQIsignSignature verifies an SQIsign signature
// Note: SQIsign integration is pending; this is a placeholder
func (s PostQuantumSigner) verifySQIsignSignature(pubKey, msg, sig []byte) ([]byte, error) {
	if len(pubKey) != SQIsignPublicKeySize {
		return nil, ErrPQInvalidPubKeySize
	}

	// TODO: Implement SQIsign verification when library is integrated
	// For now, return an error indicating the algorithm is not yet supported
	return nil, errors.New("pq: SQIsign verification not yet implemented")
}

// verifyDilithium2Signature verifies a Dilithium2 signature
func (s PostQuantumSigner) verifyDilithium2Signature(pubKey, msg, sig []byte) ([]byte, error) {
	if len(pubKey) != Dilithium2PublicKeySize {
		return nil, ErrPQInvalidPubKeySize
	}

	if len(sig) != Dilithium2SignatureSize {
		return nil, ErrPQSignatureVerificationFailed
	}

	var pkBuf [mode2.PublicKeySize]byte
	copy(pkBuf[:], pubKey)
	var pk mode2.PublicKey
	pk.Unpack(&pkBuf)

	if !mode2.Verify(&pk, msg, sig) {
		return nil, ErrPQSignatureVerificationFailed
	}

	return pubKey, nil
}

// verifyDilithium3Signature verifies a Dilithium3 signature
func (s PostQuantumSigner) verifyDilithium3Signature(pubKey, msg, sig []byte) ([]byte, error) {
	if len(pubKey) != Dilithium3PublicKeySize {
		return nil, ErrPQInvalidPubKeySize
	}

	if len(sig) != Dilithium3SignatureSize {
		return nil, ErrPQSignatureVerificationFailed
	}

	var pkBuf [mode3.PublicKeySize]byte
	copy(pkBuf[:], pubKey)
	var pk mode3.PublicKey
	pk.Unpack(&pkBuf)

	if !mode3.Verify(&pk, msg, sig) {
		return nil, ErrPQSignatureVerificationFailed
	}

	return pubKey, nil
}

// derivePQAddress derives an Ethereum-style address from a PQ public key
// The address is computed as: Keccak256(pubKey)[12:]
func (s PostQuantumSigner) derivePQAddress(pubKey []byte) types.Address {
	h := crypto.Keccak256(pubKey)
	var addr types.Address
	copy(addr[:], h[12:])
	return addr
}

// SignatureValues returns the raw R, S, V values for the signature.
// For post-quantum transactions, these values are not used (PQ signatures
// are stored directly in the PQSignature field), so this returns nil values.
func (s PostQuantumSigner) SignatureValues(tx *Transaction, sig []byte) (r, ss, v *big.Int, err error) {
	if tx.Type() != PostQuantumTxType {
		return s.londonSigner.SignatureValues(tx, sig)
	}

	// For PQ transactions, the signature is stored directly in PQSignature
	// The legacy r, s, v fields are not used
	// Return nil values to indicate no ECDSA signature
	return nil, nil, nil, nil
}

// Hash returns the hash to be signed by the sender.
// For post-quantum transactions, this returns the SigningHash which excludes
// the PQ signature from the hash computation.
func (s PostQuantumSigner) Hash(tx *Transaction) (types.Hash, error) {
	if tx.Type() != PostQuantumTxType {
		return s.londonSigner.Hash(tx)
	}

	pqTx, ok := tx.inner.(*PostQuantumTx)
	if !ok {
		return types.Hash{}, ErrNotPostQuantumTx
	}

	// Return the signing hash (excludes PQSignature)
	return pqTx.SigningHash(), nil
}

// SignPQTransaction signs a post-quantum transaction with the given private key.
// Public key must be set before computing SigningHash() since PubKeyData is
// included in the hash. Each branch sets the public key first if needed.
func SignPQTransaction(tx *PostQuantumTx, privateKey interface{}) error {
	switch tx.SigAlgo {
	case PQAlgoFalcon512:
		sk, ok := privateKey.(*falcon.PrivateKey)
		if !ok {
			return errors.New("pq: invalid private key type for Falcon-512")
		}

		if len(tx.PubKeyData) == 0 {
			pk := sk.Public().(*falcon.PublicKey)
			tx.SetPubKey(pk.Bytes(), false)
		}

		h := tx.SigningHash()
		sig, err := falcon.Sign(sk, h[:])
		if err != nil {
			return err
		}
		tx.SetPQSignature(sig)
		return nil

	case PQAlgoSQIsign:
		return errors.New("pq: SQIsign signing not yet implemented")

	case PQAlgoDilithium2:
		sk2, ok := privateKey.(*mode2.PrivateKey)
		if !ok {
			return errors.New("pq: invalid private key type for Dilithium2")
		}

		// Set public key before computing signing hash, since PubKeyData
		// is included in SigningHash().
		if len(tx.PubKeyData) == 0 {
			pk := sk2.Public().(*mode2.PublicKey)
			tx.SetPubKey(pk.Bytes(), false)
		}

		h := tx.SigningHash()
		sig := make([]byte, mode2.SignatureSize)
		mode2.SignTo(sk2, h[:], sig)
		tx.SetPQSignature(sig)
		return nil

	case PQAlgoDilithium3:
		sk3, ok := privateKey.(*mode3.PrivateKey)
		if !ok {
			return errors.New("pq: invalid private key type for Dilithium3")
		}

		if len(tx.PubKeyData) == 0 {
			pk := sk3.Public().(*mode3.PublicKey)
			tx.SetPubKey(pk.Bytes(), false)
		}

		h := tx.SigningHash()
		sig := make([]byte, mode3.SignatureSize)
		mode3.SignTo(sk3, h[:], sig)
		tx.SetPQSignature(sig)
		return nil

	default:
		return ErrUnsupportedPQAlgorithm
	}
}

// SignNewPQTx creates and signs a new Falcon-512 post-quantum transaction.
func SignNewPQTx(sk *falcon.PrivateKey, txdata *PostQuantumTx) (*Transaction, error) {
	if err := SignPQTransaction(txdata, sk); err != nil {
		return nil, err
	}
	return NewTx(txdata), nil
}

// SignNewDilithium2Tx creates and signs a new Dilithium2 post-quantum transaction.
func SignNewDilithium2Tx(sk *mode2.PrivateKey, txdata *PostQuantumTx) (*Transaction, error) {
	if err := SignPQTransaction(txdata, sk); err != nil {
		return nil, err
	}
	return NewTx(txdata), nil
}

// SignNewDilithium3Tx creates and signs a new Dilithium3 post-quantum transaction.
func SignNewDilithium3Tx(sk *mode3.PrivateKey, txdata *PostQuantumTx) (*Transaction, error) {
	if err := SignPQTransaction(txdata, sk); err != nil {
		return nil, err
	}
	return NewTx(txdata), nil
}

// PQPublicKeyRegistry is an interface for looking up post-quantum public keys
// by their hash. This is used when transactions use hash references instead
// of full public keys.
type PQPublicKeyRegistry interface {
	// GetPublicKey retrieves a public key by its hash
	// Returns nil if the key is not found
	GetPublicKey(keyHash types.Hash) ([]byte, error)

	// RegisterPublicKey registers a public key and returns its hash
	RegisterPublicKey(pubKey []byte, algo uint8) (types.Hash, error)

	// HasPublicKey checks if a public key hash is registered
	HasPublicKey(keyHash types.Hash) bool
}

// ResolvePQPublicKey resolves a PQ public key from either full key or hash reference
// If the transaction contains a full public key, it returns it directly
// If the transaction contains a hash reference, it looks up the key in the registry
func ResolvePQPublicKey(tx *PostQuantumTx, registry PQPublicKeyRegistry) ([]byte, error) {
	if tx.PubKeyMode == 0 {
		// Full public key mode - return directly
		return tx.PubKeyData, nil
	}

	if len(tx.PubKeyData) != PQPublicKeyHashSize {
		return nil, ErrInvalidPQPublicKey
	}

	// Hash reference mode - look up in registry
	var keyHash types.Hash
	copy(keyHash[:], tx.PubKeyData)

	pubKey, err := registry.GetPublicKey(keyHash)
	if err != nil {
		return nil, ErrPQPubKeyNotFound
	}

	// Verify the hash matches
	computedHash := hash.Hash(pubKey)
	if computedHash != keyHash {
		return nil, ErrPQPubKeyMismatch
	}

	return pubKey, nil
}

// MakeSignerWithPQ returns a PQ-capable Signer if usePQ is true, otherwise a London signer.
func MakeSignerWithPQ(chainId *big.Int, usePQ bool) Signer {
	if usePQ {
		return NewPostQuantumSigner(chainId)
	}
	return NewLondonSigner(chainId)
}
