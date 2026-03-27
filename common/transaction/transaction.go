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

package transaction

import (
	"bytes"
	"fmt"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"google.golang.org/protobuf/proto"
)

var (
	ErrGasFeeCapTooLow = fmt.Errorf("fee cap less than base fee")
	ErrUnmarshalHash   = fmt.Errorf("transaction hash verification failed")
)

// Transaction types.
const (
	LegacyTxType = iota
	AccessListTxType
	DynamicFeeTxType
	_ // Reserved for SetCodeTxType (EIP-7702) = 0x04
	_ // PostQuantumTxType is defined in pq_transaction.go = 0x05
)

type TxData interface {
	txType() byte
	copy() TxData

	chainID() *uint256.Int
	accessList() AccessList
	authList() AuthorizationList
	data() []byte
	gas() uint64
	gasPrice() *uint256.Int
	gasTipCap() *uint256.Int
	gasFeeCap() *uint256.Int
	value() *uint256.Int
	nonce() uint64
	to() *types.Address
	from() *types.Address
	sign() []byte
	hash() types.Hash

	rawSignatureValues() (v, r, s *uint256.Int)
	setSignatureValues(chainID, v, r, s *uint256.Int)
}

// Transactions implements DerivableList for transactions.
type Transactions []*Transaction

func (s Transactions) Len() int { return len(s) }

// EncodeIndex encodes the i'th transaction to w (legacy format for DeriveSha).
func (s Transactions) EncodeIndex(i int, w *bytes.Buffer) {
	b, _ := s[i].Marshal()
	w.Write(b)
}

// TransactionsV2 wraps Transactions with v2 encoding for Shanghai+ DeriveSha.
type TransactionsV2 Transactions

func (s TransactionsV2) Len() int { return len(s) }
func (s TransactionsV2) EncodeIndex(i int, w *bytes.Buffer) {
	b, _ := s[i].MarshalV2()
	w.Write(b)
}

type Transaction struct {
	inner TxData
	time  time.Time

	// caches
	hash atomic.Value
	size atomic.Value
	from atomic.Value
}

func NewTx(inner TxData) *Transaction {
	tx := new(Transaction)
	tx.setDecoded(inner.copy(), 0)
	return tx
}

// convertProtoToAddress converts an optional H160 proto field to *types.Address.
func convertProtoToAddress(h160 *types_pb.H160) *types.Address {
	if h160 == nil {
		return nil
	}
	// SSZ deserialization always creates non-nil H160 (fixed-size field).
	// Treat an all-zero H160 as nil to preserve contract creation semantics
	// (To=nil means contract creation, To=0x00...00 is a valid address).
	if h160.Lo == 0 && (h160.Hi == nil || (h160.Hi.Hi == 0 && h160.Hi.Lo == 0)) {
		return nil
	}
	addr := utils.ConvertH160ToPAddress(h160)
	return addr
}

// convertProtoVRS extracts optional V, R, S signature values from proto.
func convertProtoVRS(pbTx *types_pb.Transaction) (v, r, s *uint256.Int) {
	if pbTx.V != nil {
		v = utils.ConvertH256ToUint256Int(pbTx.V)
	}
	if pbTx.R != nil {
		r = utils.ConvertH256ToUint256Int(pbTx.R)
	}
	if pbTx.S != nil {
		s = utils.ConvertH256ToUint256Int(pbTx.S)
	}
	return v, r, s
}

func convertUint256IntToH256IfSet(i *uint256.Int) *types_pb.H256 {
	if i == nil {
		return nil
	}
	return utils.ConvertUint256IntToH256(i)
}

func convertProtoAccessList(pbAccessList []*types_pb.AccessTuple) AccessList {
	if len(pbAccessList) == 0 {
		return nil
	}
	accessList := make(AccessList, len(pbAccessList))
	for i, tuple := range pbAccessList {
		if tuple == nil {
			continue
		}
		var addr types.Address
		if converted := utils.ConvertH160ToPAddress(tuple.Address); converted != nil {
			addr = *converted
		}
		accessList[i] = AccessTuple{
			Address:     addr,
			StorageKeys: utils.H256sToHashes(tuple.StorageKeys),
		}
	}
	return accessList
}

