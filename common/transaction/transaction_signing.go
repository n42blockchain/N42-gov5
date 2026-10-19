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
//
// N42 transaction signer entry points. MakeSigner picks a Signer
// from ChainConfig + block number for the active fork, sigCache
// memoizes derived sender addresses per signer, and the Err*
// sentinels flag bad V/R/S values, unsupported tx types, wrong
// chain IDs and malformed signatures. Uses secp256k1 via the
// project crypto package plus common/u256 for V/R/S big math.

package transaction

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/u256"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

var (
	ErrInvalidSig           = errors.New("invalid transaction v, r, s values")
	ErrTxTypeNotSupported   = errors.New("transaction type not supported")
	ErrInvalidChainId       = errors.New("invalid chain id for signer")
	ErrInvalidSignatureSize = errors.New("invalid signature size")
)

// sigCache is used to cache the derived sender and contains
// the signer used to derive it.
type sigCache struct {
	signer Signer
	from   types.Address
}

// MakeSigner returns a Signer based on the given chain config and block number.
func MakeSigner(config *params.ChainConfig, blockNumber *big.Int) Signer {
	blockNum := uint64(0)
	if blockNumber != nil {
		blockNum = blockNumber.Uint64()
	}
	return MakeSignerWithTimestamp(config, blockNumber, blockNum)
}

// signerCache memoises the four chainID-bound signers per chain.
// The hot replay path calls MakeSignerWithTimestamp once per
// transaction; without caching, each call rebuilds the EIP-155 /
// EIP-2930 / London signer trio, allocating two new big.Int values
// (chainId, chainIdMul). Profile showed 1.55B big.Int allocs traced
// to MakeSignerWithTimestamp over a 12h replay. Single-chain workload
// → single entry suffices.
type signerCache struct {
	mu      sync.RWMutex
	chainID *big.Int
	london  Signer
	eip2930 Signer
	eip155  EIP155Signer
}

var cachedSigners signerCache

func cachedFor(chainID *big.Int) (london, eip2930 Signer, eip155 EIP155Signer) {
	cachedSigners.mu.RLock()
	if cachedSigners.chainID != nil && cachedSigners.chainID.Cmp(chainID) == 0 {
		london, eip2930, eip155 = cachedSigners.london, cachedSigners.eip2930, cachedSigners.eip155
		cachedSigners.mu.RUnlock()
		return
	}
	cachedSigners.mu.RUnlock()

	cachedSigners.mu.Lock()
	defer cachedSigners.mu.Unlock()
	if cachedSigners.chainID == nil || cachedSigners.chainID.Cmp(chainID) != 0 {
		cachedSigners.chainID = new(big.Int).Set(chainID)
		cachedSigners.eip155 = NewEIP155Signer(chainID)
		cachedSigners.eip2930 = NewEIP2930Signer(chainID)
		cachedSigners.london = NewLondonSigner(chainID)
	}
	return cachedSigners.london, cachedSigners.eip2930, cachedSigners.eip155
}

// MakeSignerWithTimestamp returns a Signer based on the given chain config, block
// number, and block timestamp. This is required for Prague+ time-based forks.
func MakeSignerWithTimestamp(config *params.ChainConfig, blockNumber *big.Int, timestamp uint64) Signer {
	if config == nil {
		return FrontierSigner{}
	}
	blockNum := uint64(0)
	if blockNumber != nil {
		blockNum = blockNumber.Uint64()
	}
	rules := config.RulesWithTimestamp(blockNum, timestamp)
	switch {
	case rules.IsLondon:
		if config.ChainID == nil {
			return NewLondonSigner(nil)
		}
		s, _, _ := cachedFor(config.ChainID)
		return s
	case rules.IsBerlin:
		if config.ChainID == nil {
			return NewEIP2930Signer(nil)
		}
		_, s, _ := cachedFor(config.ChainID)
		return s
	case rules.IsSpuriousDragon, rules.IsEip1559FeeCollector:
		if config.ChainID == nil {
			return NewEIP155Signer(nil)
		}
		_, _, s := cachedFor(config.ChainID)
		return s
	case rules.IsHomestead:
		return HomesteadSigner{}
	default:
		return FrontierSigner{}
	}
}

