package avmtypes

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/params"
)

func chainConfigFor(chainID *big.Int) *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:        chainID,
		HomesteadBlock: big.NewInt(0),
		BerlinBlock:    big.NewInt(0),
		LondonBlock:    big.NewInt(0),
	}
}

func TestToastTransactionLegacyRoundTrip(t *testing.T) {
	key, addr := testKey(t)
	chainID := big.NewInt(1)
	signer := NewLondonSigner(chainID)
	to := avmutil.BytesToAddress([]byte{0xAA})

	signed, err := SignNewTx(key, signer, &LegacyTx{
		Nonce:    0,
		GasPrice: big.NewInt(1),
		Gas:      21000,
		To:       &to,
		Value:    big.NewInt(5),
	})
	if err != nil {
		t.Fatalf("SignNewTx error: %v", err)
	}

	astTx, err := signed.ToastTransaction(chainConfigFor(chainID), big.NewInt(0))
	if err != nil {
		t.Fatalf("ToastTransaction error: %v", err)
	}
	if astTx == nil {
		t.Fatal("ToastTransaction returned nil transaction")
	}
	if astTx.Nonce() != signed.Nonce() {
		t.Errorf("Nonce mismatch: got %d, want %d", astTx.Nonce(), signed.Nonce())
	}
	if astTx.Gas() != signed.Gas() {
		t.Errorf("Gas mismatch: got %d, want %d", astTx.Gas(), signed.Gas())
	}
	gotFrom := astTx.From()
	if gotFrom == nil {
		t.Fatal("expected From() to be populated")
	}
	if *gotFrom != *ToastAddress(&addr) {
		t.Errorf("From mismatch: got %v, want %v", gotFrom, ToastAddress(&addr))
	}

	// Round trip back via FromastTransaction.
	back := new(Transaction)
	back.FromastTransaction(astTx)
	if back.Type() != LegacyTxType {
		t.Errorf("FromastTransaction Type() = %d, want %d", back.Type(), LegacyTxType)
	}
	if back.Nonce() != signed.Nonce() {
		t.Errorf("FromastTransaction Nonce() = %d, want %d", back.Nonce(), signed.Nonce())
	}
	if back.Value().Cmp(signed.Value()) != 0 {
		t.Errorf("FromastTransaction Value() = %v, want %v", back.Value(), signed.Value())
	}
}

func TestToastTransactionAccessListAndDynamicFee(t *testing.T) {
	key, _ := testKey(t)
	chainID := big.NewInt(2)
	signer := NewLondonSigner(chainID)

	alSigned, err := SignNewTx(key, signer, &AccessListTx{
		ChainID:  chainID,
		Nonce:    0,
		GasPrice: big.NewInt(1),
		Gas:      21000,
		Value:    big.NewInt(1),
	})
	if err != nil {
		t.Fatalf("SignNewTx (access list) error: %v", err)
	}
	alAst, err := alSigned.ToastTransaction(chainConfigFor(chainID), big.NewInt(0))
	if err != nil {
		t.Fatalf("ToastTransaction (access list) error: %v", err)
	}
	if alAst.Type() != transaction.AccessListTxType {
		t.Errorf("Type() = %d, want %d", alAst.Type(), transaction.AccessListTxType)
	}

	dynSigned, err := SignNewTx(key, signer, &DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     1,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(10),
		Gas:       30000,
		Value:     big.NewInt(2),
	})
	if err != nil {
		t.Fatalf("SignNewTx (dynamic fee) error: %v", err)
	}
	dynAst, err := dynSigned.ToastTransaction(chainConfigFor(chainID), big.NewInt(0))
	if err != nil {
		t.Fatalf("ToastTransaction (dynamic fee) error: %v", err)
	}
	if dynAst.Type() != transaction.DynamicFeeTxType {
		t.Errorf("Type() = %d, want %d", dynAst.Type(), transaction.DynamicFeeTxType)
	}

	backAl := new(Transaction)
	backAl.FromastTransaction(alAst)
	if backAl.Type() != AccessListTxType {
		t.Errorf("FromastTransaction (access list) Type() = %d, want %d", backAl.Type(), AccessListTxType)
	}

	backDyn := new(Transaction)
	backDyn.FromastTransaction(dynAst)
	if backDyn.Type() != DynamicFeeTxType {
		t.Errorf("FromastTransaction (dynamic fee) Type() = %d, want %d", backDyn.Type(), DynamicFeeTxType)
	}
}

func TestToastTransactionBadSignatureFails(t *testing.T) {
	// An unsigned transaction has zero-valued R/S/V which won't recover a
	// valid sender, so ToastTransaction should surface that failure.
	tx := NewTx(&LegacyTx{Nonce: 0, GasPrice: big.NewInt(1), Gas: 21000, Value: big.NewInt(1), V: big.NewInt(0), R: big.NewInt(0), S: big.NewInt(0)})
	if _, err := tx.ToastTransaction(chainConfigFor(big.NewInt(1)), big.NewInt(0)); err == nil {
		t.Fatal("expected error for a transaction with no valid signature")
	}
}
