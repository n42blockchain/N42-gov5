// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// profile1RulesTestConfig is a native HotStuff chain on Standard Profile 1,
// Amsterdam from block 0 as the standard genesis sets it.
func profile1RulesTestConfig() *params.ChainConfig {
	c := nativeShapeTestConfig()
	c.N42Profile = params.N42Profile1
	c.AmsterdamTime = new(big.Int)
	return c
}

func profile1TestSigner(t *testing.T) func(tx *transaction.Transaction) *transaction.Transaction {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	return func(tx *transaction.Transaction) *transaction.Transaction {
		signed, err := transaction.SignTx(tx, transaction.LatestSignerForChainID(big.NewInt(1)), key)
		if err != nil {
			t.Fatalf("SignTx: %v", err)
		}
		signed.SetFrom(crypto.PubkeyToAddress(key.PublicKey))
		return signed
	}
}

func checkProfile1Err(t *testing.T, err, want error) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatalf("unexpected error %v", err)
		}
		return
	}
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

// C3: a Profile-1 chain holds a set-code transaction to the EIP-7702 shape;
// a native chain outside the profile keeps its exemption.
func TestProfile1SetCodeTxShape(t *testing.T) {
	sign := profile1TestSigner(t)
	authKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate authority key: %v", err)
	}
	recipient := types.HexToAddress("0x2222222222222222222222222222222222222222")
	auth := signAuthorization(t, authKey, &transaction.Authorization{
		ChainID: *uint256.NewInt(1),
		Address: types.HexToAddress("0x3333333333333333333333333333333333333333"),
	})
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
		name   string
		config *params.ChainConfig
		to     *types.Address
		auths  transaction.AuthorizationList
		want   error
	}{
		{"profile1 valid", profile1RulesTestConfig(), &recipient, transaction.AuthorizationList{auth}, nil},
		{"profile1 create", profile1RulesTestConfig(), nil, transaction.AuthorizationList{auth}, ErrSetCodeTxCreate},
		{"profile1 empty auth list", profile1RulesTestConfig(), &recipient, transaction.AuthorizationList{}, ErrEmptyAuthList},
		{"native outside profile keeps exemption", nativeShapeTestConfig(), &recipient, transaction.AuthorizationList{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkProfile1Err(t, applyShapeTestTx(t, tc.config, setCode(tc.to, tc.auths), sign), tc.want)
		})
	}
}

// profile1TypedTx returns a transaction of type typ (0x00-0x05) with its
// sender set, unsigned: the type rules are checked before any signature.
func profile1TypedTx(typ uint8, from types.Address) *transaction.Transaction {
	to := types.HexToAddress("0x2222222222222222222222222222222222222222")
	var inner transaction.TxData
	switch typ {
	case transaction.LegacyTxType:
		inner = &transaction.LegacyTx{GasPrice: uint256.NewInt(100), Gas: 100_000, To: &to, Value: uint256.NewInt(0)}
	case transaction.AccessListTxType:
		inner = &transaction.AccessListTx{ChainID: uint256.NewInt(1), GasPrice: uint256.NewInt(100), Gas: 100_000, To: &to, Value: uint256.NewInt(0)}
	case transaction.DynamicFeeTxType:
		inner = &transaction.DynamicFeeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(100), Gas: 100_000, To: &to, Value: uint256.NewInt(0)}
	case transaction.BlobTxType:
		inner = &transaction.BlobTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(100), Gas: 100_000, To: to, Value: uint256.NewInt(0),
			BlobFeeCap: uint256.NewInt(100), BlobHashes: []types.Hash{{transaction.VersionedHashVersionKZG}}}
	case transaction.SetCodeTxType:
		inner = &transaction.SetCodeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(100), Gas: 100_000, To: &to, Value: uint256.NewInt(0),
			AuthList: transaction.AuthorizationList{{ChainID: *uint256.NewInt(1)}}}
	case transaction.PostQuantumTxType:
		inner = &transaction.PostQuantumTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(100), Gas: 100_000, To: &to, Value: uint256.NewInt(0)}
	default:
		panic("profile1TypedTx: no such type")
	}
	tx := transaction.NewTx(inner)
	tx.SetFrom(from)
	return tx
}