// LatestSignerForChainID returns the 'most permissive' Signer available. Specifically,
// this enables support for EIP-155 replay protection and all implemented EIP-2718
// transaction types if chainID is non-nil.
//
// Use this in transaction-handling code where the current block number and fork
// configuration are unknown. If you have a ChainConfig, use LatestSigner instead.
// If you have a ChainConfig and know the current block number, use MakeSigner instead.
func LatestSignerForChainID(chainID *big.Int) Signer {
	if chainID == nil {
		return HomesteadSigner{}
	}
	return NewLondonSigner(chainID)
}

// SignNewTx creates a transaction and signs it.
func SignNewTx(prv *ecdsa.PrivateKey, s Signer, txdata TxData) (*Transaction, error) {
	tx := NewTx(txdata)
	h, err := s.Hash(tx)
	if err != nil {
		return nil, err
	}
	sig, err := crypto.Sign(h[:], prv)
	if err != nil {
		return nil, err
	}
	return tx.WithSignature(s, sig)
}

// SignTx signs the transaction using the given signer and private key.
func SignTx(tx *Transaction, s Signer, prv *ecdsa.PrivateKey) (*Transaction, error) {
	h, err := s.Hash(tx)
	if err != nil {
		return nil, err
	}
	sig, err := crypto.Sign(h[:], prv)
	if err != nil {
		return nil, err
	}
	return tx.WithSignature(s, sig)
}

// Sender returns the address derived from the signature (V, R, S) using secp256k1
// elliptic curve and an error if it failed deriving or upon an incorrect
// signature.
//
// Sender may cache the address, allowing it to be used regardless of
// signing method. The cache is invalidated if the cached signer does
// not match the signer used in the current call.
// RecoverSenderFromSig recovers the sender from the transaction's
// SIGNATURE, deliberately ignoring any cached/wire-declared From on the
// transaction object. It still consults the process-wide senderCache
// (keyed by tx hash + signer), whose entries are themselves only ever
// signature-recovered results — so honest, pool-seen transactions hit the
// cache while a forged From can never be validated by it. Used by the
// import-time sender-verification gate.
// CachedSender answers from the process-wide sender cache alone: the address
// a signature recovery under this signer produced earlier for this
// transaction hash, or false. No recovery, no pool, no lock beyond an atomic
// load -- the import's verification asks this first when a hint feed has
// been filling the cache ahead of the block.
func CachedSender(signer Signer, tx *Transaction) (types.Address, bool) {
	if signer == nil || tx == nil {
		return types.Address{}, false
	}
	return senderCacheGet(tx.Hash(), signer)
}

func RecoverSenderFromSig(signer Signer, tx *Transaction) (types.Address, error) {
	hash := tx.Hash()
	if addr, ok := senderCacheGet(hash, signer); ok {
		return addr, nil
	}
	addr, err := signer.Sender(tx)
	if err != nil {
		return types.Address{}, err
	}
	senderCachePut(hash, signer, addr)
	return addr, nil
}

func Sender(signer Signer, tx *Transaction) (types.Address, error) {
	if sc := tx.from.Load(); sc != nil {
		sigCache := *sc
		// If the signer used to derive from in a previous
		// call is not the same as used current, invalidate
		// the cache.
		if sigCache.signer.Equal(signer) {
			return sigCache.from, nil
		}
	}

	// The per-object memo above only helps a caller holding this exact
	// Transaction. A block arriving from the wire is decoded into fresh values,
	// so importing re-recovers senders the pool already recovered when the
	// transactions arrived. Consult the process-wide cache before paying for
	// another ecdsa recovery; see sender_cache.go.
	hash := tx.Hash()
	if addr, ok := senderCacheGet(hash, signer); ok {
		tx.storeSenderMemo(signer, addr)
		return addr, nil
	}

	addr, err := signer.Sender(tx)
	if err != nil {
		return types.Address{}, err
	}
	tx.storeSenderMemo(signer, addr)
	senderCachePut(hash, signer, addr)
	return addr, nil
}

