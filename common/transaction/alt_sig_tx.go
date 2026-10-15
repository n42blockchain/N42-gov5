// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"bytes"
	"crypto/ed25519"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/proto/types_pb"
)

const AltSigTxType = 0x50

// AltSigTx is the offline Ed25519 carrier. It is not enabled by any runtime
// ChainConfig. Signature authorization requires a separate protocol policy;
// bare crypto/ed25519.Verify is not sufficient to reject small-order keys.
// To is mandatory; creation transactions are not part of this format.
type AltSigTx struct {
	ChainID              uint64
	Nonce                uint64
	GasTipCap, GasFeeCap *uint256.Int
	Gas                  uint64
	To                   types.Address
	Value                *uint256.Int
	Data                 []byte
	AccessList           AccessList
	PublicKey            [ed25519.PublicKeySize]byte
	Signature            [ed25519.SignatureSize]byte
}

type altSigUnsignedRLP struct {
	ChainID              uint64
	Nonce                uint64
	GasTipCap, GasFeeCap *uint256.Int
	Gas                  uint64
	To                   types.Address
	Value                *uint256.Int
	Data                 []byte
	AccessList           AccessList
	Algorithm            uint64
	PublicKey            []byte
}
type altSigSignedRLP struct {
	ChainID              uint64
	Nonce                uint64
	GasTipCap, GasFeeCap *uint256.Int
	Gas                  uint64
	To                   types.Address
	Value                *uint256.Int
	Data                 []byte
	AccessList           AccessList
	Algorithm            uint64
	PublicKey            []byte
	Signature            []byte
}

func (tx *AltSigTx) validate() error {
	if nonNilUint256(tx.GasTipCap).BitLen() > 128 || nonNilUint256(tx.GasFeeCap).BitLen() > 128 {
		return fmt.Errorf("alt signature fees exceed uint128")
	}
	return nil
}
func (tx *AltSigTx) unsigned() altSigUnsignedRLP {
	return altSigUnsignedRLP{tx.ChainID, tx.Nonce, nonNilUint256(tx.GasTipCap), nonNilUint256(tx.GasFeeCap), tx.Gas, tx.To, nonNilUint256(tx.Value), tx.Data, tx.AccessList, 1, tx.PublicKey[:]}
}
func (tx *AltSigTx) encoded() ([]byte, error) {
	if err := tx.validate(); err != nil {
		return nil, err
	}
	u := tx.unsigned()
	return encodeTypedEthereumTransaction(AltSigTxType, altSigSignedRLP{u.ChainID, u.Nonce, u.GasTipCap, u.GasFeeCap, u.Gas, u.To, u.Value, u.Data, u.AccessList, u.Algorithm, u.PublicKey, tx.Signature[:]})
}

// AltSigSigningHash includes the algorithm and public key, but not the signature.
func AltSigSigningHash(tx *AltSigTx) (types.Hash, error) {
	if tx == nil {
		return types.Hash{}, fmt.Errorf("nil alt signature transaction")
	}
	if err := tx.validate(); err != nil {
		return types.Hash{}, err
	}
	enc, err := encodeTypedEthereumTransaction(AltSigTxType, tx.unsigned())
	if err != nil {
		return types.Hash{}, err
	}
	return crypto.Keccak256Hash(enc), nil
}

// AltSigAddress derives an address only; it does not authenticate a transaction.
func AltSigAddress(publicKey [ed25519.PublicKeySize]byte) types.Address {
	h := crypto.Keccak256Hash(append([]byte{1}, publicKey[:]...))
	return types.BytesToAddress(h[12:])
}

// SignAltTx signs a deep copy using the explicit Ed25519 path. Generic ECDSA
// WithSignature/WithSignatureValues cannot populate this carrier.
func SignAltTx(tx *AltSigTx, key ed25519.PrivateKey) (*Transaction, error) {
	if tx == nil || len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid alt signature signing input")
	}
	c := tx.copy().(*AltSigTx)
	// Reconstruct from the seed instead of trusting an inconsistent private-key suffix.
	key = ed25519.NewKeyFromSeed(key.Seed())
	copy(c.PublicKey[:], key.Public().(ed25519.PublicKey))
	hash, err := AltSigSigningHash(c)
	if err != nil {
		return nil, err
	}
	copy(c.Signature[:], ed25519.Sign(key, hash[:]))
	return NewTxOwned(c), nil
}

