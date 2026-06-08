package bls

import (
	"crypto/rand"
	"testing"

	"github.com/n42blockchain/N42/crypto/bls/common"
)

// BenchmarkCommitteeBlock simulates one block's consensus work for a committee
// of size N: N members sign the block seal hash, the signatures are aggregated,
// the public keys are aggregated, and the aggregate is verified once.
func benchCommittee(b *testing.B, n int) {
	sks := make([]common.SecretKey, n)
	pks := make([]common.PublicKey, n)
	for i := 0; i < n; i++ {
		sk, err := RandKey()
		if err != nil {
			b.Fatal(err)
		}
		sks[i] = sk
		pks[i] = sk.PublicKey()
	}

	var msg [32]byte
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Unique message per "block".
		_, _ = rand.Read(msg[:])
		sigs := make([]common.Signature, n)
		for j := 0; j < n; j++ {
			sigs[j] = sks[j].Sign(msg[:])
		}
		aggSig := AggregateSignatures(sigs)
		aggPk := AggregateMultiplePubkeys(pks)
		if !aggSig.FastAggregateVerify(pks, msg) {
			b.Fatal("verify failed")
		}
		_ = aggPk
	}
}

func BenchmarkCommitteeBlock512(b *testing.B) { benchCommittee(b, 512) }
func BenchmarkSign1(b *testing.B) {
	sk, _ := RandKey()
	var msg [32]byte
	_, _ = rand.Read(msg[:])
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sk.Sign(msg[:])
	}
}