// CacheSender records an already-recovered sender for tx under signer, in
// both the per-object memo and the process-wide cache, so the next Sender call
// on this transaction (or on a fresh decode of the same bytes) is a lookup
// rather than another ECDSA recovery. The caller vouches that from was
// recovered from this transaction's signature under an equivalent signer; it
// is NOT a way to assert an unverified sender.
func (tx *Transaction) CacheSender(signer Signer, from types.Address) {
	if signer == nil {
		return
	}
	tx.storeSenderMemo(signer, from)
	senderCachePut(tx.Hash(), signer, from)
}

// SetSenderMemo records an already-recovered sender in this transaction
// object's memo only (not the process-wide cache), keyed by signer: a later
// Sender call under an Equal signer returns it, any other signer
// re-derives. The caller vouches that from was recovered from this
// transaction's signature under signer (frame admission recovers the
// senders of a frame in the background and attaches them when the frame is
// decoded into a block).
func (tx *Transaction) SetSenderMemo(signer Signer, from types.Address) {
	if signer == nil {
		return
	}
	tx.storeSenderMemo(signer, from)
}

// Signer encapsulates transaction signature handling. The name of this type is slightly
// misleading because Signers don't actually sign, they're just for validating and
// processing of signatures.
//
// Note that this interface is not a stable API and may change at any time to accommodate
// new protocol rules.
type Signer interface {
	// Sender returns the sender address of the transaction.
	Sender(tx *Transaction) (types.Address, error)

	// SignatureValues returns the raw R, S, V values corresponding to the
	// given signature.
	SignatureValues(tx *Transaction, sig []byte) (r, s, v *big.Int, err error)
	ChainID() *big.Int

	// Hash returns 'signature hash', i.e. the transaction hash that is signed by the
	// private key. This hash does not uniquely identify the transaction.
	// Returns an error if the transaction type is not supported by this signer.
	Hash(tx *Transaction) (types.Hash, error)

	// Equal returns true if the given signer is the same as the receiver.
	Equal(Signer) bool
}

type londonSigner struct{ eip2930Signer }

// NewLondonSigner returns a signer that accepts
// - EIP-1559 dynamic fee transactions
// - EIP-2930 access list transactions,
// - EIP-155 replay protected transactions, and
// - legacy Homestead transactions.
func NewLondonSigner(chainId *big.Int) Signer {
	return londonSigner{eip2930Signer{NewEIP155Signer(chainId)}}
}

func (s londonSigner) Sender(tx *Transaction) (types.Address, error) {
	switch tx.Type() {
	case DynamicFeeTxType, BlobTxType, SetCodeTxType:
		return recoverTypedSender(tx, s.chainId, func() (types.Hash, error) {
			return s.Hash(tx)
		})
	default:
		return s.eip2930Signer.Sender(tx)
	}
}

func (s londonSigner) Equal(s2 Signer) bool {
	x, ok := s2.(londonSigner)
	return ok && x.chainId.Cmp(s.chainId) == 0
}

func (s londonSigner) SignatureValues(tx *Transaction, sig []byte) (R, S, V *big.Int, err error) {
	switch txdata := tx.inner.(type) {
	case *DynamicFeeTx:
		return signatureValuesForTypedTx(txdata.ChainID, s.chainId, sig)
	case *BlobTx:
		return signatureValuesForTypedTx(txdata.ChainID, s.chainId, sig)
	case *SetCodeTx:
		return signatureValuesForTypedTx(txdata.ChainID, s.chainId, sig)
	default:
		return s.eip2930Signer.SignatureValues(tx, sig)
	}
}

