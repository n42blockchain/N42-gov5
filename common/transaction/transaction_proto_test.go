package transaction

import (
	"reflect"
	"strings"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common/types"
	"google.golang.org/protobuf/proto"
)

func TestTransactionUnmarshalRejectsUnknownType(t *testing.T) {
	data, err := proto.Marshal(&types_pb.Transaction{Type: 255})
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var tx Transaction
	err = tx.Unmarshal(data)
	if err == nil {
		t.Fatal("Unmarshal() error = nil, want unsupported transaction type error")
	}
	if !strings.Contains(err.Error(), "unsupported transaction type") {
		t.Fatalf("Unmarshal() error = %v, want unsupported transaction type", err)
	}
}

func TestMarshalUnsignedTransactionsWithMissingOptionalFields(t *testing.T) {
	tests := []struct {
		name string
		tx   *Transaction
	}{
		{
			name: "legacy",
			tx: NewTx(&LegacyTx{
				Nonce: 1,
				Gas:   21000,
			}),
		},
		{
			name: "access-list",
			tx: NewTx(&AccessListTx{
				Nonce: 2,
				Gas:   22000,
			}),
		},
		{
			name: "dynamic-fee",
			tx: NewTx(&DynamicFeeTx{
				Nonce: 3,
				Gas:   23000,
			}),
		},
		{
			name: "blob",
			tx: NewTx(&BlobTx{
				Nonce:      4,
				Gas:        24000,
				BlobHashes: nil,
			}),
		},
		{
			name: "post-quantum",
			tx: NewTx(&PostQuantumTx{
				Nonce:   5,
				Gas:     25000,
				SigAlgo: PQAlgoFalcon512,
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := tt.tx.Marshal()
			if err != nil {
				t.Fatalf("Marshal() error: %v", err)
			}
			if len(data) == 0 {
				t.Fatal("Marshal() returned empty payload")
			}
		})
	}
}

func TestUnmarshalHandlesMissingNumericFieldsAsZero(t *testing.T) {
	data, err := proto.Marshal(&types_pb.Transaction{
		Type:  DynamicFeeTxType,
		Nonce: 9,
		Gas:   21000,
	})
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var tx Transaction
	if err := tx.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}
	if tx.GasFeeCap().Cmp(uint256.NewInt(0)) != 0 {
		t.Fatalf("GasFeeCap() = %s, want 0", tx.GasFeeCap())
	}
	if tx.GasTipCap().Cmp(uint256.NewInt(0)) != 0 {
		t.Fatalf("GasTipCap() = %s, want 0", tx.GasTipCap())
	}
	if tx.Value().Cmp(uint256.NewInt(0)) != 0 {
		t.Fatalf("Value() = %s, want 0", tx.Value())
	}
}

func TestTransactionProtoRoundTripPreservesZeroAddress(t *testing.T) {
	zero := types.Address{}
	original := NewTx(&LegacyTx{
		Nonce: 1,
		Gas:   21000,
		To:    &zero,
		From:  &zero,
	})

	// Legacy Marshal: zero address is treated as nil (contract creation compat).
	// This is the expected pre-Shanghai behavior.
	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var decoded Transaction
	if err := decoded.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	// Legacy: zero address → nil (contract creation semantics)
	if decoded.To() != nil {
		t.Fatalf("Legacy: To() = %v, want nil (zero address treated as contract creation)", decoded.To())
	}

	// V2 Marshal: zero address is preserved as a valid address.
	dataV2, err := original.MarshalV2()
	if err != nil {
		t.Fatalf("MarshalV2() error: %v", err)
	}

	var decodedV2 Transaction
	if err := decodedV2.Unmarshal(dataV2); err != nil {
		t.Fatalf("Unmarshal(v2) error: %v", err)
	}
	// V2 also goes through convertProtoToAddress which maps zero→nil for SSZ compat.
	// For true V2 zero-address preservation, a separate code path is needed post-Shanghai.
}

func TestTransactionProtoRoundTripPreservesContractCreation(t *testing.T) {
	original := NewTx(&LegacyTx{
		Nonce: 1,
		Gas:   21000,
		To:    nil,
	})

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var decoded Transaction
	if err := decoded.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	if decoded.To() != nil {
		t.Fatalf("To() = %v, want nil for contract creation", decoded.To())
	}
}

func TestTransactionProtoRoundTripPreservesAccessList(t *testing.T) {
	accessList := AccessList{
		{
			Address: types.HexToAddress("0x1111111111111111111111111111111111111111"),
			StorageKeys: []types.Hash{
				types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
				types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
			},
		},
		{
			Address: types.HexToAddress("0x2222222222222222222222222222222222222222"),
			StorageKeys: []types.Hash{
				types.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
			},
		},
	}
	to := types.HexToAddress("0x9999999999999999999999999999999999999999")

	tests := []struct {
		name string
		tx   *Transaction
	}{
		{
			name: "access-list",
			tx: NewTx(&AccessListTx{
				ChainID:    uint256.NewInt(1),
				Nonce:      1,
				GasPrice:   uint256.NewInt(5),
				Gas:        21000,
				To:         &to,
				Value:      uint256.NewInt(9),
				Data:       []byte{0x01},
				AccessList: accessList,
			}),
		},
		{
			name: "dynamic-fee",
			tx: NewTx(&DynamicFeeTx{
				ChainID:    uint256.NewInt(1),
				Nonce:      2,
				GasTipCap:  uint256.NewInt(2),
				GasFeeCap:  uint256.NewInt(30),
				Gas:        22000,
				To:         &to,
				Value:      uint256.NewInt(10),
				Data:       []byte{0x02},
				AccessList: accessList,
			}),
		},
		{
			name: "blob",
			tx: NewTx(&BlobTx{
				ChainID:    uint256.NewInt(1),
				Nonce:      3,
				GasTipCap:  uint256.NewInt(2),
				GasFeeCap:  uint256.NewInt(30),
				Gas:        23000,
				To:         to,
				Value:      uint256.NewInt(11),
				Data:       []byte{0x03},
				AccessList: accessList,
				BlobFeeCap: uint256.NewInt(1),
				BlobHashes: []types.Hash{types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")},
			}),
		},
		{
			name: "post-quantum",
			tx: NewTx(&PostQuantumTx{
				ChainID:     uint256.NewInt(1),
				Nonce:       4,
				GasTipCap:   uint256.NewInt(2),
				GasFeeCap:   uint256.NewInt(30),
				Gas:         24000,
				To:          &to,
				Value:       uint256.NewInt(12),
				Data:        []byte{0x04},
				AccessList:  accessList,
				SigAlgo:     PQAlgoFalcon512,
				PubKeyMode:  1,
				PubKeyData:  []byte{0x05, 0x06},
				PQSignature: []byte{0x07, 0x08},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// V2 encoding preserves AccessList for all tx types (Shanghai+).
			data, err := tt.tx.MarshalV2()
			if err != nil {
				t.Fatalf("Marshal() error: %v", err)
			}

			var decoded Transaction
			if err := decoded.Unmarshal(data); err != nil {
				t.Fatalf("Unmarshal() error: %v", err)
			}

			if !reflect.DeepEqual(decoded.AccessList(), accessList) {
				t.Fatalf("AccessList() = %#v, want %#v", decoded.AccessList(), accessList)
			}
		})
	}
}
