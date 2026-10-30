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

// N42 0x05 v1: full-key, padded Falcon-512; standard EIP-2718 framing.
type pqTxRLP struct {
	ChainID     *uint256.Int
	Nonce       uint64
	GasTipCap   *uint256.Int
	GasFeeCap   *uint256.Int
	Gas         uint64
	To          *types.Address `rlp:"nil"`
	Value       *uint256.Int
	Data        []byte
	AccessList  AccessList
	SigAlgo     uint8
	PubKeyMode  uint8
	PubKeyData  []byte
	PQSignature []byte
}

func validatePQWire(p *PostQuantumTx) error {
	if p == nil || p.ChainID == nil || p.ChainID.IsZero() || p.ChainID.BitLen() > 64 ||
		p.GasTipCap == nil || p.GasFeeCap == nil || p.GasTipCap.BitLen() > 128 || p.GasFeeCap.BitLen() > 128 ||
		p.GasTipCap.Cmp(p.GasFeeCap) > 0 || p.To == nil || p.Value == nil ||
		p.SigAlgo != PQAlgoFalcon512 || p.PubKeyMode != 0 || len(p.PubKeyData) != 897 || len(p.PQSignature) != 666 {
		return fmt.Errorf("invalid N42 0x05 full-key Falcon-512 transaction")
	}
	return nil
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

// blobTxNetworkWrapperRLP is the EIP-4844 (version 0) network form:
// [tx_payload_body, blobs, commitments, blob_proofs].
type blobTxNetworkWrapperRLP struct {
	Tx          blobTxRLP
	Blobs       []Blob
	Commitments []Commitment
	Proofs      []Proof
}

// blobTxNetworkWrapperV1RLP is the EIP-7594 (version 1) network form:
// [tx_payload_body, wrapper_version, blobs, commitments, cell_proofs].
type blobTxNetworkWrapperV1RLP struct {
	Tx          blobTxRLP
	Version     byte
	Blobs       []Blob
	Commitments []Commitment
	Proofs      []Proof
}

// decodeBlobTxPayload decodes a blob transaction payload (without the type
// byte) in either the canonical form [chainID, nonce, ...] or one of the
// network forms that carry the sidecar. As in geth, the forms are told apart
// structurally: the network forms start with the tx body as a nested list,
// and the versioned form has a version byte, not the blob list, second.
func decodeBlobTxPayload(payload []byte) (blobTxRLP, *BlobTxSidecar, error) {
	var dec blobTxRLP
	content, _, err := rlp.SplitList(payload)
	if err != nil {
		return dec, nil, err
	}
	firstKind, _, rest, err := rlp.Split(content)
	if err != nil {
		return dec, nil, err
	}
	if firstKind != rlp.List {
		err := rlp.DecodeBytes(payload, &dec)
		return dec, nil, err
	}
	secondKind, _, _, err := rlp.Split(rest)
	if err != nil {
		return dec, nil, err
	}
	if secondKind == rlp.List {
		var wrapper blobTxNetworkWrapperRLP
		if err := rlp.DecodeBytes(payload, &wrapper); err != nil {
			return dec, nil, err
		}
		return wrapper.Tx, &BlobTxSidecar{
			Version:     BlobSidecarVersion0,
			Blobs:       wrapper.Blobs,
			Commitments: wrapper.Commitments,
			Proofs:      wrapper.Proofs,
		}, nil
	}
	var wrapper blobTxNetworkWrapperV1RLP
	if err := rlp.DecodeBytes(payload, &wrapper); err != nil {
		return dec, nil, err
	}
	if wrapper.Version != BlobSidecarVersion1 {
		return dec, nil, fmt.Errorf("unsupported blob tx sidecar version %d", wrapper.Version)
	}
	return wrapper.Tx, &BlobTxSidecar{
		Version:     BlobSidecarVersion1,
		Blobs:       wrapper.Blobs,
		Commitments: wrapper.Commitments,
		Proofs:      wrapper.Proofs,
	}, nil
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
// DecodeEthereumTransaction decodes a canonical Ethereum wire encoding and
// keeps it: data IS that encoding -- being it is what makes it decodable -- so
// caching it here spares the transactions root re-deriving the same bytes for
// every leaf of every imported block. The copy is 110-odd bytes against a full
// RLP encode, and it is necessary because the caller's buffer may be reused.
func DecodeEthereumTransaction(data []byte) (*Transaction, error) {
	tx, err := decodeEthereumTransaction(data)
	if err != nil {
		return nil, err
	}
	// A blob transaction in its network form (with sidecar) is NOT its
	// canonical encoding: leave the cache empty so EthEncoded derives the
	// bare transaction instead of returning the wrapper.
	if tx.BlobTxSidecar() == nil {
		tx.cacheEncoded(append([]byte(nil), data...))
	}
	return tx, nil
}

func decodeEthereumTransaction(data []byte) (*Transaction, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty transaction payload")
	}
	if data[0] >= 0xc0 {
		var dec legacyTxRLP
		if err := rlp.DecodeBytes(data, &dec); err != nil {
			return nil, err
		}
		return NewTxOwned(&LegacyTx{
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
	case PostQuantumTxType:
		var dec pqTxRLP
		if err := rlp.DecodeBytes(payload, &dec); err != nil {
			return nil, err
		}
		tx := &PostQuantumTx{ChainID: dec.ChainID, Nonce: dec.Nonce, GasTipCap: dec.GasTipCap, GasFeeCap: dec.GasFeeCap,
			Gas: dec.Gas, To: dec.To, Value: dec.Value, Data: dec.Data, AccessList: dec.AccessList,
			SigAlgo: dec.SigAlgo, PubKeyMode: dec.PubKeyMode, PubKeyData: dec.PubKeyData, PQSignature: dec.PQSignature}
		if err := validatePQWire(tx); err != nil {
			return nil, err
		}
		return NewTxOwned(tx), nil

	case AccessListTxType:
		var dec accessListTxRLP
		if err := rlp.DecodeBytes(payload, &dec); err != nil {
			return nil, err
		}
		return NewTxOwned(&AccessListTx{
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
		return NewTxOwned(&DynamicFeeTx{
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
		dec, sidecar, err := decodeBlobTxPayload(payload)
		if err != nil {
			return nil, err
		}
		return NewTxOwned(&BlobTx{
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
			Sidecar:    sidecar,
		}), nil
	case SetCodeTxType:
		var dec setCodeTxRLP
		if err := rlp.DecodeBytes(payload, &dec); err != nil {
			return nil, err
		}
		return NewTxOwned(&SetCodeTx{
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
	case *PostQuantumTx:
		if err := validatePQWire(inner); err != nil {
			return nil, err
		}
		return encodeTypedEthereumTransaction(PostQuantumTxType, &pqTxRLP{
			ChainID: inner.ChainID, Nonce: inner.Nonce, GasTipCap: inner.GasTipCap, GasFeeCap: inner.GasFeeCap,
			Gas: inner.Gas, To: inner.To, Value: inner.Value, Data: inner.Data, AccessList: inner.AccessList,
			SigAlgo: inner.SigAlgo, PubKeyMode: inner.PubKeyMode, PubKeyData: inner.PubKeyData, PQSignature: inner.PQSignature,
		})

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

// EncodeEthereumPooledTransaction encodes a transaction for eth/68+ pool
// propagation. Blob transactions retain their sidecar in the EIP-4844 network
// wrapper; block payload encoding intentionally omits it.
func EncodeEthereumPooledTransaction(tx *Transaction) ([]byte, error) {
	if tx == nil {
		return nil, fmt.Errorf("nil transaction")
	}
	blobTx, ok := tx.inner.(*BlobTx)
	if !ok || blobTx.Sidecar == nil {
		return EncodeEthereumTransaction(tx)
	}
	body := blobTxRLP{
		ChainID:    nonNilUint256(blobTx.ChainID),
		Nonce:      blobTx.Nonce,
		GasTipCap:  nonNilUint256(blobTx.GasTipCap),
		GasFeeCap:  nonNilUint256(blobTx.GasFeeCap),
		Gas:        blobTx.Gas,
		To:         blobTx.To,
		Value:      nonNilUint256(blobTx.Value),
		Data:       blobTx.Data,
		AccessList: blobTx.AccessList,
		BlobFeeCap: nonNilUint256(blobTx.BlobFeeCap),
		BlobHashes: blobTx.BlobHashes,
		V:          nonNilUint256(blobTx.V),
		R:          nonNilUint256(blobTx.R),
		S:          nonNilUint256(blobTx.S),
	}
	sc := blobTx.Sidecar
	var wrapper interface{}
	switch sc.Version {
	case BlobSidecarVersion0:
		wrapper = &blobTxNetworkWrapperRLP{Tx: body, Blobs: sc.Blobs, Commitments: sc.Commitments, Proofs: sc.Proofs}
	case BlobSidecarVersion1:
		wrapper = &blobTxNetworkWrapperV1RLP{Tx: body, Version: sc.Version, Blobs: sc.Blobs, Commitments: sc.Commitments, Proofs: sc.Proofs}
	default:
		return nil, fmt.Errorf("unsupported blob tx sidecar version %d", sc.Version)
	}
	payload, err := rlp.EncodeToBytes(wrapper)
	if err != nil {
		return nil, err
	}
	return append([]byte{BlobTxType}, payload...), nil
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
