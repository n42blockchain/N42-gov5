package internal

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// A creation tx whose initcode is a lone SLOTNUM on an N42 Fusaka chain fails
// as a stack underflow inside the EVM; the tx itself still applies.
func TestApplyTransactionFusakaSlotNumEmptyStack(t *testing.T) {
	cfg := testStateTransitionChainConfig()
	cfg.Consensus = params.HotStuffConsensus
	cfg.FusakaTime = big.NewInt(0)
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	sign := func(tx *transaction.Transaction) *transaction.Transaction {
		s, err := transaction.SignTx(tx, transaction.LatestSignerForChainID(big.NewInt(1)), key)
		if err != nil {
			t.Fatalf("SignTx: %v", err)
		}
		s.SetFrom(crypto.PubkeyToAddress(key.PublicKey))
		return s
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic: %v", r)
		}
	}()
	err = applyShapeTestTx(t, cfg, func() transaction.TxData {
		return &transaction.DynamicFeeTx{
			ChainID:   uint256.NewInt(1),
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(100),
			Gas:       100_000,
			Value:     uint256.NewInt(0),
			Data:      []byte{0x4b},
		}
	}, sign)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
}
