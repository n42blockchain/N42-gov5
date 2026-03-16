package transaction

import (
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

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	var decoded Transaction
	if err := decoded.Unmarshal(data); err != nil {
		t.Fatalf("Unmarshal() error: %v", err)
	}

	if decoded.To() == nil || *decoded.To() != zero {
		t.Fatalf("To() = %v, want zero address", decoded.To())
	}
	if decoded.From() == nil || *decoded.From() != zero {
		t.Fatalf("From() = %v, want zero address", decoded.From())
	}
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