// Hash returns the hash to be signed by the sender.
// It does not uniquely identify the transaction.
func (s londonSigner) Hash(tx *Transaction) (types.Hash, error) {
	switch tx.Type() {
	case DynamicFeeTxType:
		return hash.PrefixedRlpHash(
			tx.Type(),
			[]interface{}{
				s.chainId,
				tx.Nonce(),
				tx.GasTipCap(),
				tx.GasFeeCap(),
				tx.Gas(),
				tx.To(),
				tx.Value(),
				tx.Data(),
				tx.AccessList(),
			}), nil
	case BlobTxType:
		return hash.PrefixedRlpHash(
			tx.Type(),
			[]interface{}{
				s.chainId,
				tx.Nonce(),
				tx.GasTipCap(),
				tx.GasFeeCap(),
				tx.Gas(),
				tx.To(),
				tx.Value(),
				tx.Data(),
				tx.AccessList(),
				tx.BlobFeeCap(),
				tx.BlobHashes(),
			}), nil
	case SetCodeTxType:
		return hash.PrefixedRlpHash(
			tx.Type(),
			[]interface{}{
				s.chainId,
				tx.Nonce(),
				tx.GasTipCap(),
				tx.GasFeeCap(),
				tx.Gas(),
				tx.To(),
				tx.Value(),
				tx.Data(),
				tx.AccessList(),
				tx.AuthList(),
			}), nil
	default:
		return s.eip2930Signer.Hash(tx)
	}
}

func recoverTypedSender(tx *Transaction, expectedChainID *big.Int, hashFn func() (types.Hash, error)) (types.Address, error) {
	V, R, S := tx.RawSignatureValues()
	V1 := new(big.Int).Add(V.ToBig(), big.NewInt(27))
	chainId, _ := uint256.FromBig(expectedChainID)
	id := tx.ChainId()
	if id.Cmp(chainId) != 0 {
		return types.Address{}, ErrInvalidChainId
	}
	h, err := hashFn()
	if err != nil {
		return types.Address{}, err
	}
	return recoverPlainRS(h, R, S, V1, true)
}

// legacySenderFast is EIP155Signer.Sender (and eip2930Signer.Sender) of a
// replay-protected legacy transaction in uint64 arithmetic, for the case
// every real transaction is in: V, the chain id and twice the chain id fit
// in 64 bits and V >= 2*chainId+8. ok=false leaves the transaction to the
// big.Int path (unprotected, or any other V), which decides the same way;
// on that path the protection check, the chain id derivation, FromBig and
// the V arithmetic were five allocations per recovery (~40 bytes per
// imported transaction).
//
// Equivalence with the big.Int path, step by step: Protected is
// isProtectedV(V), which for V < 256 excludes 0, 1, 27 and 28 and is true
// above; the chain id is DeriveChainId(V) = (V-35)/2 in uint64 arithmetic
// (V is neither 27 nor 28 here), compared with FromBig(chainId) = chainId;
// the recovery value V-chainIdMul-8 is non-negative here, so its BitLen and
// Uint64 are those of the uint64 difference (recoverPlainV).
func (s EIP155Signer) legacySenderFast(tx *Transaction) (addr types.Address, err error, ok bool) {
	inner, isLegacy := tx.inner.(*LegacyTx)
	if !isLegacy || legacySenderFastOffForTest || inner.V == nil || !inner.V.IsUint64() || s.chainId == nil || s.chainId.Sign() < 0 ||
		!s.chainId.IsUint64() || s.chainIdMul == nil || s.chainIdMul.Sign() < 0 || !s.chainIdMul.IsUint64() {
		return types.Address{}, nil, false
	}
	v := inner.V.Uint64()
	if v < 256 && (v == 27 || v == 28 || v == 1 || v == 0) {
		return types.Address{}, nil, false // unprotected: the Homestead path
	}
	mul := s.chainIdMul.Uint64()
	if mul > math.MaxUint64-8 || v < mul+8 {
		return types.Address{}, nil, false
	}
	if (v-35)/2 != s.chainId.Uint64() {
		return types.Address{}, ErrInvalidChainId, true
	}
	h, herr := s.Hash(tx)
	if herr != nil {
		return types.Address{}, herr, true
	}
	addr, err = recoverPlainV(h, inner.R, inner.S, v-mul-8, true)
	return addr, err, true
}

// legacySenderFastOffForTest sends every legacy recovery down the big.Int
// path (tests compare the two).
var legacySenderFastOffForTest bool