func convertAccessListToProto(accessList AccessList) []*types_pb.AccessTuple {
	if len(accessList) == 0 {
		return nil
	}
	pbAccessList := make([]*types_pb.AccessTuple, 0, len(accessList))
	for _, tuple := range accessList {
		pbAccessList = append(pbAccessList, &types_pb.AccessTuple{
			Address:     utils.ConvertAddressToH160(tuple.Address),
			StorageKeys: utils.ConvertHashesToH256(tuple.StorageKeys),
		})
	}
	return pbAccessList
}

func txDataFromProtoMessage(message proto.Message) (TxData, error) {
	pbTx, ok := message.(*types_pb.Transaction)
	if !ok {
		return nil, fmt.Errorf("invalid proto message type for transaction")
	}

	v, r, s := convertProtoVRS(pbTx)
	var inner TxData

	switch pbTx.Type {
	case LegacyTxType:
		inner = &LegacyTx{
			Nonce:    pbTx.Nonce,
			Gas:      pbTx.Gas,
			GasPrice: utils.ConvertH256ToUint256Int(pbTx.GasPrice),
			Value:    utils.ConvertH256ToUint256Int(pbTx.Value),
			Data:     pbTx.Data,
			To:       convertProtoToAddress(pbTx.To),
			From:     convertProtoToAddress(pbTx.From),
			Sign:     pbTx.Sign,
			V:        v, R: r, S: s,
		}

	case AccessListTxType:
		inner = &AccessListTx{
			ChainID:    uint256.NewInt(pbTx.ChainID),
			Nonce:      pbTx.Nonce,
			Gas:        pbTx.Gas,
			GasPrice:   utils.ConvertH256ToUint256Int(pbTx.GasPrice),
			Value:      utils.ConvertH256ToUint256Int(pbTx.Value),
			Data:       pbTx.Data,
			To:         convertProtoToAddress(pbTx.To),
			From:       convertProtoToAddress(pbTx.From),
			AccessList: convertProtoAccessList(pbTx.AccessList),
			Sign:       pbTx.Sign,
			V:          v, R: r, S: s,
		}

	case DynamicFeeTxType:
		inner = &DynamicFeeTx{
			ChainID:    uint256.NewInt(pbTx.ChainID),
			Nonce:      pbTx.Nonce,
			Gas:        pbTx.Gas,
			GasFeeCap:  utils.ConvertH256ToUint256Int(pbTx.FeePerGas),
			GasTipCap:  utils.ConvertH256ToUint256Int(pbTx.PriorityFeePerGas),
			Value:      utils.ConvertH256ToUint256Int(pbTx.Value),
			Data:       pbTx.Data,
			To:         convertProtoToAddress(pbTx.To),
			From:       convertProtoToAddress(pbTx.From),
			AccessList: convertProtoAccessList(pbTx.AccessList),
			Sign:       pbTx.Sign,
			V:          v, R: r, S: s,
		}

	case BlobTxType:
		var to types.Address
		if addr := convertProtoToAddress(pbTx.To); addr != nil {
			to = *addr
		}
		inner = &BlobTx{
			ChainID:    uint256.NewInt(pbTx.ChainID),
			Nonce:      pbTx.Nonce,
			Gas:        pbTx.Gas,
			GasFeeCap:  utils.ConvertH256ToUint256Int(pbTx.FeePerGas),
			GasTipCap:  utils.ConvertH256ToUint256Int(pbTx.PriorityFeePerGas),
			Value:      utils.ConvertH256ToUint256Int(pbTx.Value),
			Data:       pbTx.Data,
			To:         to,
			AccessList: convertProtoAccessList(pbTx.AccessList),
			BlobFeeCap: utils.ConvertH256ToUint256Int(pbTx.BlobFeeCap),
			BlobHashes: utils.H256sToHashes(pbTx.BlobHashes),
			V:          v, R: r, S: s,
		}

	case PostQuantumTxType:
		inner = &PostQuantumTx{
			ChainID:     uint256.NewInt(pbTx.ChainID),
			Nonce:       pbTx.Nonce,
			Gas:         pbTx.Gas,
			GasFeeCap:   utils.ConvertH256ToUint256Int(pbTx.FeePerGas),
			GasTipCap:   utils.ConvertH256ToUint256Int(pbTx.PriorityFeePerGas),
			Value:       utils.ConvertH256ToUint256Int(pbTx.Value),
			Data:        pbTx.Data,
			To:          convertProtoToAddress(pbTx.To),
			AccessList:  convertProtoAccessList(pbTx.AccessList),
			SigAlgo:     uint8(pbTx.PqSigAlgo),
			PubKeyMode:  uint8(pbTx.PqPubKeyMode),
			PubKeyData:  pbTx.PqPubKeyData,
			PQSignature: pbTx.PqSignature,
			V:           v, R: r, S: s,
		}

	default:
		return nil, fmt.Errorf("unsupported transaction type: %d", pbTx.Type)
	}

	// Hash verification is disabled for P2P compatibility.
	_ = utils.ConvertH256ToHash(pbTx.Hash)
	_ = inner.hash()

	return inner, nil
}

