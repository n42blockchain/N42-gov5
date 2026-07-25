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
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

var senderRecoveryChainID = big.NewInt(94)

// buildSignedTxs produces n transfer transactions, each from a distinct key,
// encoded and decoded through the wire format so that the resulting objects are
// byte-for-byte what an imported block carries: signature only, no sender.
func buildSignedTxs(t *testing.T, n int) ([]*transaction.Transaction, []types.Address) {
	t.Helper()

	signer := transaction.NewLondonSigner(senderRecoveryChainID)
	txs := make([]*transaction.Transaction, n)
	want := make([]types.Address, n)

	for i := 0; i < n; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			t.Fatalf("generate key %d: %v", i, err)
		}
		to := types.Address{byte(i), byte(i >> 8), 0xaa}
		chainID, _ := uint256.FromBig(senderRecoveryChainID)
		inner := &transaction.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     uint64(i),
			GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1_000_000_000),
			Gas:       21000,
			To:        &to,
			Value:     uint256.NewInt(uint64(i) + 1),
		}
		signed, err := transaction.SignNewTx(key, signer, inner)
		if err != nil {
			t.Fatalf("sign tx %d: %v", i, err)
		}

		// Round-trip through the wire encoding: this is what strips any sender
		// the signing path may have left behind, and is exactly what
		// Block.DecodeRLP does for a block received from a peer.
		enc, err := transaction.EncodeEthereumTransaction(signed)
		if err != nil {
			t.Fatalf("encode tx %d: %v", i, err)
		}
		decoded, err := transaction.DecodeEthereumTransaction(enc)
		if err != nil {
			t.Fatalf("decode tx %d: %v", i, err)
		}
		if decoded.From() != nil {
			t.Fatalf("tx %d: wire-decoded transaction unexpectedly carries a sender", i)
		}

		txs[i] = decoded
		want[i] = crypto.PubkeyToAddress(key.PublicKey)
	}
	return txs, want
}

// unrecoverableTx builds a well-formed transaction carrying a signature that
// can never recover: R = S = 0 is rejected outright by ValidateSignatureValues,
// so the failure is deterministic and does not depend on curve arithmetic. It
// goes through the same wire round-trip as the valid transactions, so it is
// indistinguishable from one that arrived over the network.
func unrecoverableTx(t *testing.T, nonce uint64) *transaction.Transaction {
	t.Helper()

	chainID, _ := uint256.FromBig(senderRecoveryChainID)
	to := types.Address{0xba, 0xd0}
	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(1_000_000_000),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(1),
		V:         new(uint256.Int),
		R:         new(uint256.Int),
		S:         new(uint256.Int),
	})

	enc, err := transaction.EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	bad, err := transaction.DecodeEthereumTransaction(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return bad
}

