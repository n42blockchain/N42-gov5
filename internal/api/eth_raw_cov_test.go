// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/accounts"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

func apiTNewTransactionAPI(t *testing.T) *TransactionAPI {
	t.Helper()
	bca := setupStorageTestAPI(t)
	bca.api.txspool = &fakeTxsPool{}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	bca.api.bc = newPreciseChainStub(bca.api.chainConfig, bca.api.db, genesis)
	return NewTransactionAPI(bca.api, new(AddrLocker))
}

func TestTransactionAPI_Sign_NoAccountManager(t *testing.T) {
	txAPI := apiTNewTransactionAPI(t)
	if _, err := txAPI.Sign(context.Background(), avmcommon.Address{}, nil); err == nil {
		t.Fatal("Sign(no account manager) error = nil, want error")
	}
}

func TestTransactionAPI_SignTransaction_NoFrom(t *testing.T) {
	txAPI := apiTNewTransactionAPI(t)
	if _, err := txAPI.SignTransaction(context.Background(), TransactionArgs{}); err == nil {
		t.Fatal("SignTransaction(no From) error = nil, want error")
	}
}

func TestTransactionAPI_SignTransaction_NoAccountManager(t *testing.T) {
	txAPI := apiTNewTransactionAPI(t)
	from := avmcommon.Address{}
	args := TransactionArgs{From: &from}
	if _, err := txAPI.SignTransaction(context.Background(), args); err == nil {
		t.Fatal("SignTransaction(no account manager) error = nil, want error")
	}
}

func TestTransactionAPI_GetRawTransactionByHash_NotFound(t *testing.T) {
	txAPI := apiTNewTransactionAPI(t)
	raw, err := txAPI.GetRawTransactionByHash(context.Background(), avmcommon.Hash{})
	if err != nil {
		t.Fatalf("GetRawTransactionByHash() error = %v", err)
	}
	if raw != nil {
		t.Fatalf("GetRawTransactionByHash(unknown) = %v, want nil", raw)
	}
}

func TestTransactionAPI_GetRawTransactionByBlockHashAndIndex_NotFound(t *testing.T) {
	txAPI := apiTNewTransactionAPI(t)
	raw, err := txAPI.GetRawTransactionByBlockHashAndIndex(context.Background(), avmcommon.Hash{}, 0)
	if err != nil {
		t.Fatalf("GetRawTransactionByBlockHashAndIndex() error = %v", err)
	}
	if raw != nil {
		t.Fatalf("GetRawTransactionByBlockHashAndIndex(unknown block) = %v, want nil", raw)
	}
}

func TestTransactionAPI_PendingTransactions_NoAccounts(t *testing.T) {
	txAPI := apiTNewTransactionAPI(t)
	txs, err := txAPI.PendingTransactions()
	if err != nil {
		t.Fatalf("PendingTransactions() error = %v", err)
	}
	if len(txs) != 0 {
		t.Fatalf("PendingTransactions(no accounts) = %v, want empty", txs)
	}
}

func TestTransactionAPI_Resend_DelegatesToSendTransaction(t *testing.T) {
	txAPI := apiTNewTransactionAPI(t)
	txAPI.api.accountManager = accounts.NewManager(&accounts.Config{})

	from := avmcommon.Address{}
	args := TransactionArgs{From: &from}
	// Account manager has no wallets, so SendTransaction fails with "account
	// not found"; exercise the Resend -> SendTransaction delegation path with
	// gas overrides set.
	gasPrice := (*hexutil.Big)(big.NewInt(1))
	gasLimit := new(hexutil.Uint64)
	*gasLimit = 21000
	if _, err := txAPI.Resend(context.Background(), args, gasPrice, gasLimit); err == nil {
		t.Fatal("Resend() error = nil, want error from SendTransaction")
	}
}
