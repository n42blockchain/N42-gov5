// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package transaction

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// signedTxs builds n distinct signed transactions plus their wire encodings, so
// a test can re-decode them into fresh objects the way block import does.
func signedTxs(t *testing.T, signer Signer, n int) (txs []*Transaction, raws [][]byte, from types.Address) {
	t.Helper()
	key, err := crypto.HexToECDSA("4c0883a69102937d6231471b5dbb6204fe5129617082797f9d2f2e8a6f5f7c2f")
	if err != nil {
		t.Fatal(err)
	}
	from = crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x1111111111111111111111111111111111111111")
	for i := 0; i < n; i++ {
		signed, err := SignTx(NewTx(&LegacyTx{
			Nonce:    uint64(i),
			GasPrice: uint256.NewInt(10),
			Gas:      21000,
			To:       &to,
			Value:    uint256.NewInt(2),
		}), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := EncodeEthereumTransaction(signed)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, signed)
		raws = append(raws, raw)
	}
	return txs, raws, from
}

// decodeAll re-decodes the wire bytes into fresh Transaction values — the state
// a block's transactions are in when import reaches them.
func decodeAll(t *testing.T, raws [][]byte) []*Transaction {
	t.Helper()
	out := make([]*Transaction, len(raws))
	for i, raw := range raws {
		tx, err := DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = tx
	}
	return out
}

// TestSenderCacheHitsOnFreshlyDecodedTx is the scenario the cache exists for:
// the pool recovers a transaction's sender, the block carrying it is decoded
// into new objects, and the second recovery must come from the cache with the
// same answer.
func TestSenderCacheHitsOnFreshlyDecodedTx(t *testing.T) {
	if senderCache == nil {
		t.Skip("sender cache disabled")
	}
	signer := NewLondonSigner(big.NewInt(1))
	_, raws, want := signedTxs(t, signer, 200)

	// Pass 1 — the "mempool": every recovery is real work and fills the cache.
	for _, tx := range decodeAll(t, raws) {
		got, err := Sender(signer, tx)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("pass 1: got %s want %s", got, want)
		}
	}

	// Pass 2 — "block import": fresh objects, so the per-tx memo cannot help.
	ResetSenderCacheStats()
	for _, tx := range decodeAll(t, raws) {
		got, err := Sender(signer, tx)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("pass 2: got %s want %s", got, want)
		}
	}
	hits, misses := SenderCacheStats()
	if hits != 200 || misses != 0 {
		t.Fatalf("pass 2 should be all hits: hits=%d misses=%d", hits, misses)
	}
}

// TestSenderCacheRejectsOtherSigner: the same bytes recover differently under
// different chain rules, so a cached entry must not be served to another signer.
func TestSenderCacheRejectsOtherSigner(t *testing.T) {
	if senderCache == nil {
		t.Skip("sender cache disabled")
	}
	signer1 := NewLondonSigner(big.NewInt(1))
	_, raws, want := signedTxs(t, signer1, 1)
	tx := decodeAll(t, raws)[0]
	if got, err := Sender(signer1, tx); err != nil || got != want {
		t.Fatalf("seed: got %s err %v", got, err)
	}

	signer2 := NewLondonSigner(big.NewInt(999))
	fresh := decodeAll(t, raws)[0]
	ResetSenderCacheStats()
	got, err := Sender(signer2, fresh)
	hits, _ := SenderCacheStats()
	if hits != 0 {
		t.Fatal("cached entry was served to a different signer")
	}
	// Under the wrong chain id the signature either fails to recover or yields
	// some other address — either is fine, silently returning `want` is not.
	if err == nil && got == want {
		t.Fatal("wrong signer produced the right sender from cache")
	}
}

// TestSenderCacheConcurrent hammers the cache from many goroutines. Run under
// -race: the entries are published through atomic pointers with no mutex, so
// this is where a wrong publication would show up.
func TestSenderCacheConcurrent(t *testing.T) {
	if senderCache == nil {
		t.Skip("sender cache disabled")
	}
	signer := NewLondonSigner(big.NewInt(1))
	_, raws, want := signedTxs(t, signer, 500)

	var wg sync.WaitGroup
	bad := make(chan string, 64)
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < 3; r++ {
				for _, tx := range decodeAll(t, raws) {
					got, err := Sender(signer, tx)
					if err != nil || got != want {
						bad <- "wrong sender"
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(bad)
	for msg := range bad {
		t.Fatal(msg)
	}
}

// TestSenderCacheSpeedup measures what the cache is worth on the import path.
func TestSenderCacheSpeedup(t *testing.T) {
	if senderCache == nil {
		t.Skip("sender cache disabled")
	}
	signer := NewLondonSigner(big.NewInt(1))
	_, raws, _ := signedTxs(t, signer, 2000)

	// Cold: nothing cached yet, so every recovery is real.
	for i := range senderCache {
		senderCache[i].Store(nil)
	}
	cold := decodeAll(t, raws)
	t0 := time.Now()
	for _, tx := range cold {
		if _, err := Sender(signer, tx); err != nil {
			t.Fatal(err)
		}
	}
	coldD := time.Since(t0)

	// Warm: same transactions, fresh objects, cache populated.
	warm := decodeAll(t, raws)
	t1 := time.Now()
	for _, tx := range warm {
		if _, err := Sender(signer, tx); err != nil {
			t.Fatal(err)
		}
	}
	warmD := time.Since(t1)

	t.Logf("cold %s (%s/tx), warm %s (%s/tx) → %.1fx",
		coldD.Truncate(time.Millisecond), (coldD / 2000).Truncate(time.Nanosecond),
		warmD.Truncate(time.Millisecond), (warmD / 2000).Truncate(time.Nanosecond),
		float64(coldD)/float64(warmD))
}