func FromProtoMessage(message proto.Message) (*Transaction, error) {
	inner, err := txDataFromProtoMessage(message)
	if err != nil {
		return nil, err
	}
	return NewTx(inner), nil
}

// toProtoFields populates a types_pb.Transaction with all fields except Hash.
func (tx *Transaction) toProtoFields() *types_pb.Transaction {
	var pbTx types_pb.Transaction
	pbTx.Type = uint64(tx.inner.txType())

	switch t := tx.inner.(type) {
	case *LegacyTx:
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = convertUint256IntToH256IfSet(tx.GasPrice())
		pbTx.Value = convertUint256IntToH256IfSet(tx.Value())
		pbTx.Data = tx.Data()
		if from := tx.From(); from != nil {
			pbTx.From = utils.ConvertAddressToH160(*from)
		}
		pbTx.Sign = t.Sign

	case *AccessListTx:
		if t.ChainID != nil {
			pbTx.ChainID = t.ChainID.Uint64()
		}
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = convertUint256IntToH256IfSet(tx.GasPrice())
		pbTx.Value = convertUint256IntToH256IfSet(tx.Value())
		pbTx.Data = tx.Data()
		if from := tx.From(); from != nil {
			pbTx.From = utils.ConvertAddressToH160(*from)
		}
		pbTx.Sign = t.Sign

	case *DynamicFeeTx:
		if t.ChainID != nil {
			pbTx.ChainID = t.ChainID.Uint64()
		}
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = convertUint256IntToH256IfSet(tx.GasPrice())
		pbTx.Value = convertUint256IntToH256IfSet(tx.Value())
		pbTx.Data = tx.Data()
		if from := tx.From(); from != nil {
			pbTx.From = utils.ConvertAddressToH160(*from)
		}
		pbTx.Sign = t.Sign
		pbTx.FeePerGas = convertUint256IntToH256IfSet(t.GasFeeCap)
		pbTx.PriorityFeePerGas = convertUint256IntToH256IfSet(t.GasTipCap)

	case *PostQuantumTx:
		if t.ChainID != nil {
			pbTx.ChainID = t.ChainID.Uint64()
		}
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = convertUint256IntToH256IfSet(tx.GasPrice())
		pbTx.Value = convertUint256IntToH256IfSet(tx.Value())
		pbTx.Data = tx.Data()
		pbTx.FeePerGas = convertUint256IntToH256IfSet(t.GasFeeCap)
		pbTx.PriorityFeePerGas = convertUint256IntToH256IfSet(t.GasTipCap)
		pbTx.PqSigAlgo = uint32(t.SigAlgo)
		pbTx.PqPubKeyMode = uint32(t.PubKeyMode)
		pbTx.PqPubKeyData = t.PubKeyData
		pbTx.PqSignature = t.PQSignature

	case *BlobTx:
		if t.ChainID != nil {
			pbTx.ChainID = t.ChainID.Uint64()
		}
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = convertUint256IntToH256IfSet(tx.GasPrice())
		pbTx.Value = convertUint256IntToH256IfSet(tx.Value())
		pbTx.Data = tx.Data()
		pbTx.FeePerGas = convertUint256IntToH256IfSet(t.GasFeeCap)
		pbTx.PriorityFeePerGas = convertUint256IntToH256IfSet(t.GasTipCap)
		pbTx.BlobFeeCap = convertUint256IntToH256IfSet(t.BlobFeeCap)
		pbTx.BlobHashes = utils.ConvertHashesToH256(t.BlobHashes)
	}

	if tx.To() != nil {
		pbTx.To = utils.ConvertAddressToH160(*tx.To())
	}
	if accessList := tx.AccessList(); len(accessList) > 0 {
		pbTx.AccessList = convertAccessListToProto(accessList)
	}

	// NOTE: V, R, S are intentionally NOT set here. The legacy mainnet chain
	// data was encoded without these fields (signatures are in the Sign field).
	// Adding V/R/S would change proto.Marshal output and break DeriveSha hashes.
	// They are added in ToProtoMessage() for RPC/storage use only.

	return &pbTx
}

