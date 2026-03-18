package transaction

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
)

type legacyTxRLP struct {
	Nonce    uint64
	GasPrice *uint256.Int
	Gas      uint64
	To       *types.Address `rlp:"nil"`
	Value    *uint256.Int
	Data     []byte
	V        *uint256.Int
	R        *uint256.Int
	S        *uint256.Int
}

type accessListTxRLP struct {
	ChainID    *uint256.Int
	Nonce      uint64
	GasPrice   *uint256.Int
	Gas        uint64
	To         *types.Address `rlp:"nil"`
	Value      *uint256.Int
	Data       []byte
	AccessList AccessList
	V          *uint256.Int
	R          *uint256.Int
	S          *uint256.Int
}

type dynamicFeeTxRLP struct {
	ChainID    *uint256.Int
	Nonce      uint64
	GasTipCap  *uint256.Int
	GasFeeCap  *uint256.Int
	Gas        uint64
	To         *types.Address `rlp:"nil"`
	Value      *uint256.Int
	Data       []byte
	AccessList AccessList
	V          *uint256.Int
	R          *uint256.Int
	S          *uint256.Int
}

type blobTxRLP struct {
	ChainID    *uint256.Int
	Nonce      uint64
	GasTipCap  *uint256.Int
	GasFeeCap  *uint256.Int
	Gas        uint64
	To         types.Address
	Value      *uint256.Int
	Data       []byte
	AccessList AccessList
	BlobFeeCap *uint256.Int
	BlobHashes []types.Hash
	V          *uint256.Int
	R          *uint256.Int
	S          *uint256.Int
}

type setCodeTxRLP struct {
	ChainID    *uint256.Int
	Nonce      uint64
	GasTipCap  *uint256.Int
	GasFeeCap  *uint256.Int
	Gas        uint64
	To         *types.Address `rlp:"nil"`
	Value      *uint256.Int
	Data       []byte
	AccessList AccessList
	AuthList   AuthorizationList
	V          *uint256.Int
	R          *uint256.Int
	S          *uint256.Int
}

