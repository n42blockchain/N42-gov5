// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Performance data for two decisions:
//  1. PQ signature-verify cost (Falcon / Dilithium2 / Dilithium3) vs secp256k1
//     ecrecover — calibrates the PQ gas schedule (pq_gas.go constants are
//     placeholders until benchmarked).
//  2. CompressedBatchTx (0x06) throughput + bytes/tx under different recipient
//     distributions — decides B1 productionization.
//
//   go test -tags nosqlite,noboltdb -run XXX -bench . -benchmem ./common/transaction/
//   go test -tags nosqlite,noboltdb -run BytesPerTx -v ./common/transaction/

package transaction

import (
	"crypto/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/dilithium/mode2"
	"github.com/n42blockchain/N42/crypto/dilithium/mode3"
	"github.com/n42blockchain/N42/crypto/falcon"
)

var benchMsg = crypto.Keccak256([]byte("n42 benchmark message"))

// --- signature verify cost (gas calibration) ---------------------------------

func BenchmarkVerifySecp256k1Ecrecover(b *testing.B) {
	key, _ := crypto.GenerateKey()
	sig, _ := crypto.Sign(benchMsg, key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := crypto.SigToPub(benchMsg, sig); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyFalcon512(b *testing.B) {
	pk, sk, err := falcon.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	sig, err := falcon.Sign(sk, benchMsg)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !falcon.Verify(pk, benchMsg, sig) {
			b.Fatal("falcon verify failed")
		}
	}
}

func BenchmarkVerifyDilithium2(b *testing.B) {
	pk, sk, err := mode2.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	sig := make([]byte, mode2.SignatureSize)
	mode2.SignTo(sk, benchMsg, sig)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !mode2.Verify(pk, benchMsg, sig) {
			b.Fatal("dilithium2 verify failed")
		}
	}
}

func BenchmarkVerifyDilithium3(b *testing.B) {
	pk, sk, err := mode3.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	sig := make([]byte, mode3.SignatureSize)
	mode3.SignTo(sk, benchMsg, sig)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !mode3.Verify(pk, benchMsg, sig) {
			b.Fatal("dilithium3 verify failed")
		}
	}
}

// --- B1 batch throughput ------------------------------------------------------

func benchBatch(b *testing.B, n int) *CompressedBatchTx {
	b.Helper()
	key, _ := crypto.GenerateKey()
	tx := &CompressedBatchTx{
		ChainID: uint256.NewInt(86), BaseNonce: 1,
		GasTipCap: uint256.NewInt(1e9), GasFeeCap: uint256.NewInt(2e10),
		To: make([]*types.Address, n), Value: make([]*uint256.Int, n),
		Gas: make([]uint64, n), Data: make([][]byte, n),
	}
	for i := 0; i < n; i++ {
		var to types.Address
		to[0], to[19] = byte(i), byte(i>>8)
		tx.To[i] = &to
		tx.Value[i] = uint256.NewInt(uint64(i + 1))
		tx.Gas[i] = 21000
	}
	if err := tx.Sign(key); err != nil {
		b.Fatal(err)
	}
	return tx
}

func BenchmarkBatchSign10k(b *testing.B) {
	key, _ := crypto.GenerateKey()
	tx := benchBatch(b, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tx.Sign(key)
	}
}

func BenchmarkBatchRecover10k(b *testing.B) {
	tx := benchBatch(b, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tx.RecoverSender(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBatchExpand10k(b *testing.B) {
	tx := benchBatch(b, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tx.Expand(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBatchEncode10k(b *testing.B) {
	tx := benchBatch(b, 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = tx.EncodeBatch()
	}
}

func BenchmarkBatchDecode10k(b *testing.B) {
	tx := benchBatch(b, 10000)
	enc := tx.EncodeBatch()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeBatch(enc); err != nil {
			b.Fatal(err)
		}
	}
}

// --- bytes/tx under recipient distributions (compression data) ----------------

func TestBatchBytesPerTxDistributions(t *testing.T) {
	const n = 10000
	key, _ := crypto.GenerateKey()
	mk := func(pick func(i int) *types.Address) int {
		tx := &CompressedBatchTx{
			ChainID: uint256.NewInt(86), BaseNonce: 1,
			GasTipCap: uint256.NewInt(1e9), GasFeeCap: uint256.NewInt(2e10),
			To: make([]*types.Address, n), Value: make([]*uint256.Int, n),
			Gas: make([]uint64, n), Data: make([][]byte, n),
		}
		for i := 0; i < n; i++ {
			tx.To[i] = pick(i)
			tx.Value[i] = uint256.NewInt(uint64(i%1000 + 1))
			tx.Gas[i] = 21000
		}
		_ = tx.Sign(key)
		enc := tx.EncodeBatch()
		// sanity: decode must round-trip
		if _, err := DecodeBatch(enc); err != nil {
			t.Fatal(err)
		}
		return len(enc)
	}

	pool := make([]*types.Address, 16)
	for i := range pool {
		var a types.Address
		a[0] = byte(i)
		pool[i] = &a
	}
	one := pool[0]

	allDistinct := mk(func(i int) *types.Address { var a types.Address; a[0], a[19] = byte(i), byte(i>>8); return &a })
	clustered16 := mk(func(i int) *types.Address { return pool[i%16] })
	single := mk(func(i int) *types.Address { return one })

	t.Logf("bytes/tx (raw, pre-zstd), N=%d:", n)
	t.Logf("  all-distinct recipients : %.2f  (%d B) [raw To column, 20B each]", float64(allDistinct)/n, allDistinct)
	t.Logf("  16-recipient pool       : %.2f  (%d B) [dict To column]", float64(clustered16)/n, clustered16)
	t.Logf("  single recipient        : %.2f  (%d B) [dict To column]", float64(single)/n, single)
}
