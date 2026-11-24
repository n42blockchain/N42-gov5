// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package txspool

import (
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/params"
)

// profile1PoolConfigs returns a native chain on Profile 1 and the same chain
// outside the profile, both before Amsterdam so the EIP-3860 initcode cap
// (49,152 bytes) sits below the pool's own 128 KiB size limit.
func profile1PoolConfigs() (profile1, outside *params.ChainConfig) {
	o := *params.TestChainConfig
	o.Consensus = params.HotStuffConsensus
	o.GlamsterdamTime = nil
	o.AmsterdamTime = nil
	p := o
	p.N42Profile = params.N42Profile1
	return &p, &o
}

// C2/C4/C7: the pool of a Profile-1 chain refuses blob and post-quantum
// transactions by type; outside the profile they are not refused for that
// reason.
func TestValidateTx_Profile1RefusesTypes(t *testing.T) {
	profile1, outside := profile1PoolConfigs()
	_, from := generateTestKey()
	pq := transaction.NewTx(&transaction.PostQuantumTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(10),
		Gas: 21_000, To: &from, Value: uint256.NewInt(0)})
	for _, tc := range []struct {
		name string
		tx   *transaction.Transaction
		blob bool
	}{
		{"blob", newPoolBlobTx(from, 0, transaction.BlobSidecarVersion1), true},
		{"post-quantum", pq, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := g49NewValidationPool()
			pool.chainconfig = profile1
			pool.currentState.(*mockReadState).balances[from] = new(uint256.Int).Lsh(uint256.NewInt(1), 100)
			err := pool.validateTx(tc.tx, true, nil)
			if !errors.Is(err, internal.ErrTxTypeNotInProfile1) || !errors.Is(err, internal.ErrTxTypeNotSupported) {
				t.Fatalf("profile 1: err = %v, want ErrTxTypeNotInProfile1", err)
			}
			if got := errors.Is(err, internal.ErrBlobTxOnNativeChain); got != tc.blob {
				t.Fatalf("profile 1: err = %v, blob error %v, want %v", err, got, tc.blob)
			}
			pool.chainconfig = outside
			if err := pool.validateTx(tc.tx, true, nil); errors.Is(err, internal.ErrTxTypeNotInProfile1) {
				t.Fatalf("outside the profile: err = %v", err)
			}
		})
	}
}

// C6/C7: the pool of a Profile-1 chain caps a creation's initcode as
// execution does; outside the profile the native exemption stays.
func TestValidateTx_Profile1InitCodeCap(t *testing.T) {
	profile1, outside := profile1PoolConfigs()
	key, from := generateTestKey()
	create := func(size int) *transaction.Transaction {
		tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(10), Gas: 1_000_000, Value: uint256.NewInt(0), Data: make([]byte, size)}),
			transaction.NewLondonSigner(big.NewInt(1)), key)
		if err != nil {
			t.Fatal(err)
		}
		tx.SetFrom(from)
		return tx
	}
	for _, tc := range []struct {
		name   string
		config *params.ChainConfig
		size   int
		capped bool
	}{
		{"profile1 over cap", profile1, params.MaxInitCodeSize + 1, true},
		{"profile1 at cap", profile1, params.MaxInitCodeSize, false},
		{"outside profile over cap", outside, params.MaxInitCodeSize + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := g49NewValidationPool()
			pool.chainconfig = tc.config
			pool.currentState.(*mockReadState).balances[from] = new(uint256.Int).Lsh(uint256.NewInt(1), 100)
			err := pool.validateTx(create(tc.size), true, nil)
			if got := errors.Is(err, internal.ErrMaxInitCodeSizeExceeded); got != tc.capped {
				t.Fatalf("err = %v, capped %v, want %v", err, got, tc.capped)
			}
		})
	}
}
