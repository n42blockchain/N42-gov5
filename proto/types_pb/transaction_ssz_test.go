package types_pb_test

import (
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/utils"
)

func TestAccessTupleSSZRoundTrip(t *testing.T) {
	original := newAccessTuple(
		"0x1111111111111111111111111111111111111111",
		"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatalf("MarshalSSZ() error = %v", err)
	}

	var decoded types_pb.AccessTuple
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatalf("UnmarshalSSZ() error = %v", err)
	}

	if !reflect.DeepEqual(&decoded, original) {
		t.Fatalf("decoded = %#v, want %#v", &decoded, original)
	}
}

func TestTransactionSSZRoundTripPreservesAccessList(t *testing.T) {
	original := &types_pb.Transaction{
		Type:     1,
		Nonce:    7,
		GasPrice: newH256("0x01"),
		Gas:      21000,
		Value:    newH256("0x09"),
		Data:     []byte{0x01, 0x02},
		Sign:     []byte{0x03, 0x04},
		To:       newH160("0x9999999999999999999999999999999999999999"),
		From:     newH160("0x1234567890123456789012345678901234567890"),
		ChainID:  1,
		Hash:     newH256("0x1111111111111111111111111111111111111111111111111111111111111111"),
		R:        newH256("0x02"),
		S:        newH256("0x03"),
		V:        newH256("0x04"),
		AccessList: []*types_pb.AccessTuple{
			newAccessTuple(
				"0x1111111111111111111111111111111111111111",
				"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			),
			newAccessTuple(
				"0x2222222222222222222222222222222222222222",
				"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			),
		},
	}

	decoded := roundTripTransactionSSZ(t, original)
	if !reflect.DeepEqual(decoded.AccessList, original.AccessList) {
		t.Fatalf("AccessList = %#v, want %#v", decoded.AccessList, original.AccessList)
	}
}

func TestTransactionSSZRoundTripPreservesBlobFields(t *testing.T) {
	original := &types_pb.Transaction{
		Type:              3,
		Nonce:             8,
		GasPrice:          newH256("0x01"),
		Gas:               22000,
		FeePerGas:         newH256("0x20"),
		PriorityFeePerGas: newH256("0x02"),
		Value:             newH256("0x0a"),
		To:                newH160("0x9999999999999999999999999999999999999999"),
		ChainID:           1,
		Hash:              newH256("0x2222222222222222222222222222222222222222222222222222222222222222"),
		R:                 newH256("0x05"),
		S:                 newH256("0x06"),
		V:                 newH256("0x07"),
		BlobFeeCap:        newH256("0x10"),
		BlobHashes: []*types_pb.H256{
			newH256("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
			newH256("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		},
		AccessList: []*types_pb.AccessTuple{
			newAccessTuple("0x3333333333333333333333333333333333333333", "0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
		},
	}

	decoded := roundTripTransactionSSZ(t, original)
	if !reflect.DeepEqual(decoded.BlobHashes, original.BlobHashes) {
		t.Fatalf("BlobHashes = %#v, want %#v", decoded.BlobHashes, original.BlobHashes)
	}
	if decoded.BlobFeeCap == nil || !reflect.DeepEqual(decoded.BlobFeeCap, original.BlobFeeCap) {
		t.Fatalf("BlobFeeCap = %#v, want %#v", decoded.BlobFeeCap, original.BlobFeeCap)
	}
	if !reflect.DeepEqual(decoded.AccessList, original.AccessList) {
		t.Fatalf("AccessList = %#v, want %#v", decoded.AccessList, original.AccessList)
	}
}

func TestTransactionSSZRoundTripPreservesPostQuantumFields(t *testing.T) {
	original := &types_pb.Transaction{
		Type:              5,
		Nonce:             9,
		Gas:               23000,
		FeePerGas:         newH256("0x30"),
		PriorityFeePerGas: newH256("0x03"),
		Value:             newH256("0x0b"),
		ChainID:           1,
		Hash:              newH256("0x3333333333333333333333333333333333333333333333333333333333333333"),
		R:                 newH256("0x08"),
		S:                 newH256("0x09"),
		V:                 newH256("0x0a"),
		PqSigAlgo:         2,
		PqPubKeyMode:      1,
		PqPubKeyData:      []byte{0x01, 0x02, 0x03},
		PqSignature:       []byte{0x04, 0x05, 0x06, 0x07},
		AccessList: []*types_pb.AccessTuple{
			newAccessTuple("0x4444444444444444444444444444444444444444", "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"),
		},
	}

	decoded := roundTripTransactionSSZ(t, original)
	if decoded.PqSigAlgo != original.PqSigAlgo {
		t.Fatalf("PqSigAlgo = %d, want %d", decoded.PqSigAlgo, original.PqSigAlgo)
	}
	if decoded.PqPubKeyMode != original.PqPubKeyMode {
		t.Fatalf("PqPubKeyMode = %d, want %d", decoded.PqPubKeyMode, original.PqPubKeyMode)
	}
	if !reflect.DeepEqual(decoded.PqPubKeyData, original.PqPubKeyData) {
		t.Fatalf("PqPubKeyData = %v, want %v", decoded.PqPubKeyData, original.PqPubKeyData)
	}
	if !reflect.DeepEqual(decoded.PqSignature, original.PqSignature) {
		t.Fatalf("PqSignature = %v, want %v", decoded.PqSignature, original.PqSignature)
	}
	if !reflect.DeepEqual(decoded.AccessList, original.AccessList) {
		t.Fatalf("AccessList = %#v, want %#v", decoded.AccessList, original.AccessList)
	}
}

func TestTransactionHashTreeRootHandlesNilOptionals(t *testing.T) {
	tx := &types_pb.Transaction{Type: 1}
	if _, err := tx.HashTreeRoot(); err != nil {
		t.Fatalf("HashTreeRoot() error = %v", err)
	}
}

func roundTripTransactionSSZ(t *testing.T, original *types_pb.Transaction) *types_pb.Transaction {
	t.Helper()

	data, err := original.MarshalSSZ()
	if err != nil {
		t.Fatalf("MarshalSSZ() error = %v", err)
	}

	var decoded types_pb.Transaction
	if err := decoded.UnmarshalSSZ(data); err != nil {
		t.Fatalf("UnmarshalSSZ() error = %v", err)
	}
	return &decoded
}

func newAccessTuple(addr string, storageKeys ...string) *types_pb.AccessTuple {
	keys := make([]*types_pb.H256, 0, len(storageKeys))
	for _, key := range storageKeys {
		keys = append(keys, newH256(key))
	}
	return &types_pb.AccessTuple{
		Address:     newH160(addr),
		StorageKeys: keys,
	}
}

func newH160(addr string) *types_pb.H160 {
	return utils.ConvertAddressToH160(types.HexToAddress(addr))
}

func newH256(hash string) *types_pb.H256 {
	return utils.ConvertHashToH256(types.HexToHash(hash))
}