func signatureValuesForTypedTx(txChainID *uint256.Int, signerChainID *big.Int, sig []byte) (R, S, V *big.Int, err error) {
	chainId, _ := uint256.FromBig(signerChainID)
	if txChainID != nil && txChainID.Sign() != 0 && txChainID.Cmp(chainId) != 0 {
		return nil, nil, nil, ErrInvalidChainId
	}
	R, S, _, err = decodeSignature(sig)
	if err != nil {
		return nil, nil, nil, err
	}
	V = big.NewInt(int64(sig[64]))
	return R, S, V, nil
}

type eip2930Signer struct{ EIP155Signer }

// NewEIP2930Signer returns a signer that accepts EIP-2930 access list transactions,
// EIP-155 replay protected transactions, and legacy Homestead transactions.
func NewEIP2930Signer(chainId *big.Int) Signer {
	return eip2930Signer{NewEIP155Signer(chainId)}
}

func (s eip2930Signer) ChainID() *big.Int {
	return s.chainId
}

func (s eip2930Signer) Equal(s2 Signer) bool {
	x, ok := s2.(eip2930Signer)
	return ok && x.chainId.Cmp(s.chainId) == 0
}

func (s eip2930Signer) Sender(tx *Transaction) (types.Address, error) {
	if addr, err, ok := s.EIP155Signer.legacySenderFast(tx); ok {
		return addr, err
	}
	V, R, S := tx.RawSignatureValues()
	V1 := V.ToBig()
	switch tx.Type() {
	case LegacyTxType:
		if !tx.Protected() {
			return HomesteadSigner{}.Sender(tx)
		}
		V1 = new(big.Int).Sub(V1, s.chainIdMul)
		V1.Sub(V1, big8)
	case AccessListTxType:
		// AL txs are defined to use 0 and 1 as their recovery
		// id, add 27 to become equivalent to unprotected Homestead signatures.
		V1 = new(big.Int).Add(V1, big.NewInt(27))
	default:
		return types.Address{}, ErrTxTypeNotSupported
	}
	chainId, _ := uint256.FromBig(s.chainId)
	id := tx.ChainId()
	if id.Cmp(chainId) != 0 {
		return types.Address{}, ErrInvalidChainId
	}
	h, err := s.Hash(tx)
	if err != nil {
		return types.Address{}, err
	}
	return recoverPlainRS(h, R, S, V1, true)
}

func (s eip2930Signer) SignatureValues(tx *Transaction, sig []byte) (R, S, V *big.Int, err error) {
	switch txdata := tx.inner.(type) {
	case *LegacyTx:
		return s.EIP155Signer.SignatureValues(tx, sig)
	case *AccessListTx:
		// Check that chain ID of tx matches the signer. We also accept ID zero here,
		// because it indicates that the chain ID was not specified in the tx.
		chainId, _ := uint256.FromBig(s.chainId)
		if txdata.ChainID.Sign() != 0 && txdata.ChainID.Cmp(chainId) != 0 {
			return nil, nil, nil, ErrInvalidChainId
		}
		R, S, _, err = decodeSignature(sig)
		if err != nil {
			return nil, nil, nil, err
		}
		V = big.NewInt(int64(sig[64]))
	default:
		return nil, nil, nil, ErrTxTypeNotSupported
	}
	return R, S, V, nil
}

// Hash returns the hash to be signed by the sender.
// It does not uniquely identify the transaction.
// Returns ErrTxTypeNotSupported if the transaction type is not supported.
func (s eip2930Signer) Hash(tx *Transaction) (types.Hash, error) {
	switch tx.Type() {
	case LegacyTxType:
		return legacySigningHash(tx, s.chainId), nil
	case AccessListTxType:
		return hash.PrefixedRlpHash(
			tx.Type(),
			[]interface{}{
				s.chainId,
				tx.Nonce(),
				tx.GasPrice(),
				tx.Gas(),
				tx.To(),
				tx.Value(),
				tx.Data(),
				tx.AccessList(),
			}), nil
	default:
		return types.Hash{}, ErrTxTypeNotSupported
	}
}

