// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// controllableTxsPool is a common.ITxsPool stub whose AddLocal behavior is
// configurable, to exercise SubmitTransaction's error-propagation path
// (fakeTxsPool in tx_ingest_high_water_test.go always succeeds).
type controllableTxsPool struct {
	fakeTxsPool
	addLocalErr error
}

func (c *controllableTxsPool) AddLocal(tx *transaction.Transaction) error {
	return c.addLocalErr
}

var _ common.ITxsPool = (*controllableTxsPool)(nil)

func TestSubmitTransaction_NilPool(t *testing.T) {
	_, err := SubmitTransaction(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("SubmitTransaction(nil api) error = nil, want error")
	}

	api := &API{}
	_, err = SubmitTransaction(context.Background(), api, nil)
	if err == nil {
		t.Fatal("SubmitTransaction(nil pool) error = nil, want error")
	}
}

func TestSubmitTransaction_FeeTooHighRejected(t *testing.T) {
	api := &API{txspool: &fakeTxsPool{}}
	to := types.Address{1}
	highGasPrice := uint256.NewInt(2_000_000_000_000) // 2000 Gwei > 1000 Gwei cap
	tx := transaction.NewTransaction(0, types.Address{}, &to, uint256.NewInt(0), 21000, highGasPrice, nil)

	_, err := SubmitTransaction(context.Background(), api, tx)
	if err == nil {
		t.Fatal("SubmitTransaction(excessive gas price) error = nil, want error")
	}
}

func TestSubmitTransaction_PoolRejects(t *testing.T) {
	pool := &controllableTxsPool{addLocalErr: errors.New("nonce too low")}
	api := &API{txspool: pool}
	to := types.Address{1}
	tx := transaction.NewTransaction(0, types.Address{}, &to, uint256.NewInt(0), 21000, uint256.NewInt(1), nil)

	_, err := SubmitTransaction(context.Background(), api, tx)
	if err == nil {
		t.Fatal("SubmitTransaction(pool rejects) error = nil, want error")
	}
	if err.Error() != "nonce too low" {
		t.Fatalf("SubmitTransaction() error = %v, want pool's error echoed", err)
	}
}

func TestSubmitTransaction_Success(t *testing.T) {
	pool := &controllableTxsPool{}
	api := &API{txspool: pool}
	to := types.Address{1}
	tx := transaction.NewTransaction(0, types.Address{}, &to, uint256.NewInt(0), 21000, uint256.NewInt(1), nil)

	_, err := SubmitTransaction(context.Background(), api, tx)
	if err != nil {
		t.Fatalf("SubmitTransaction() error = %v", err)
	}
}

func TestTransactionAPI_SendTransactionUnknownAccount(t *testing.T) {
	api := &API{
		txspool:        &fakeTxsPool{},
		accountManager: &accounts.Manager{},
	}
	txAPI := NewTransactionAPI(api, new(AddrLocker))

	_, err := txAPI.SendTransaction(context.Background(), TransactionArgs{})
	if err == nil {
		t.Fatal("SendTransaction(no wallet registered) error = nil, want error")
	}
}