// DecodeAltSigTransactionOffline decodes canonical wire bytes without enabling
// runtime admission or asserting signature authorization.
func DecodeAltSigTransactionOffline(data []byte) (*Transaction, error) {
	if len(data) < 2 || data[0] != AltSigTxType {
		return nil, fmt.Errorf("not an alt signature transaction")
	}
	var w altSigSignedRLP
	if err := rlp.DecodeBytes(data[1:], &w); err != nil {
		return nil, err
	}
	if w.Algorithm != 1 || len(w.PublicKey) != 32 || len(w.Signature) != 64 {
		return nil, fmt.Errorf("invalid alt signature algorithm/key/signature shape")
	}
	t := &AltSigTx{ChainID: w.ChainID, Nonce: w.Nonce, GasTipCap: w.GasTipCap, GasFeeCap: w.GasFeeCap, Gas: w.Gas, To: w.To, Value: w.Value, Data: w.Data, AccessList: w.AccessList}
	copy(t.PublicKey[:], w.PublicKey)
	copy(t.Signature[:], w.Signature)
	enc, err := t.encoded()
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(enc, data) {
		return nil, fmt.Errorf("noncanonical alt signature transaction")
	}
	tx := NewTx(t)
	tx.cacheEncoded(types.CopyBytes(data))
	return tx, nil
}
func (tx *AltSigTx) copy() TxData {
	c := *tx
	c.Data = types.CopyBytes(tx.Data)
	c.AccessList = copyAccessList(tx.AccessList)
	c.GasTipCap = new(uint256.Int).Set(nonNilUint256(tx.GasTipCap))
	c.GasFeeCap = new(uint256.Int).Set(nonNilUint256(tx.GasFeeCap))
	c.Value = new(uint256.Int).Set(nonNilUint256(tx.Value))
	return &c
}
func (tx *AltSigTx) txType() byte                { return AltSigTxType }
func (tx *AltSigTx) chainID() *uint256.Int       { return uint256.NewInt(tx.ChainID) }
func (tx *AltSigTx) accessList() AccessList      { return tx.AccessList }
func (tx *AltSigTx) authList() AuthorizationList { return nil }
func (tx *AltSigTx) data() []byte                { return tx.Data }
func (tx *AltSigTx) gas() uint64                 { return tx.Gas }
func (tx *AltSigTx) gasPrice() *uint256.Int      { return tx.GasFeeCap }
func (tx *AltSigTx) gasTipCap() *uint256.Int     { return tx.GasTipCap }
func (tx *AltSigTx) gasFeeCap() *uint256.Int     { return tx.GasFeeCap }
func (tx *AltSigTx) value() *uint256.Int         { return tx.Value }
func (tx *AltSigTx) nonce() uint64               { return tx.Nonce }
func (tx *AltSigTx) to() *types.Address          { return &tx.To }
func (tx *AltSigTx) from() *types.Address        { return nil }
func (tx *AltSigTx) sign() []byte {
	return append(append(make([]byte, 0, 96), tx.PublicKey[:]...), tx.Signature[:]...)
}
func (tx *AltSigTx) hash() types.Hash {
	enc, err := tx.encoded()
	if err != nil {
		return types.Hash{}
	}
	return crypto.Keccak256Hash(enc)
}
func (tx *AltSigTx) rawSignatureValues() (v, r, s *uint256.Int)       { return nil, nil, nil }
func (tx *AltSigTx) setSignatureValues(chainID, v, r, s *uint256.Int) {} // blocked by public ECDSA setters

// Check the complete carrier before using legacy protobuf conversion helpers,
// which assume well-formed nested H128/H256 messages.
func altSigFromProto(pbTx *types_pb.Transaction) (TxData, error) {
	if pbTx.To == nil || pbTx.To.Hi == nil || len(pbTx.Sign) != 96 || pbTx.From != nil || pbTx.V != nil || pbTx.R != nil || pbTx.S != nil || pbTx.GasPrice != nil || pbTx.PqSigAlgo != 0 || pbTx.PqPubKeyMode != 0 || len(pbTx.PqPubKeyData) != 0 || len(pbTx.PqSignature) != 0 || pbTx.BlobFeeCap != nil || len(pbTx.BlobHashes) != 0 {
		return nil, fmt.Errorf("invalid alt signature protobuf carrier")
	}
	validH256 := func(v *types_pb.H256) bool { return v != nil && v.Hi != nil && v.Lo != nil }
	for _, v := range []*types_pb.H256{pbTx.Value, pbTx.FeePerGas, pbTx.PriorityFeePerGas} {
		if v != nil && !validH256(v) {
			return nil, fmt.Errorf("invalid alt signature integer")
		}
	}
	for _, a := range pbTx.AccessList {
		if a == nil || a.Address == nil || a.Address.Hi == nil {
			return nil, fmt.Errorf("invalid alt signature access address")
		}
		for _, h := range a.StorageKeys {
			if !validH256(h) {
				return nil, fmt.Errorf("invalid alt signature storage key")
			}
		}
	}
	t := &AltSigTx{ChainID: pbTx.ChainID, Nonce: pbTx.Nonce, Gas: pbTx.Gas, GasTipCap: utils.ConvertH256ToUint256Int(pbTx.PriorityFeePerGas), GasFeeCap: utils.ConvertH256ToUint256Int(pbTx.FeePerGas), Value: utils.ConvertH256ToUint256Int(pbTx.Value), To: *convertProtoToAddress(pbTx.To), Data: pbTx.Data, AccessList: convertProtoAccessList(pbTx.AccessList)}
	copy(t.PublicKey[:], pbTx.Sign[:32])
	copy(t.Signature[:], pbTx.Sign[32:])
	if err := t.validate(); err != nil {
		return nil, err
	}
	return t, nil
}
