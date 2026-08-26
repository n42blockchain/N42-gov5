// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	commonhash "github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// randomTestAccessList mixes empty lists, tuples with no keys, and tuples with
// many keys so every list-length prefix path (short and long form) is hit.
func randomTestAccessList(rng *rand.Rand, variant int) AccessList {
	switch variant % 4 {
	case 0:
		return nil
	case 1:
		return AccessList{}
	}
	n := 1 + rng.Intn(4)
	al := make(AccessList, n)
	for i := range al {
		rng.Read(al[i].Address[:])
		keys := rng.Intn(6)
		if variant%7 == 0 {
			keys = 20 // pushes the tuple over the 55-byte short-list limit
		}
		al[i].StorageKeys = make([]types.Hash, keys)
		for k := range al[i].StorageKeys {
			rng.Read(al[i].StorageKeys[k][:])
		}
	}
	return al
}

func randomTestTo(rng *rand.Rand, i int) *types.Address {
	if i%4 == 0 {
		return nil
	}
	a := types.Address{}
	rng.Read(a[:])
	return &a
}

// TestDynamicFeeTxHashMatchesReflectionEncoder pins the direct encoder to the
// reflection encoder it replaces, field for field, over nil/zero/small/full
// uint256 values, create vs call, calldata across the short/long string
// boundary, and access lists across the short/long list boundary.
func TestDynamicFeeTxHashMatchesReflectionEncoder(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 2000; i++ {
		data := make([]byte, i%300)
		rng.Read(data)
		tx := &DynamicFeeTx{
			ChainID:    randomTestU256(rng, i),
			Nonce:      rng.Uint64(),
			GasTipCap:  randomTestU256(rng, i+1),
			GasFeeCap:  randomTestU256(rng, i+2),
			Gas:        rng.Uint64(),
			To:         randomTestTo(rng, i),
			Value:      randomTestU256(rng, i+3),
			Data:       data,
			AccessList: randomTestAccessList(rng, i),
			V:          randomTestU256(rng, i+4),
			R:          randomTestU256(rng, i+5),
			S:          randomTestU256(rng, i+6),
		}
		want := commonhash.PrefixedRlpHash(DynamicFeeTxType, []interface{}{
			tx.ChainID, tx.Nonce, tx.GasTipCap, tx.GasFeeCap, tx.Gas, tx.To,
			tx.Value, tx.Data, tx.AccessList, tx.V, tx.R, tx.S,
		})
		if got := tx.hash(); got != want {
			t.Fatalf("case %d: hash mismatch\n got  %x\n want %x", i, got, want)
		}
	}
}

func TestAccessListTxHashMatchesReflectionEncoder(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 2000; i++ {
		data := make([]byte, i%300)
		rng.Read(data)
		tx := &AccessListTx{
			ChainID:    randomTestU256(rng, i),
			Nonce:      rng.Uint64(),
			GasPrice:   randomTestU256(rng, i+1),
			Gas:        rng.Uint64(),
			To:         randomTestTo(rng, i),
			Value:      randomTestU256(rng, i+2),
			Data:       data,
			AccessList: randomTestAccessList(rng, i),
			V:          randomTestU256(rng, i+3),
			R:          randomTestU256(rng, i+4),
			S:          randomTestU256(rng, i+5),
		}
		want := commonhash.PrefixedRlpHash(AccessListTxType, []interface{}{
			tx.ChainID, tx.Nonce, tx.GasPrice, tx.Gas, tx.To, tx.Value, tx.Data,
			tx.AccessList, tx.V, tx.R, tx.S,
		})
		if got := tx.hash(); got != want {
			t.Fatalf("case %d: hash mismatch\n got  %x\n want %x", i, got, want)
		}
	}
}

// TestTypedTxHashIgnoresStorageFields: From and Sign are storage-side and must
// not change the hash — the reflection list never included them either.
func TestTypedTxHashIgnoresStorageFields(t *testing.T) {
	to := types.Address{1, 2, 3}
	from := types.Address{9, 9, 9}
	base := &DynamicFeeTx{ChainID: uint256.NewInt(1), Nonce: 7, GasTipCap: uint256.NewInt(2),
		GasFeeCap: uint256.NewInt(30), Gas: 21000, To: &to, Value: uint256.NewInt(5),
		V: uint256.NewInt(1), R: uint256.NewInt(2), S: uint256.NewInt(3)}
	with := *base
	with.From = &from
	with.Sign = []byte{1, 2, 3}
	if base.hash() != with.hash() {
		t.Fatal("From/Sign leaked into the transaction hash")
	}
}

func benchDynamicFeeTx() *DynamicFeeTx {
	to := types.Address{0xde, 0xad}
	data := make([]byte, 68) // ERC-20 transfer calldata size
	return &DynamicFeeTx{
		ChainID: uint256.NewInt(1), Nonce: 123456, GasTipCap: uint256.NewInt(1_500_000_000),
		GasFeeCap: uint256.NewInt(40_000_000_000), Gas: 65000, To: &to,
		Value: new(uint256.Int), Data: data,
		V: uint256.NewInt(1),
		R: new(uint256.Int).SetBytes([]byte{0x7f, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}),
		S: new(uint256.Int).SetBytes([]byte{0x3f, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}),
	}
}

func BenchmarkDynamicFeeTxHashDirect(b *testing.B) {
	tx := benchDynamicFeeTx()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = tx.hash()
	}
}

func BenchmarkDynamicFeeTxHashReflection(b *testing.B) {
	tx := benchDynamicFeeTx()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = commonhash.PrefixedRlpHash(DynamicFeeTxType, []interface{}{
			tx.ChainID, tx.Nonce, tx.GasTipCap, tx.GasFeeCap, tx.Gas, tx.To,
			tx.Value, tx.Data, tx.AccessList, tx.V, tx.R, tx.S,
		})
	}
}