// C1-C4: the allow-list is exactly {0, 1, 2, 4} on Profile 1; a chain
// outside the profile is never refused by it.
func TestCheckProfile1TxType(t *testing.T) {
	p1 := profile1RulesTestConfig()
	for typ := 0; typ <= 0xff; typ++ {
		allowed := typ == 0 || typ == 1 || typ == 2 || typ == 4
		err := CheckProfile1TxType(p1, uint8(typ))
		if allowed != (err == nil) {
			t.Fatalf("type 0x%02x: err = %v, allowed %v", typ, err, allowed)
		}
		if err != nil && (!errors.Is(err, ErrTxTypeNotInProfile1) || !errors.Is(err, ErrTxTypeNotSupported)) {
			t.Fatalf("type 0x%02x: err = %v, want ErrTxTypeNotInProfile1 and ErrTxTypeNotSupported", typ, err)
		}
		if got := errors.Is(err, ErrBlobTxOnNativeChain); got != (typ == transaction.BlobTxType) {
			t.Fatalf("type 0x%02x: blob error %v", typ, got)
		}
		for _, other := range []*params.ChainConfig{nil, testStateTransitionChainConfig(), nativeShapeTestConfig()} {
			if err := CheckProfile1TxType(other, uint8(typ)); err != nil {
				t.Fatalf("type 0x%02x outside the profile: %v", typ, err)
			}
		}
	}
}

// C2: execution on a Profile-1 chain refuses a blob transaction by type; a
// native chain outside the profile does not refuse it for that reason. (A
// post-quantum transaction has no sender this signer can derive, so it is
// covered by the pool, block validity and includability tests.)
func TestProfile1ExecutionRefusesTypes(t *testing.T) {
	from := types.HexToAddress("0x4444444444444444444444444444444444444444")
	for _, tc := range []struct {
		name    string
		config  *params.ChainConfig
		typ     uint8
		refused bool
	}{
		{"profile1 blob", profile1RulesTestConfig(), transaction.BlobTxType, true},
		{"profile1 dynamic fee", profile1RulesTestConfig(), transaction.DynamicFeeTxType, false},
		{"native blob outside profile", nativeShapeTestConfig(), transaction.BlobTxType, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := profile1TypedTx(tc.typ, from)
			err := applyShapeTestTx(t, tc.config, func() transaction.TxData { return &transaction.LegacyTx{} },
				func(*transaction.Transaction) *transaction.Transaction { return tx })
			if got := errors.Is(err, ErrTxTypeNotInProfile1); got != tc.refused {
				t.Fatalf("err = %v, refused %v, want %v", err, got, tc.refused)
			}
		})
	}
}

// C6: a Profile-1 chain caps initcode from block 0 (EIP-7954 under
// Amsterdam, EIP-3860 before it); a native chain outside the profile keeps
// its pre-Amsterdam exemption.
func TestProfile1InitCodeCap(t *testing.T) {
	sign := profile1TestSigner(t)
	create := func(size int) func() transaction.TxData {
		return func() transaction.TxData {
			return &transaction.DynamicFeeTx{
				ChainID:   uint256.NewInt(1),
				GasTipCap: uint256.NewInt(1),
				GasFeeCap: uint256.NewInt(100),
				Gas:       10_000_000,
				Value:     uint256.NewInt(0),
				Data:      make([]byte, size),
			}
		}
	}
	preAmsterdam := profile1RulesTestConfig()
	preAmsterdam.AmsterdamTime = nil
	for _, tc := range []struct {
		name   string
		config *params.ChainConfig
		size   int
		want   error
	}{
		{"profile1 amsterdam over cap", profile1RulesTestConfig(), params.MaxInitCodeSizeEIP7954 + 1, ErrMaxInitCodeSizeExceeded},
		{"profile1 pre-amsterdam over cap", preAmsterdam, params.MaxInitCodeSize + 1, ErrMaxInitCodeSizeExceeded},
		{"profile1 pre-amsterdam at cap", preAmsterdam, params.MaxInitCodeSize, nil},
		{"native outside profile over cap", nativeShapeTestConfig(), params.MaxInitCodeSize + 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkProfile1Err(t, applyShapeTestTx(t, tc.config, create(tc.size), sign), tc.want)
		})
	}
}