// DecodeEthereumTransaction decodes a standard Ethereum raw transaction
// (legacy RLP or EIP-2718 typed envelope) into the local Transaction type.
func DecodeEthereumTransaction(data []byte) (*Transaction, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty transaction payload")
	}
	if data[0] >= 0xc0 {
		var dec legacyTxRLP
		if err := rlp.DecodeBytes(data, &dec); err != nil {
			return nil, err
		}
		return NewTx(&LegacyTx{
			Nonce:    dec.Nonce,
			GasPrice: dec.GasPrice,
			Gas:      dec.Gas,
			To:       dec.To,
			Value:    dec.Value,
			Data:     dec.Data,
			V:        dec.V,
			R:        dec.R,
			S:        dec.S,
		}), nil
	}

	payload := data[1:]
	switch data[0] {
	case AccessListTxType:
		var dec accessListTxRLP
		if err := rlp.DecodeBytes(payload, &dec); err != nil {
			return nil, err
		}
		return NewTx(&AccessListTx{
			ChainID:    dec.ChainID,
			Nonce:      dec.Nonce,
			GasPrice:   dec.GasPrice,
			Gas:        dec.Gas,
			To:         dec.To,
			Value:      dec.Value,
			Data:       dec.Data,
			AccessList: dec.AccessList,
			V:          dec.V,
			R:          dec.R,
			S:          dec.S,
		}), nil
	case DynamicFeeTxType:
		var dec dynamicFeeTxRLP
		if err := rlp.DecodeBytes(payload, &dec); err != nil {
			return nil, err
		}
		return NewTx(&DynamicFeeTx{
			ChainID:    dec.ChainID,
			Nonce:      dec.Nonce,
			GasTipCap:  dec.GasTipCap,
			GasFeeCap:  dec.GasFeeCap,
			Gas:        dec.Gas,
			To:         dec.To,
			Value:      dec.Value,
			Data:       dec.Data,
			AccessList: dec.AccessList,
			V:          dec.V,
			R:          dec.R,
			S:          dec.S,
		}), nil
	case BlobTxType:
		var dec blobTxRLP
		if err := rlp.DecodeBytes(payload, &dec); err != nil {
			return nil, err
		}
		return NewTx(&BlobTx{
			ChainID:    dec.ChainID,
			Nonce:      dec.Nonce,
			GasTipCap:  dec.GasTipCap,
			GasFeeCap:  dec.GasFeeCap,
			Gas:        dec.Gas,
			To:         dec.To,
			Value:      dec.Value,
			Data:       dec.Data,
			AccessList: dec.AccessList,
			BlobFeeCap: dec.BlobFeeCap,
			BlobHashes: dec.BlobHashes,
			V:          dec.V,
			R:          dec.R,
			S:          dec.S,
		}), nil
	case SetCodeTxType:
		var dec setCodeTxRLP
		if err := rlp.DecodeBytes(payload, &dec); err != nil {
			return nil, err
		}
		return NewTx(&SetCodeTx{
			ChainID:    dec.ChainID,
			Nonce:      dec.Nonce,
			GasTipCap:  dec.GasTipCap,
			GasFeeCap:  dec.GasFeeCap,
			Gas:        dec.Gas,
			To:         dec.To,
			Value:      dec.Value,
			Data:       dec.Data,
			AccessList: dec.AccessList,
			AuthList:   dec.AuthList,
			V:          dec.V,
			R:          dec.R,
			S:          dec.S,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported ethereum tx type: 0x%02x", data[0])
	}
}

// EncodeEthereumTransaction encodes a transaction into the standard Ethereum
// raw transaction format used by Engine API payloads.
func EncodeEthereumTransaction(tx *Transaction) ([]byte, error) {
	if tx == nil {
		return nil, fmt.Errorf("nil transaction")
	}
	switch inner := tx.inner.(type) {
	case *LegacyTx:
		return rlp.EncodeToBytes(&legacyTxRLP{
			Nonce:    inner.Nonce,
			GasPrice: nonNilUint256(inner.GasPrice),
			Gas:      inner.Gas,
			To:       inner.To,
			Value:    nonNilUint256(inner.Value),
			Data:     inner.Data,
			V:        nonNilUint256(inner.V),
			R:        nonNilUint256(inner.R),
			S:        nonNilUint256(inner.S),
		})
	case *AccessListTx:
		return encodeTypedEthereumTransaction(AccessListTxType, &accessListTxRLP{
			ChainID:    nonNilUint256(inner.ChainID),
			Nonce:      inner.Nonce,
			GasPrice:   nonNilUint256(inner.GasPrice),
			Gas:        inner.Gas,
			To:         inner.To,
			Value:      nonNilUint256(inner.Value),
			Data:       inner.Data,
			AccessList: inner.AccessList,
			V:          nonNilUint256(inner.V),
			R:          nonNilUint256(inner.R),
			S:          nonNilUint256(inner.S),
		})
	case *DynamicFeeTx:
		return encodeTypedEthereumTransaction(DynamicFeeTxType, &dynamicFeeTxRLP{
			ChainID:    nonNilUint256(inner.ChainID),
			Nonce:      inner.Nonce,
			GasTipCap:  nonNilUint256(inner.GasTipCap),
			GasFeeCap:  nonNilUint256(inner.GasFeeCap),
			Gas:        inner.Gas,
			To:         inner.To,
			Value:      nonNilUint256(inner.Value),
			Data:       inner.Data,
			AccessList: inner.AccessList,
			V:          nonNilUint256(inner.V),
			R:          nonNilUint256(inner.R),
			S:          nonNilUint256(inner.S),
		})
	case *BlobTx:
		return encodeBlobEthereumTransaction(inner)
	case *SetCodeTx:
		return encodeTypedEthereumTransaction(SetCodeTxType, &setCodeTxRLP{
			ChainID:    nonNilUint256(inner.ChainID),
			Nonce:      inner.Nonce,
			GasTipCap:  nonNilUint256(inner.GasTipCap),
			GasFeeCap:  nonNilUint256(inner.GasFeeCap),
			Gas:        inner.Gas,
			To:         inner.To,
			Value:      nonNilUint256(inner.Value),
			Data:       inner.Data,
			AccessList: inner.AccessList,
			AuthList:   inner.AuthList,
			V:          nonNilUint256(inner.V),
			R:          nonNilUint256(inner.R),
			S:          nonNilUint256(inner.S),
		})
	default:
		return nil, fmt.Errorf("unsupported ethereum tx type: 0x%02x", tx.Type())
	}
}

func encodeBlobEthereumTransaction(tx *BlobTx) ([]byte, error) {
	if tx == nil {
		return nil, fmt.Errorf("nil blob transaction")
	}
	return encodeTypedEthereumTransaction(BlobTxType, &blobTxRLP{
		ChainID:    nonNilUint256(tx.ChainID),
		Nonce:      tx.Nonce,
		GasTipCap:  nonNilUint256(tx.GasTipCap),
		GasFeeCap:  nonNilUint256(tx.GasFeeCap),
		Gas:        tx.Gas,
		To:         tx.To,
		Value:      nonNilUint256(tx.Value),
		Data:       tx.Data,
		AccessList: tx.AccessList,
		BlobFeeCap: nonNilUint256(tx.BlobFeeCap),
		BlobHashes: tx.BlobHashes,
		V:          nonNilUint256(tx.V),
		R:          nonNilUint256(tx.R),
		S:          nonNilUint256(tx.S),
	})
}

func encodeTypedEthereumTransaction(txType byte, payload interface{}) ([]byte, error) {
	enc, err := rlp.EncodeToBytes(payload)
	if err != nil {
		return nil, err
	}
	return append([]byte{txType}, enc...), nil
}

func nonNilUint256(v *uint256.Int) *uint256.Int {
	if v != nil {
		return v
	}
	return new(uint256.Int)
}