func (tx *Transaction) ToProtoMessage() proto.Message {
	pbTx := tx.toProtoFields()
	pbTx.Hash = utils.ConvertHashToH256(tx.Hash())

	// Include V, R, S for RPC consumers (not used in DeriveSha hash).
	v, r, s := tx.RawSignatureValues()
	if v != nil {
		pbTx.V = convertUint256IntToH256IfSet(v)
	}
	if r != nil {
		pbTx.R = convertUint256IntToH256IfSet(r)
	}
	if s != nil {
		pbTx.S = convertUint256IntToH256IfSet(s)
	}

	return pbTx
}

func (tx *Transaction) setDecoded(inner TxData, size int) {
	tx.inner = inner
	tx.time = time.Now()
}

func (tx *Transaction) RawSignatureValues() (v, r, s *uint256.Int) {
	return tx.inner.rawSignatureValues()
}

// WithSignatureValues sets the signature values using the transaction's existing chainID.
func (tx *Transaction) WithSignatureValues(v, r, s *uint256.Int) (*Transaction, error) {
	tx.inner.setSignatureValues(tx.inner.chainID(), v, r, s)
	return tx, nil
}

// Marshal encodes the transaction to protobuf bytes. The encoding MUST match
// the original mainnet format exactly because DeriveSha (transaction root hash)
// is computed from these bytes. Any change to the encoding breaks chain
// compatibility with existing block data.
func (tx Transaction) Marshal() ([]byte, error) {
	var pbTx types_pb.Transaction
	pbTx.Type = uint64(tx.inner.txType())

	switch t := tx.inner.(type) {
	case *AccessListTx:
		if t.ChainID != nil {
			pbTx.ChainID = t.ChainID.Uint64()
		}
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = utils.ConvertUint256IntToH256(tx.GasPrice())
		pbTx.Value = utils.ConvertUint256IntToH256(tx.Value())
		pbTx.Data = tx.Data()
		if from := tx.From(); from != nil { pbTx.From = utils.ConvertAddressToH160(*from) }
		pbTx.Sign = t.Sign
	case *LegacyTx:
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = utils.ConvertUint256IntToH256(tx.GasPrice())
		pbTx.Value = utils.ConvertUint256IntToH256(tx.Value())
		pbTx.Data = tx.Data()
		if from := tx.From(); from != nil { pbTx.From = utils.ConvertAddressToH160(*from) }
		pbTx.Sign = t.Sign
	case *DynamicFeeTx:
		if t.ChainID != nil {
			pbTx.ChainID = t.ChainID.Uint64()
		}
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = utils.ConvertUint256IntToH256(tx.GasPrice())
		pbTx.Value = utils.ConvertUint256IntToH256(tx.Value())
		pbTx.Data = tx.Data()
		if from := tx.From(); from != nil { pbTx.From = utils.ConvertAddressToH160(*from) }
		pbTx.Sign = t.Sign
		pbTx.FeePerGas = utils.ConvertUint256IntToH256(t.GasFeeCap)
		pbTx.PriorityFeePerGas = utils.ConvertUint256IntToH256(t.GasTipCap)
	case *BlobTx:
		if t.ChainID != nil {
			pbTx.ChainID = t.ChainID.Uint64()
		}
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = utils.ConvertUint256IntToH256(tx.GasPrice())
		pbTx.Value = utils.ConvertUint256IntToH256(tx.Value())
		pbTx.Data = tx.Data()
		if from := tx.From(); from != nil {
			pbTx.From = utils.ConvertAddressToH160(*from)
		}
		pbTx.FeePerGas = utils.ConvertUint256IntToH256(t.GasFeeCap)
		pbTx.PriorityFeePerGas = utils.ConvertUint256IntToH256(t.GasTipCap)
		pbTx.BlobFeeCap = convertUint256IntToH256IfSet(t.BlobFeeCap)
		pbTx.BlobHashes = utils.ConvertHashesToH256(t.BlobHashes)
	case *PostQuantumTx:
		if t.ChainID != nil {
			pbTx.ChainID = t.ChainID.Uint64()
		}
		pbTx.Nonce = tx.Nonce()
		pbTx.Gas = tx.Gas()
		pbTx.GasPrice = utils.ConvertUint256IntToH256(tx.GasPrice())
		pbTx.Value = utils.ConvertUint256IntToH256(tx.Value())
		pbTx.Data = tx.Data()
		pbTx.FeePerGas = utils.ConvertUint256IntToH256(t.GasFeeCap)
		pbTx.PriorityFeePerGas = utils.ConvertUint256IntToH256(t.GasTipCap)
		pbTx.PqSigAlgo = uint32(t.SigAlgo)
		pbTx.PqPubKeyMode = uint32(t.PubKeyMode)
		pbTx.PqPubKeyData = t.PubKeyData
		pbTx.PqSignature = t.PQSignature
	}
	if tx.To() != nil {
		pbTx.To = utils.ConvertAddressToH160(*tx.To())
	}
	v, r, s := tx.RawSignatureValues()
	if v != nil {
		pbTx.V = utils.ConvertUint256IntToH256(v)
	}
	if r != nil {
		pbTx.R = utils.ConvertUint256IntToH256(r)
	}
	if s != nil {
		pbTx.S = utils.ConvertUint256IntToH256(s)
	}
	return proto.Marshal(&pbTx)
}