// Under deferred execution a Profile-1 block whose transaction execution
// would refuse is not includable: a refused type, an over-cap initcode or a
// malformed set-code transaction. Outside the profile the same transactions
// are not refused for those reasons.
func TestProfile1DeferredTxPlanRefuses(t *testing.T) {
	sign := profile1TestSigner(t)
	from := types.HexToAddress("0x4444444444444444444444444444444444444444")
	hdr := &block.Header{Number: uint256.NewInt(10), Time: 1, GasLimit: 30_000_000, BaseFee: uint256.NewInt(1)}
	// The plan recovers senders: the transactions that get past the type
	// check are signed.
	overCap := sign(transaction.NewTx(&transaction.DynamicFeeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(100),
		Gas: 20_000_000, Value: uint256.NewInt(0), Data: make([]byte, params.MaxInitCodeSizeEIP7954+1)}))
	noAuth := sign(transaction.NewTx(&transaction.SetCodeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(100),
		Gas: 100_000, To: &from, Value: uint256.NewInt(0)}))
	plain := sign(transaction.NewTx(&transaction.DynamicFeeTx{ChainID: uint256.NewInt(1), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(100),
		Gas: 100_000, To: &from, Value: uint256.NewInt(0)}))
	for _, tc := range []struct {
		name string
		tx   *transaction.Transaction
		want error
	}{
		{"blob", profile1TypedTx(transaction.BlobTxType, from), ErrBlobTxOnNativeChain},
		{"post-quantum", profile1TypedTx(transaction.PostQuantumTxType, from), ErrTxTypeNotInProfile1},
		{"initcode over cap", overCap, ErrMaxInitCodeSizeExceeded},
		{"set code without authorizations", noAuth, ErrEmptyAuthList},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := deferredTxPlan(profile1RulesTestConfig(), hdr, []*transaction.Transaction{tc.tx})
			checkProfile1Err(t, err, tc.want)
			outside := nativeShapeTestConfig()
			outside.AmsterdamTime = new(big.Int)
			if _, err := deferredTxPlan(outside, hdr, []*transaction.Transaction{tc.tx}); errors.Is(err, tc.want) {
				t.Fatalf("outside the profile: %v", err)
			}
		})
	}
	if _, err := deferredTxPlan(profile1RulesTestConfig(), hdr, []*transaction.Transaction{plain}); err != nil {
		t.Fatalf("dynamic fee: %v", err)
	}
}

// Block validity: a Profile-1 block carrying a refused type fails
// ValidateBody; the same block on a chain outside the profile does not fail
// for that reason.
func TestProfile1ValidateBodyRefusesTypes(t *testing.T) {
	bc, genesis, _ := newReaderTestChain(t)
	from := types.HexToAddress("0x4444444444444444444444444444444444444444")
	for _, typ := range []uint8{transaction.BlobTxType, transaction.PostQuantumTxType} {
		txs := []*transaction.Transaction{profile1TypedTx(transaction.DynamicFeeTxType, from), profile1TypedTx(typ, from)}
		blk := block.NewBlock(&block.Header{
			Number:     uint256.NewInt(2),
			ParentHash: genesis.Hash(),
			Difficulty: uint256.NewInt(1),
			TxHash:     block.TxRootAt(txs, 0),
		}, txs).(*block.Block)
		err := NewBlockValidator(profile1RulesTestConfig(), bc, nil).ValidateBody(blk)
		if !errors.Is(err, ErrTxTypeNotInProfile1) {
			t.Fatalf("type 0x%02x on profile 1: err = %v", typ, err)
		}
		err = NewBlockValidator(nativeShapeTestConfig(), bc, nil).ValidateBody(blk)
		if errors.Is(err, ErrTxTypeNotInProfile1) {
			t.Fatalf("type 0x%02x outside the profile: err = %v", typ, err)
		}
	}
}
