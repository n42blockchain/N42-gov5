package internal

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func applyShapeTestTx(t *testing.T, config *params.ChainConfig, build func() transaction.TxData, sign func(tx *transaction.Transaction) *transaction.Transaction) error {
	t.Helper()
	db := memdb.NewTestDB(t)
	txdb := memdb.BeginRw(t, db)
	ibs := state.New(state.NewPlainState(txdb, 1))
	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")

	tx := sign(transaction.NewTx(build()))
	sender := *tx.From()
	ibs.CreateAccount(sender, false)
	ibs.AddBalance(sender, uint256.NewInt(1_000_000_000_000_000_000))

	excessBlobGas := uint64(0)
	header := &block.Header{
		Number:        uint256.NewInt(1),
		GasLimit:      30_000_000,
		Time:          1,
		BaseFee:       uint256.NewInt(7),
		Difficulty:    uint256.NewInt(0),
		ExcessBlobGas: &excessBlobGas,
	}
	gp := new(common.GasPool).AddGas(header.GasLimit)
	usedGas := uint64(0)
	_, _, err := ApplyTransaction(config, func(uint64) types.Hash { return types.Hash{} },
		nil, &coinbase, gp, ibs, state.NewNoopWriter(), header, tx, &usedGas, vm2.Config{})
	return err
}

func nativeShapeTestConfig() *params.ChainConfig {
	c := testStateTransitionChainConfig()
	c.Consensus = params.HotStuffConsensus
	return c
}

// A set-code transaction is held to geth's EIP-7702 shape rules on Ethereum
// chains: not before Prague, never a contract creation, and never with an
// empty authorization list. N42 native chains keep their own rules.
func TestApplyTransactionRejectsMalformedSetCodeTx(t *testing.T) {
	prePrague := testStateTransitionChainConfig()
	prePrague.PragueTime = nil
	prePrague.OsakaTime = nil

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	authKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate authority key: %v", err)
	}
	recipient := types.HexToAddress("0x2222222222222222222222222222222222222222")
	auth := signAuthorization(t, authKey, &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
		Address: types.HexToAddress("0x3333333333333333333333333333333333333333"),
		Nonce:   0,
	})
	sign := func(tx *transaction.Transaction) *transaction.Transaction {
		signed, err := transaction.SignTx(tx, transaction.LatestSignerForChainID(big.NewInt(1)), senderKey)
		if err != nil {
			t.Fatalf("SignTx: %v", err)
		}
		from := crypto.PubkeyToAddress(senderKey.PublicKey)
		signed.SetFrom(from)
		return signed
	}
	setCode := func(to *types.Address, auths transaction.AuthorizationList) func() transaction.TxData {
		return func() transaction.TxData {
			return &transaction.SetCodeTx{
				ChainID:   uint256.NewInt(1),
				GasTipCap: uint256.NewInt(1),
				GasFeeCap: uint256.NewInt(100),
				Gas:       100_000,
				To:        to,
				Value:     uint256.NewInt(0),
				AuthList:  auths,
			}
		}
	}

	for _, tc := range []struct {
		name    string
		config  *params.ChainConfig
		to      *types.Address
		auths   transaction.AuthorizationList
		wantErr error
	}{
		{"valid", testStateTransitionChainConfig(), &recipient, transaction.AuthorizationList{auth}, nil},
		{"create", testStateTransitionChainConfig(), nil, transaction.AuthorizationList{auth}, ErrSetCodeTxCreate},
		{"empty auth list", testStateTransitionChainConfig(), &recipient, transaction.AuthorizationList{}, ErrEmptyAuthList},
		{"pre-prague", prePrague, &recipient, transaction.AuthorizationList{auth}, ErrTxTypeNotSupported},
		{"native empty auth list", nativeShapeTestConfig(), &recipient, transaction.AuthorizationList{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := applyShapeTestTx(t, tc.config, setCode(tc.to, tc.auths), sign)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// A creation transaction's initcode is capped from Shanghai (EIP-3860) on
// Ethereum chains; N42 native chains keep their own rules.
func TestApplyTransactionRejectsOversizedInitCode(t *testing.T) {
	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	sign := func(tx *transaction.Transaction) *transaction.Transaction {
		signed, err := transaction.SignTx(tx, transaction.LatestSignerForChainID(big.NewInt(1)), senderKey)
		if err != nil {
			t.Fatalf("SignTx: %v", err)
		}
		signed.SetFrom(crypto.PubkeyToAddress(senderKey.PublicKey))
		return signed
	}
	create := func(size int) func() transaction.TxData {
		return func() transaction.TxData {
			// STOP-filled initcode: zero bytes keep intrinsic gas low.
			return &transaction.DynamicFeeTx{
				ChainID:   uint256.NewInt(1),
				GasTipCap: uint256.NewInt(1),
				GasFeeCap: uint256.NewInt(100),
				Gas:       10_000_000,
				Value:     uint256.NewInt(0),
				Data:      bytes.Repeat([]byte{0x00}, size),
			}
		}
	}

	for _, tc := range []struct {
		name    string
		config  *params.ChainConfig
		size    int
		wantErr error
	}{
		{"at limit", testStateTransitionChainConfig(), params.MaxInitCodeSize, nil},
		{"over limit", testStateTransitionChainConfig(), params.MaxInitCodeSize + 1, ErrMaxInitCodeSizeExceeded},
		{"native over limit", nativeShapeTestConfig(), params.MaxInitCodeSize + 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := applyShapeTestTx(t, tc.config, create(tc.size), sign)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