// TestRecoverBlockSendersMatchesSerial is the core equivalence check: after the
// parallel pre-pass, every transaction must report exactly the sender a plain
// serial transaction.Sender call would have produced.
func TestRecoverBlockSendersMatchesSerial(t *testing.T) {
	const n = 64
	signer := transaction.NewLondonSigner(senderRecoveryChainID)

	parallelTxs, want := buildSignedTxs(t, n)
	recoverBlockSenders(signer, parallelTxs)

	for i, tx := range parallelTxs {
		// Reading back through Sender exercises the memo, which is the whole
		// point: this must not re-derive, and must agree with the key.
		got, err := transaction.Sender(signer, tx)
		if err != nil {
			t.Fatalf("tx %d: Sender after parallel recovery: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("tx %d: sender mismatch: got %s want %s", i, got.Hex(), want[i].Hex())
		}
		// AsMessage is the path execution actually takes.
		msg, err := tx.AsMessage(signer, uint256.NewInt(7))
		if err != nil {
			t.Fatalf("tx %d: AsMessage: %v", i, err)
		}
		if msg.From() != want[i] {
			t.Fatalf("tx %d: AsMessage sender mismatch: got %s want %s", i, msg.From().Hex(), want[i].Hex())
		}
	}
}

// TestRecoverBlockSendersInvalidSignature pins the failure semantics: a block
// containing an unrecoverable transaction must behave identically with and
// without the pre-pass — the bad transaction still fails, at the same index,
// with the same error, and the good ones around it are unaffected.
func TestRecoverBlockSendersInvalidSignature(t *testing.T) {
	const n = 32
	const badIdx = 17
	signer := transaction.NewLondonSigner(senderRecoveryChainID)

	txs, want := buildSignedTxs(t, n)
	txs[badIdx] = unrecoverableTx(t, uint64(badIdx))

	// Baseline: what the serial path does today.
	serialErrs := make([]error, n)
	for i, tx := range txs {
		_, serialErrs[i] = transaction.Sender(signer, tx)
	}
	if serialErrs[badIdx] == nil {
		t.Fatalf("test setup: corrupted transaction still recovers")
	}

	// Rebuild from scratch so no memo carries over from the baseline pass.
	txs, want = buildSignedTxs(t, n)
	txs[badIdx] = unrecoverableTx(t, uint64(badIdx))

	recoverBlockSenders(signer, txs)

	for i, tx := range txs {
		got, err := transaction.Sender(signer, tx)
		if i == badIdx {
			if err == nil {
				t.Fatalf("tx %d: expected recovery failure to survive the pre-pass, got sender %s", i, got.Hex())
			}
			if err.Error() != serialErrs[i].Error() {
				t.Fatalf("tx %d: error changed: got %v want %v", i, err, serialErrs[i])
			}
			// The execution loop's own entry point must fail too.
			if _, err := tx.AsMessage(signer, uint256.NewInt(7)); err == nil {
				t.Fatalf("tx %d: AsMessage unexpectedly succeeded", i)
			}
			continue
		}
		if err != nil {
			t.Fatalf("tx %d: unexpected error next to a bad transaction: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("tx %d: sender mismatch: got %s want %s", i, got.Hex(), want[i].Hex())
		}
	}
}

// TestRecoverBlockSendersSkipsPresetSender guards the miner-local case: a block
// whose transactions already carry a sender (taken straight from the pool) must
// not have that sender overwritten or re-derived.
func TestRecoverBlockSendersSkipsPresetSender(t *testing.T) {
	const n = 16
	signer := transaction.NewLondonSigner(senderRecoveryChainID)

	txs, want := buildSignedTxs(t, n)
	preset := types.Address{0xde, 0xad, 0xbe, 0xef}
	txs[0].SetFrom(preset)

	recoverBlockSenders(signer, txs)

	msg, err := txs[0].AsMessage(signer, nil)
	if err != nil {
		t.Fatalf("AsMessage: %v", err)
	}
	if msg.From() != preset {
		t.Fatalf("preset sender was not honoured: got %s want %s", msg.From().Hex(), preset.Hex())
	}
	for i := 1; i < n; i++ {
		got, err := transaction.Sender(signer, txs[i])
		if err != nil {
			t.Fatalf("tx %d: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("tx %d: sender mismatch", i)
		}
	}
}

// TestRecoverBlockSendersSmallBlockNoop documents that blocks below the
// threshold are left untouched — the execution loop recovers them inline, as
// it always did.
func TestRecoverBlockSendersSmallBlockNoop(t *testing.T) {
	signer := transaction.NewLondonSigner(senderRecoveryChainID)
	txs, want := buildSignedTxs(t, senderRecoveryMinTxs-1)

	recoverBlockSenders(signer, txs)

	for i, tx := range txs {
		got, err := transaction.Sender(signer, tx)
		if err != nil {
			t.Fatalf("tx %d: %v", i, err)
		}
		if got != want[i] {
			t.Fatalf("tx %d: sender mismatch", i)
		}
	}
}

// TestRecoverBlockSendersSpeedup is the quantitative check. It measures the
// same workload serially and through the worker pool; with a real fan-out the
// parallel pass must come out meaningfully ahead. Reported either way so the
// number shows up in -v runs.
func TestRecoverBlockSendersSpeedup(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing check in -short mode")
	}
	const n = 4096
	signer := transaction.NewLondonSigner(senderRecoveryChainID)

	serialTxs, _ := buildSignedTxs(t, n)
	parallelTxs, _ := buildSignedTxs(t, n)

	start := time.Now()
	for _, tx := range serialTxs {
		if _, err := transaction.Sender(signer, tx); err != nil {
			t.Fatalf("serial recovery: %v", err)
		}
	}
	serial := time.Since(start)

	start = time.Now()
	recoverBlockSenders(signer, parallelTxs)
	parallel := time.Since(start)

	t.Logf("workers=%d txs=%d serial=%v (%v/tx) parallel=%v speedup=%.2fx",
		senderRecoveryWorkers, n, serial, serial/n, parallel, float64(serial)/float64(parallel))

	if senderRecoveryWorkers >= 2 && parallel >= serial {
		t.Fatalf("parallel recovery (%v) was not faster than serial (%v) with %d workers",
			parallel, serial, senderRecoveryWorkers)
	}
}

// countingSigner delegates to a real signer while counting how many times an
// actual derivation was performed. Equal is pointer identity, which is what
// transaction.Sender checks before it will trust a cache entry.
type countingSigner struct {
	transaction.Signer
	calls atomic.Int64
}

func (c *countingSigner) Sender(tx *transaction.Transaction) (types.Address, error) {
	c.calls.Add(1)
	return c.Signer.Sender(tx)
}

func (c *countingSigner) Equal(other transaction.Signer) bool {
	o, ok := other.(*countingSigner)
	return ok && o == c
}

// TestRecoverBlockSendersWarmsCache proves the mechanism rather than the wall
// clock: after the pre-pass, the serial execution loop must perform zero
// derivations, because every AsMessage hits the memo the pre-pass filled in.
func TestRecoverBlockSendersWarmsCache(t *testing.T) {
	const n = 64
	signer := &countingSigner{Signer: transaction.NewLondonSigner(senderRecoveryChainID)}
	txs, want := buildSignedTxs(t, n)

	recoverBlockSenders(signer, txs)
	if got := signer.calls.Load(); got != n {
		t.Fatalf("pre-pass derived %d senders, want %d", got, n)
	}

	// Everything from here on models the execution loop.
	signer.calls.Store(0)
	for i, tx := range txs {
		msg, err := tx.AsMessage(signer, uint256.NewInt(7))
		if err != nil {
			t.Fatalf("tx %d: AsMessage: %v", i, err)
		}
		if msg.From() != want[i] {
			t.Fatalf("tx %d: sender mismatch: got %s want %s", i, msg.From().Hex(), want[i].Hex())
		}
	}
	if got := signer.calls.Load(); got != 0 {
		t.Fatalf("execution loop still performed %d sender derivations; cache was not warmed", got)
	}
}