// MarshalV2 encodes the transaction using the new format (Shanghai+).
// Includes AccessList, uses nil-safe conversions, and excludes V/R/S from
// the hash-relevant encoding (they're in the Sign field).
func (tx Transaction) MarshalV2() ([]byte, error) {
	pbTx := tx.toProtoFields()
	return proto.Marshal(pbTx)
}

func (tx *Transaction) MarshalTo(data []byte) (n int, err error) {
	b, err := tx.Marshal()
	if err != nil {
		return 0, err
	}
	copy(data, b)
	return len(b), nil
}

func (tx *Transaction) Unmarshal(data []byte) error {
	var pbTx types_pb.Transaction
	if err := proto.Unmarshal(data, &pbTx); err != nil {
		return err
	}
	inner, err := txDataFromProtoMessage(&pbTx)
	if err != nil {
		return err
	}
	tx.setDecoded(inner, 0)
	return nil
}

func (tx *Transaction) Type() uint8             { return tx.inner.txType() }
func (tx *Transaction) ChainId() *uint256.Int   { return tx.inner.chainID() }
func (tx *Transaction) Data() []byte            { return tx.inner.data() }
func (tx *Transaction) Gas() uint64             { return tx.inner.gas() }
func (tx *Transaction) GasPrice() *uint256.Int  { return tx.inner.gasPrice() }
func (tx *Transaction) GasTipCap() *uint256.Int { return tx.inner.gasTipCap() }
func (tx *Transaction) GasFeeCap() *uint256.Int { return tx.inner.gasFeeCap() }
func (tx *Transaction) Value() *uint256.Int     { return tx.inner.value() }
func (tx *Transaction) Nonce() uint64           { return tx.inner.nonce() }
func (tx *Transaction) To() *types.Address      { return copyAddressPtr(tx.inner.to()) }
func (tx *Transaction) From() *types.Address    { return tx.inner.from() }
func (tx *Transaction) Sign() []byte            { return tx.inner.sign() }

func (tx *Transaction) AccessList() AccessList      { return tx.inner.accessList() }
func (tx *Transaction) AuthList() AuthorizationList { return tx.inner.authList() }

func (tx *Transaction) SetFrom(addr types.Address) {
	switch t := tx.inner.(type) {
	case *AccessListTx:
		t.From = &addr
	case *LegacyTx:
		t.From = &addr
	case *DynamicFeeTx:
		t.From = &addr
	case *PostQuantumTx:
		// PostQuantumTx address is derived from PQ public key
	}
}

func (tx *Transaction) SetNonce(nonce uint64) {
	switch t := tx.inner.(type) {
	case *AccessListTx:
		t.Nonce = nonce
	case *LegacyTx:
		t.Nonce = nonce
	case *DynamicFeeTx:
		t.Nonce = nonce
	case *PostQuantumTx:
		t.Nonce = nonce
	}
}

func (tx *Transaction) Cost() *uint256.Int {
	total := new(uint256.Int).Mul(tx.inner.gasPrice(), uint256.NewInt(tx.inner.gas()))
	return total.Add(total, tx.Value())
}

func (tx *Transaction) Hash() types.Hash {
	if h := tx.hash.Load(); h != nil {
		return h.(types.Hash)
	}
	h := tx.inner.hash()
	tx.hash.Store(h)
	return h
}

