// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/big"
	"runtime"
	"testing"

	dcrsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/erigontech/secp256k1"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// S67b (docs/QS_BLOCK_TIME_BUDGET.md 6fd, docs/QS_QUEUE.md row S67) isolates
// where the ~72us/recovery (vs. libsecp256k1's own 40-50us single-threaded
// figure) goes: the cgo transition, context contention under concurrency, the
// allocations recoverPlainRS still pays, and whether a pure-Go alternative or
// a per-goroutine context changes the per-call cost at realistic validator
// thread counts (1, 8, 16).

func s67bSignedTx(b *testing.B) (*Transaction, Signer, types.Hash, []byte) {
	b.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		b.Fatalf("key: %v", err)
	}
	chainID := big.NewInt(94)
	signer := LatestSignerForChainID(chainID)
	to := types.Address{0xde, 0xad}
	tx, err := SignNewTx(key, signer, &LegacyTx{
		Nonce:    1,
		GasPrice: uint256.NewInt(10_000_000_000),
		Gas:      21000,
		To:       &to,
		Value:    uint256.NewInt(1),
	})
	if err != nil {
		b.Fatalf("sign: %v", err)
	}
	hash, err := signer.Hash(tx)
	if err != nil {
		b.Fatalf("hash: %v", err)
	}
	sig, err := crypto.Sign(hash[:], key)
	if err != nil {
		b.Fatalf("sign raw: %v", err)
	}
	return tx, signer, hash, sig
}

// ---- (a) crypto.Ecrecover alone ----

func s67bRunEcrecover(b *testing.B, procs int) {
	_, _, hash, sig := s67bSignedTx(b)
	runtime.GOMAXPROCS(procs)
	b.SetParallelism(procs)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := crypto.Ecrecover(hash[:], sig); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkS67bEcrecover_P1(b *testing.B)  { s67bRunEcrecover(b, 1) }
func BenchmarkS67bEcrecover_P8(b *testing.B)  { s67bRunEcrecover(b, 8) }
func BenchmarkS67bEcrecover_P16(b *testing.B) { s67bRunEcrecover(b, 16) }

// ---- (b) recoverPlainRS ----

func s67bRunRecoverPlainRS(b *testing.B, procs int) {
	tx, signer, hash, _ := s67bSignedTx(b)
	_ = signer
	// Mirrors EIP155Signer.Sender's own V un-mapping (transaction_signing.go
	// ~line 529): a Protected() legacy tx stores V as chainIdMul + 8 + recid,
	// so recoverPlainRS needs it put back into the historical 27/28 form.
	v, r, s := tx.RawSignatureValues()
	chainIdMul := new(big.Int).Mul(big.NewInt(94), big.NewInt(2))
	vb := new(big.Int).Sub(v.ToBig(), chainIdMul)
	vb.Sub(vb, big.NewInt(8))
	homestead := true
	runtime.GOMAXPROCS(procs)
	b.SetParallelism(procs)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := recoverPlainRS(hash, r, s, vb, homestead); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkS67bRecoverPlainRS_P1(b *testing.B)  { s67bRunRecoverPlainRS(b, 1) }
func BenchmarkS67bRecoverPlainRS_P8(b *testing.B)  { s67bRunRecoverPlainRS(b, 8) }
func BenchmarkS67bRecoverPlainRS_P16(b *testing.B) { s67bRunRecoverPlainRS(b, 16) }

// ---- (c) transaction.Sender end-to-end, cold sender cache per call ----
//
// A genuinely cold cache on every iteration would require a fresh
// Transaction (and fresh wire hash) per call, which would bury the recovery
// cost under allocation/signing cost for the setup itself. Instead each
// goroutine cycles through a small pool of distinct pre-signed transactions,
// calling signer.Sender directly (bypassing tx.from/senderCacheGet) so every
// call pays a real recovery -- matching what BenchmarkSenderRecoveryFullPath
// already does, but across GOMAXPROCS settings.

func s67bRunSenderColdCache(b *testing.B, procs int) {
	const poolSize = 64
	key, err := crypto.GenerateKey()
	if err != nil {
		b.Fatalf("key: %v", err)
	}
	chainID := big.NewInt(94)
	signer := LatestSignerForChainID(chainID)
	to := types.Address{0xde, 0xad}
	txs := make([]*Transaction, poolSize)
	for i := range txs {
		tx, err := SignNewTx(key, signer, &LegacyTx{
			Nonce:    uint64(i),
			GasPrice: uint256.NewInt(10_000_000_000),
			Gas:      21000,
			To:       &to,
			Value:    uint256.NewInt(1),
		})
		if err != nil {
			b.Fatalf("sign: %v", err)
		}
		txs[i] = tx
	}

	runtime.GOMAXPROCS(procs)
	b.SetParallelism(procs)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tx := txs[i%poolSize]
			i++
			if _, err := signer.Sender(tx); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkS67bSenderColdCache_P1(b *testing.B)  { s67bRunSenderColdCache(b, 1) }
func BenchmarkS67bSenderColdCache_P8(b *testing.B)  { s67bRunSenderColdCache(b, 8) }
func BenchmarkS67bSenderColdCache_P16(b *testing.B) { s67bRunSenderColdCache(b, 16) }

// ---- Keccak sighash cost alone, so it is not misattributed to recovery ----

func s67bRunKeccakSighash(b *testing.B, procs int) {
	tx, signer, _, _ := s67bSignedTx(b)
	runtime.GOMAXPROCS(procs)
	b.SetParallelism(procs)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = signer.Hash(tx)
		}
	})
}

