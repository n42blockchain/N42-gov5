// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// Hash() of a transaction carrying its cached consensus encoding (decoded
// from the wire, or encoded once) equals the hash re-derived from the
// fields, for every type the shortcut covers, across RLP length boundaries.
func TestHashFromCachedEncodingMatchesFieldHash(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	chainID := big.NewInt(94)
	cid, _ := uint256.FromBig(chainID)
	to := types.Address{0x11, 0x22}
	var txs []*Transaction
	for _, dl := range []int{0, 1, 55, 56, 300, 70000} {
		data := make([]byte, dl)
		for i := range data {
			data[i] = byte(i)
		}
		for _, dest := range []*types.Address{&to, nil} {
			legacy, err := SignNewTx(key, NewEIP155Signer(chainID), &LegacyTx{Nonce: uint64(dl), GasPrice: uint256.NewInt(7_000_000_000), Gas: 90000, To: dest, Value: uint256.MustFromDecimal("123456789012345678901234567890"), Data: data})
			if err != nil {
				t.Fatal(err)
			}
			al, err := SignNewTx(key, NewEIP2930Signer(chainID), &AccessListTx{ChainID: cid, Nonce: 3, GasPrice: uint256.NewInt(1), Gas: 50000, To: dest, Value: uint256.NewInt(0), Data: data, AccessList: AccessList{{Address: to, StorageKeys: []types.Hash{{1}, {2}}}}})
			if err != nil {
				t.Fatal(err)
			}
			df, err := SignNewTx(key, NewLondonSigner(chainID), &DynamicFeeTx{ChainID: cid, Nonce: ^uint64(0) - 1, GasTipCap: uint256.NewInt(2), GasFeeCap: uint256.NewInt(1 << 40), Gas: 21000, To: dest, Value: uint256.NewInt(1), Data: data})
			if err != nil {
				t.Fatal(err)
			}
			txs = append(txs, legacy, al, df)
		}
	}
	for i, tx := range txs {
		want := tx.inner.hash()
		enc, err := EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := DecodeEthereumTransaction(enc)
		if err != nil {
			t.Fatal(err)
		}
		if got := dec.Hash(); got != want {
			t.Fatalf("tx %d (type %d): decoded hash %x != field hash %x", i, tx.Type(), got, want)
		}
		fresh := NewTx(tx.inner)
		if _, err := fresh.EthEncoded(); err != nil {
			t.Fatal(err)
		}
		if got := fresh.Hash(); got != want {
			t.Fatalf("tx %d (type %d): encoded-then-hashed %x != field hash %x", i, tx.Type(), got, want)
		}
	}
}