// BlobHashes returns the blob hashes for EIP-4844 blob transactions.
func (tx *Transaction) BlobHashes() []types.Hash {
	if blobTx, ok := tx.inner.(*BlobTx); ok {
		return blobTx.BlobHashes
	}
	return nil
}

// BlobFeeCap returns the blob fee cap for EIP-4844 blob transactions.
func (tx *Transaction) BlobFeeCap() *uint256.Int {
	if blobTx, ok := tx.inner.(*BlobTx); ok {
		return blobTx.BlobFeeCap
	}
	return nil
}

// BlobGas returns the blob gas used by this transaction.
func (tx *Transaction) BlobGas() uint64 {
	if blobTx, ok := tx.inner.(*BlobTx); ok {
		return blobTx.BlobGas()
	}
	return 0
}

func (tx *Transaction) GasFeeCapCmp(other *Transaction) int {
	return tx.inner.gasFeeCap().Cmp(other.inner.gasFeeCap())
}

func (tx *Transaction) GasTipCapCmp(other *Transaction) int {
	return tx.inner.gasTipCap().Cmp(other.inner.gasTipCap())
}

func (tx *Transaction) GasFeeCapIntCmp(other *uint256.Int) int {
	return tx.inner.gasFeeCap().Cmp(other)
}

func (tx *Transaction) GasTipCapIntCmp(other *uint256.Int) int {
	return tx.inner.gasTipCap().Cmp(other)
}

// EffectiveGasTip returns the effective miner gasTipCap for the given base fee.
// Returns both the value and ErrGasFeeCapTooLow if the fee cap is below base fee.
func (tx *Transaction) EffectiveGasTip(baseFee *uint256.Int) (*uint256.Int, error) {
	if baseFee == nil {
		return tx.GasTipCap(), nil
	}
	var err error
	gasFeeCap := tx.GasFeeCap()
	if gasFeeCap.Cmp(baseFee) == -1 {
		err = ErrGasFeeCapTooLow
	}
	return uint256Min(tx.GasTipCap(), new(uint256.Int).Sub(gasFeeCap, baseFee)), err
}

func (tx *Transaction) EffectiveGasTipValue(baseFee *uint256.Int) *uint256.Int {
	effectiveTip, _ := tx.EffectiveGasTip(baseFee)
	return effectiveTip
}

func (tx *Transaction) EffectiveGasTipCmp(other *Transaction, baseFee *uint256.Int) int {
	if baseFee == nil {
		return tx.GasTipCapCmp(other)
	}
	return tx.EffectiveGasTipValue(baseFee).Cmp(other.EffectiveGasTipValue(baseFee))
}

func (tx *Transaction) EffectiveGasTipIntCmp(other *uint256.Int, baseFee *uint256.Int) int {
	if baseFee == nil {
		return tx.GasTipCapIntCmp(other)
	}
	return tx.EffectiveGasTipValue(baseFee).Cmp(other)
}

func uint256Min(x, y *uint256.Int) *uint256.Int {
	if x.Cmp(y) == 1 {
		return y
	}
	return x
}

func isProtectedV(V *big.Int) bool {
	if V.BitLen() <= 8 {
		v := V.Uint64()
		return v != 27 && v != 28 && v != 1 && v != 0
	}
	return true
}

// Protected reports whether the transaction is replay-protected.
func (tx *Transaction) Protected() bool {
	switch inner := tx.inner.(type) {
	case *LegacyTx:
		v := inner.V.ToBig()
		return v != nil && isProtectedV(v)
	default:
		return true
	}
}

// WithSignature returns a new transaction with the given signature.
// The signature must be in [R || S || V] format where V is 0 or 1.
func (tx *Transaction) WithSignature(signer Signer, sig []byte) (*Transaction, error) {
	r, s, v, err := signer.SignatureValues(tx, sig)
	if err != nil {
		return nil, err
	}
	cpy := tx.inner.copy()

	var chainID *uint256.Int
	if signer.ChainID() != nil {
		chainID, _ = uint256.FromBig(signer.ChainID())
	}

	v1, _ := uint256.FromBig(v)
	r1, _ := uint256.FromBig(r)
	s1, _ := uint256.FromBig(s)
	cpy.setSignatureValues(chainID, v1, r1, s1)
	return &Transaction{inner: cpy, time: tx.time}, nil
}

func copyAddressPtr(a *types.Address) *types.Address {
	if a == nil {
		return nil
	}
	cpy := *a
	return &cpy
}