// EIP155Signer implements Signer using the EIP-155 rules. This accepts transactions which
// are replay-protected as well as unprotected homestead transactions.
type EIP155Signer struct {
	chainId, chainIdMul *big.Int
}

func NewEIP155Signer(chainId *big.Int) EIP155Signer {
	if chainId == nil {
		chainId = new(big.Int)
	}
	return EIP155Signer{
		chainId:    chainId,
		chainIdMul: new(big.Int).Mul(chainId, big.NewInt(2)),
	}
}

func (s EIP155Signer) ChainID() *big.Int {
	return s.chainId
}

func (s EIP155Signer) Equal(s2 Signer) bool {
	eip155, ok := s2.(EIP155Signer)
	return ok && eip155.chainId.Cmp(s.chainId) == 0
}

var big8 = big.NewInt(8)

func (s EIP155Signer) Sender(tx *Transaction) (types.Address, error) {
	if tx.Type() != LegacyTxType {
		return types.Address{}, ErrTxTypeNotSupported
	}
	if addr, err, ok := s.legacySenderFast(tx); ok {
		return addr, err
	}
	if !tx.Protected() {
		return HomesteadSigner{}.Sender(tx)
	}
	chainId, _ := uint256.FromBig(s.chainId)
	id := tx.ChainId()
	if id.Cmp(chainId) != 0 {
		return types.Address{}, ErrInvalidChainId
	}
	V, R, S := tx.RawSignatureValues()
	V1 := V.ToBig()
	V1 = new(big.Int).Sub(V1, s.chainIdMul)
	V1.Sub(V1, big8)
	h, err := s.Hash(tx)
	if err != nil {
		return types.Address{}, err
	}
	return recoverPlainRS(h, R, S, V1, true)
}

// SignatureValues returns signature values. This signature
// needs to be in the [R || S || V] format where V is 0 or 1.
func (s EIP155Signer) SignatureValues(tx *Transaction, sig []byte) (R, S, V *big.Int, err error) {
	if tx.Type() != LegacyTxType {
		return nil, nil, nil, ErrTxTypeNotSupported
	}
	R, S, V, err = decodeSignature(sig)
	if err != nil {
		return nil, nil, nil, err
	}
	if s.chainId.Sign() != 0 {
		V = big.NewInt(int64(sig[64] + 35))
		V.Add(V, s.chainIdMul)
	}
	return R, S, V, nil
}

// Hash returns the hash to be signed by the sender.
// It does not uniquely identify the transaction.
func (s EIP155Signer) Hash(tx *Transaction) (types.Hash, error) {
	return legacySigningHash(tx, s.chainId), nil
}

// HomesteadTransaction implements TransactionInterface using the
// homestead rules.
type HomesteadSigner struct{ FrontierSigner }

func (s HomesteadSigner) ChainID() *big.Int {
	return nil
}

func (s HomesteadSigner) Equal(s2 Signer) bool {
	_, ok := s2.(HomesteadSigner)
	return ok
}

// SignatureValues returns signature values. This signature
// needs to be in the [R || S || V] format where V is 0 or 1.
func (hs HomesteadSigner) SignatureValues(tx *Transaction, sig []byte) (r, s, v *big.Int, err error) {
	return hs.FrontierSigner.SignatureValues(tx, sig)
}

func (hs HomesteadSigner) Sender(tx *Transaction) (types.Address, error) {
	if tx.Type() != LegacyTxType {
		return types.Address{}, ErrTxTypeNotSupported
	}
	v, r, s := tx.RawSignatureValues()
	h, err := hs.Hash(tx)
	if err != nil {
		return types.Address{}, err
	}
	return recoverPlainRS(h, r, s, v.ToBig(), true)
}

type FrontierSigner struct{}

func (s FrontierSigner) ChainID() *big.Int {
	return nil
}

func (s FrontierSigner) Equal(s2 Signer) bool {
	_, ok := s2.(FrontierSigner)
	return ok
}

func (fs FrontierSigner) Sender(tx *Transaction) (types.Address, error) {
	if tx.Type() != LegacyTxType {
		return types.Address{}, ErrTxTypeNotSupported
	}
	v, r, s := tx.RawSignatureValues()
	h, err := fs.Hash(tx)
	if err != nil {
		return types.Address{}, err
	}
	return recoverPlainRS(h, r, s, v.ToBig(), false)
}

