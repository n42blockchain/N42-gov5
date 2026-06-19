// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
)

// TestB1RegistryCompression: all-distinct recipients, but registered → the
// registry-index column drops bytes/tx far below the raw 27.75, and the signed
// content (SigningHash/sender) is unchanged.
func TestB1RegistryCompression(t *testing.T) {
	const n = 10000
	key, _ := crypto.GenerateKey()
	reg := NewMapAddrRegistry()
	tx := &CompressedBatchTx{
		ChainID: uint256.NewInt(86), BaseNonce: 1,
		GasTipCap: uint256.NewInt(1e9), GasFeeCap: uint256.NewInt(2e10),
		To: make([]*types.Address, n), Value: make([]*uint256.Int, n),
		Gas: make([]uint64, n), Data: make([][]byte, n),
	}
	for i := 0; i < n; i++ {
		var to types.Address // ALL DISTINCT
		to[0], to[1], to[19] = byte(i), byte(i>>8), byte(i)
		tx.To[i] = &to
		reg.Register(to)
		tx.Value[i] = uint256.NewInt(uint64(i + 1))
		tx.Gas[i] = 21000
	}
	if err := tx.Sign(key); err != nil {
		t.Fatal(err)
	}
	signer, _ := tx.RecoverSender()

	raw := len(tx.EncodeBatch())
	regd := len(tx.EncodeBatchReg(reg))
	t.Logf("B1 all-distinct, N=%d: raw %.2f B/tx, registry-index %.2f B/tx (%.0f%% smaller)",
		n, float64(raw)/n, float64(regd)/n, 100*(1-float64(regd)/float64(raw)))

	if float64(regd)/n > 13 {
		t.Fatalf("registry B1 = %.2f B/tx, expected < 13 (near the 12 target)", float64(regd)/n)
	}
	// Registry encoding round-trips and yields the SAME signer (signed content
	// is registry-independent).
	dec, err := DecodeBatchReg(tx.EncodeBatchReg(reg), reg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dec.RecoverSender()
	if err != nil || got != signer {
		t.Fatalf("registry-decoded RecoverSender = %x (err %v), want %x", got, err, signer)
	}
	for i := 0; i < n; i++ {
		if *dec.To[i] != *tx.To[i] {
			t.Fatalf("registry roundtrip To[%d] mismatch", i)
		}
	}
}

// TestB2RegistryCompression: B2 with both sender and to registered → drops from
// ~51 B/tx (two 20B addrs) substantially, and the aggregate sig still verifies.
func TestB2RegistryCompression(t *testing.T) {
	const n = 2000
	reg := NewMapAddrRegistry()
	keys := make([]blscommon.SecretKey, n)
	pubReg := make(map[types.Address]blscommon.PublicKey, n)
	tx := &AggregateBatchTx{
		ChainID: uint256.NewInt(86),
		GasTipCap: uint256.NewInt(1e9), GasFeeCap: uint256.NewInt(2e10),
		Sender: make([]*types.Address, n), Nonce: make([]uint64, n),
		To: make([]*types.Address, n), Value: make([]*uint256.Int, n),
		Gas: make([]uint64, n), Data: make([][]byte, n),
	}
	for i := 0; i < n; i++ {
		sk, _ := bls.RandKey()
		keys[i] = sk
		sa := BLSAddress(sk.PublicKey())
		pubReg[sa] = sk.PublicKey()
		reg.Register(sa)
		var to types.Address
		to[0], to[1] = byte(i), byte(i>>8)
		tx.To[i] = &to
		reg.Register(to)
		tx.Nonce[i] = uint64(i)
		tx.Value[i] = uint256.NewInt(uint64(i + 1))
		tx.Gas[i] = 21000
	}
	if err := tx.AggregateBatchSign(keys); err != nil {
		t.Fatal(err)
	}
	resolve := func(a types.Address) (blscommon.PublicKey, bool) { pk, ok := pubReg[a]; return pk, ok }

	raw := len(tx.EncodeBatch())
	regd := len(tx.EncodeBatchReg(reg))
	t.Logf("B2 cross-account, N=%d: raw %.2f B/tx, registry-index %.2f B/tx (%.0f%% smaller); one 96B BLS sig",
		n, float64(raw)/n, float64(regd)/n, 100*(1-float64(regd)/float64(raw)))

	dec, err := DecodeAggregateBatchReg(tx.EncodeBatchReg(reg), reg)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := dec.Verify(resolve)
	if err != nil || !ok {
		t.Fatalf("registry-decoded aggregate Verify = %v (err %v), want true", ok, err)
	}
}
