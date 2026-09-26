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

// BenchmarkSenderRecoveryFullPath measures signer.Sender's own full cost on
// a FRESH, never-cached legacy transfer (S59, docs/QS_BLOCK_TIME_BUDGET.md
// 6fa): EIP155Signer.Sender's own legacySigningHash (RLP encode + keccak)
// then recoverPlainRS (a 65-byte sig buffer alloc, crypto.Ecrecover --
// libsecp256k1 -- and a SECOND keccak to derive the address from the
// recovered pubkey). This calls signer.Sender directly, bypassing
// transaction.Sender's own cache wrapper entirely, so every b.N iteration
// pays a real recovery regardless of cache state.
func BenchmarkSenderRecoveryFullPath(b *testing.B) {
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

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := signer.Sender(tx); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSenderRecoveryRawEcrecover isolates the raw libsecp256k1 call
// alone (crypto.Ecrecover), excluding the signing-hash computation (RLP
// encode + keccak) and the address-derivation keccak that
// recoverPlainRS also pays -- the gap between this number and
// BenchmarkSenderRecoveryFullPath's own is exactly that extra hashing and
// allocation (S59's own PART 1 question).
func BenchmarkSenderRecoveryRawEcrecover(b *testing.B) {
	key, err := crypto.GenerateKey()
	if err != nil {
		b.Fatalf("key: %v", err)
	}
	hash := types.Hash{0x01, 0x02, 0x03, 0x04}
	sig, err := crypto.Sign(hash[:], key)
	if err != nil {
		b.Fatalf("sign: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := crypto.Ecrecover(hash[:], sig); err != nil {
			b.Fatal(err)
		}
	}
}