// SignatureValues returns signature values. This signature
// needs to be in the [R || S || V] format where V is 0 or 1.
func (fs FrontierSigner) SignatureValues(tx *Transaction, sig []byte) (r, s, v *big.Int, err error) {
	if tx.Type() != LegacyTxType {
		return nil, nil, nil, ErrTxTypeNotSupported
	}
	return decodeSignature(sig)
}

// Hash returns the hash to be signed by the sender.
// It does not uniquely identify the transaction.
func (fs FrontierSigner) Hash(tx *Transaction) (types.Hash, error) {
	return hash.RlpHash([]interface{}{
		tx.Nonce(),
		tx.GasPrice(),
		tx.Gas(),
		tx.To(),
		tx.Value(),
		tx.Data(),
	}), nil
}

func decodeSignature(sig []byte) (r, s, v *big.Int, err error) {
	if len(sig) != crypto.SignatureLength {
		return nil, nil, nil, fmt.Errorf("%w: got %d, want %d", ErrInvalidSignatureSize, len(sig), crypto.SignatureLength)
	}
	r = new(big.Int).SetBytes(sig[:32])
	s = new(big.Int).SetBytes(sig[32:64])
	v = new(big.Int).SetBytes([]byte{sig[64] + 27})
	return r, s, v, nil
}

func recoverPlain(sighash types.Hash, R, S, Vb *big.Int, homestead bool) (types.Address, error) {
	if R == nil || S == nil {
		return types.Address{}, ErrInvalidSig
	}
	lr, _ := uint256.FromBig(R)
	ls, _ := uint256.FromBig(S)
	return recoverPlainRS(sighash, lr, ls, Vb, homestead)
}

// recoverPlainRS is recoverPlain with r and s in the form every caller already
// holds them: RawSignatureValues returns uint256, and the old signature forced
// each one out to a big.Int and straight back again.
//
// Six allocations per recovery went into that round trip — two ToBig, two
// FromBig and two big.Int.Bytes — on the single hottest path in the node
// (secp256k1_ext_ecdsa_recover is 94% of all cgo time under load). On the 480M
// rig they were 65M and 44M objects, together about 6% of everything the node
// allocated.
//
// The recovery id keeps its big.Int path deliberately. v is one small
// allocation, but the arithmetic each signer does on it — subtracting
// chainIdMul, going negative on a malformed input, then BitLen and Uint64 —
// does NOT carry over to a wrapping uint256, and sender recovery is not a
// place to change behaviour on malformed input.
//
// Byte-for-byte the same signature buffer: WriteToSlice fills 32 bytes
// big-endian zero-padded, which is what copy(sig[32-len(r):32], r) built.
func recoverPlainRS(sighash types.Hash, R, S *uint256.Int, Vb *big.Int, homestead bool) (types.Address, error) {
	if R == nil || S == nil || Vb == nil {
		return types.Address{}, ErrInvalidSig
	}
	if Vb.BitLen() > 8 {
		return types.Address{}, ErrInvalidSig
	}
	return recoverPlainV(sighash, R, S, Vb.Uint64(), homestead)
}

// recoverPlainV is recoverPlainRS with the recovery value as Vb.Uint64()
// (Vb of at most 8 bits), or as a non-negative uint64 of any size.
func recoverPlainV(sighash types.Hash, R, S *uint256.Int, vb uint64, homestead bool) (types.Address, error) {
	if R == nil || S == nil || vb >= 256 {
		return types.Address{}, ErrInvalidSig
	}
	V := byte(vb - 27)
	if !crypto.ValidateSignatureValues(V, R, S, homestead) {
		return types.Address{}, ErrInvalidSig
	}
	// encode the signature in uncompressed format
	sc := recoverScratchPool.Get().(*recoverScratch)
	defer recoverScratchPool.Put(sc)
	sc.msg = sighash
	sig := sc.sig[:]
	R.WriteToSlice(sig[:32])
	S.WriteToSlice(sig[32:64])
	sig[64] = V
	if countRecoveries.Load() {
		recoveries.Add(1)
	}
	// recover the public key from the signature
	pub, err := crypto.EcrecoverInto(sc.msg[:], sig, sc.pub[:0])
	if err != nil {
		return types.Address{}, err
	}
	if len(pub) == 0 || pub[0] != 4 {
		return types.Address{}, errors.New("invalid public key")
	}
	return pubkeyAddress(pub[1:]), nil
}