func BenchmarkS67bKeccakSighash_P1(b *testing.B)  { s67bRunKeccakSighash(b, 1) }
func BenchmarkS67bKeccakSighash_P8(b *testing.B)  { s67bRunKeccakSighash(b, 8) }
func BenchmarkS67bKeccakSighash_P16(b *testing.B) { s67bRunKeccakSighash(b, 16) }

// ---- Alternative: erigon wrapper with a per-goroutine Context ----
//
// RecoverPubkeyWithContext takes an explicit *secp256k1.Context. DefaultContext
// is one process-wide context shared by every call; libsecp256k1's recover/verify
// path does not mutate context state (it is a read-only "sign+verify" context
// used here only for verify-shaped operations), so this measures whether giving
// each goroutine its own Context changes anything -- i.e. whether the cost at 8/16
// threads is contention on the context or just raw cgo-call/C-side cost replicated
// per core.

func s67bRunEcrecoverPerGoroutineContext(b *testing.B, procs int) {
	_, _, hash, sig := s67bSignedTx(b)
	runtime.GOMAXPROCS(procs)
	b.SetParallelism(procs)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := secp256k1.NewContext()
		for pb.Next() {
			if _, err := secp256k1.RecoverPubkeyWithContext(ctx, hash[:], sig, nil); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkS67bEcrecoverPerGoroutineContext_P1(b *testing.B) {
	s67bRunEcrecoverPerGoroutineContext(b, 1)
}
func BenchmarkS67bEcrecoverPerGoroutineContext_P8(b *testing.B) {
	s67bRunEcrecoverPerGoroutineContext(b, 8)
}
func BenchmarkS67bEcrecoverPerGoroutineContext_P16(b *testing.B) {
	s67bRunEcrecoverPerGoroutineContext(b, 16)
}

// ---- Alternative: erigon wrapper, reusing the pubkey output buffer ----
//
// RecoverPubkeyWithContext appends into pkbuf when it has spare capacity,
// so a goroutine-local 65-byte buffer removes that allocation entirely.

func s67bRunEcrecoverReuseBuf(b *testing.B, procs int) {
	_, _, hash, sig := s67bSignedTx(b)
	runtime.GOMAXPROCS(procs)
	b.SetParallelism(procs)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		buf := make([]byte, 0, 65)
		for pb.Next() {
			if _, err := secp256k1.RecoverPubkeyWithContext(secp256k1.DefaultContext, hash[:], sig, buf[:0]); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkS67bEcrecoverReuseBuf_P1(b *testing.B)  { s67bRunEcrecoverReuseBuf(b, 1) }
func BenchmarkS67bEcrecoverReuseBuf_P8(b *testing.B)  { s67bRunEcrecoverReuseBuf(b, 8) }
func BenchmarkS67bEcrecoverReuseBuf_P16(b *testing.B) { s67bRunEcrecoverReuseBuf(b, 16) }

// ---- Alternative: decred/dcrec pure-Go recovery ----

func s67bRunDcrecRecover(b *testing.B, procs int) {
	_, _, hash, sig := s67bSignedTx(b)
	// dcrec wants [sig[64]+27 || R || S], not [R||S||V].
	dsig := make([]byte, 65)
	dsig[0] = sig[64] + 27
	copy(dsig[1:], sig[:64])
	runtime.GOMAXPROCS(procs)
	b.SetParallelism(procs)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			pub, _, err := ecdsa.RecoverCompact(dsig, hash[:])
			if err != nil {
				b.Fatal(err)
			}
			_ = pub
		}
	})
}

func BenchmarkS67bDcrecRecover_P1(b *testing.B)  { s67bRunDcrecRecover(b, 1) }
func BenchmarkS67bDcrecRecover_P8(b *testing.B)  { s67bRunDcrecRecover(b, 8) }
func BenchmarkS67bDcrecRecover_P16(b *testing.B) { s67bRunDcrecRecover(b, 16) }

// TestS67bDcrecMatchesLibsecp256k1 sanity-checks the dcrec alternative
// actually recovers the same key as the production libsecp256k1 path, so the
// benchmark above is not silently measuring a broken call.
func TestS67bDcrecMatchesLibsecp256k1(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	hash := types.Hash{0x01, 0x02, 0x03, 0x04}
	sig, err := crypto.Sign(hash[:], key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	wantPub, err := crypto.Ecrecover(hash[:], sig)
	if err != nil {
		t.Fatalf("ecrecover: %v", err)
	}

	dsig := make([]byte, 65)
	dsig[0] = sig[64] + 27
	copy(dsig[1:], sig[:64])
	gotPub, _, err := ecdsa.RecoverCompact(dsig, hash[:])
	if err != nil {
		t.Fatalf("dcrec recover: %v", err)
	}
	gotUncompressed := gotPub.SerializeUncompressed()
	if len(wantPub) != len(gotUncompressed) {
		t.Fatalf("length mismatch: %d vs %d", len(wantPub), len(gotUncompressed))
	}
	for i := range wantPub {
		if wantPub[i] != gotUncompressed[i] {
			t.Fatalf("pubkey mismatch at byte %d: %x vs %x", i, wantPub, gotUncompressed)
		}
	}
	_ = dcrsecp.PubKeyBytesLenUncompressed
}