// pubkeyAddress is the address of a 64-byte uncompressed public key (X||Y):
// the low 20 bytes of its Keccak-256. 64 bytes fit one Keccak rate block, so
// with N42_KECCAK_ONEBLOCK on (the default) this is one permutation on a
// stack state; off, it is the pooled sponge (Keccak256Hash keeps the digest
// on the stack, where Keccak256 allocates it). Runs once per recovery.
func pubkeyAddress(xy []byte) (addr types.Address) {
	var h types.Hash
	ok := false
	if crypto.KeccakOneBlockEnabled {
		h, ok = crypto.Keccak256OneBlock(xy)
	}
	if !ok {
		h = crypto.Keccak256Hash(xy)
	}
	copy(addr[:], h[12:])
	return addr
}

// recoverScratch holds a recovery's buffers. The signature hash, the
// signature and the public key reach the secp256k1 cgo call, so buffers on
// recoverPlainRS's stack would escape: four allocations per recovery (the
// hash moved to the heap, the signature, the key and its Keccak digest),
// ~90 bytes per imported transaction. A pooled scratch is pointer-free and
// none of it outlives the call.
type recoverScratch struct {
	msg types.Hash
	sig [crypto.SignatureLength]byte
	pub [65]byte
}

var recoverScratchPool = sync.Pool{New: func() any { return new(recoverScratch) }}

// deriveChainID derives the chain id from the given v parameter
func DeriveChainId(v *uint256.Int) *uint256.Int {
	if v == nil {
		return new(uint256.Int)
	}
	if v.IsUint64() {
		v := v.Uint64()
		if v == 27 || v == 28 {
			return new(uint256.Int)
		}
		return new(uint256.Int).SetUint64((v - 35) / 2)
	}
	r := new(uint256.Int).Sub(v, u256.Num35)
	return r.Div(r, u256.Num2)
}

// Sender recovery accounting for tests (P8a): when on, every secp256k1
// sender recovery is counted. Off in production: one atomic load per
// recovery, no shared counter traffic.
var (
	countRecoveries atomic.Bool
	recoveries      atomic.Uint64
)

// CountSenderRecoveriesForTest starts counting sender recoveries from zero
// and returns a function that stops counting and returns the count.
func CountSenderRecoveriesForTest() func() uint64 {
	recoveries.Store(0)
	countRecoveries.Store(true)
	return func() uint64 {
		countRecoveries.Store(false)
		return recoveries.Load()
	}
}

// SenderRecoveriesForTest returns the count so far (counting stays on).
func SenderRecoveriesForTest() uint64 { return recoveries.Load() }

// HasSenderMemo reports whether tx's memo already holds a sender derived
// under signer (an atomic load and a signer comparison; no hashing).
func (tx *Transaction) HasSenderMemo(signer Signer) bool {
	if signer == nil {
		return false
	}
	sc := tx.from.Load()
	if sc == nil {
		return false
	}
	return sc.signer.Equal(signer)
}

// storeSenderMemo publishes the per-object sender memo. The first memo of a
// transaction is written into its own slot (claimed once, so a reader
// holding the slot's pointer never sees it change); a later memo, under
// another signer, gets its own allocation. Boxing every memo into an
// interface was one allocation per transaction on every import (P14).
func (tx *Transaction) storeSenderMemo(signer Signer, from types.Address) {
	if tx.fromSlotUsed.CompareAndSwap(false, true) {
		tx.fromSlot = sigCache{signer: signer, from: from}
		tx.from.Store(&tx.fromSlot)
		return
	}
	tx.from.Store(&sigCache{signer: signer, from: from})
}
