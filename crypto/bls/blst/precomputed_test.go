package blst

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/crypto/bls/common"
)

// TestPrecomputedHashByteIdentical is the safety gate for the BLS re-seal
// hash-once optimization: signing via a shared precomputed G2 hash point MUST be
// byte-for-byte identical to calling sk.Sign(msg) individually. If this ever
// diverges, a chain re-sealed with the optimized path would not match one sealed
// the original way, so resume/continuation would break.
func TestPrecomputedHashByteIdentical(t *testing.T) {
	const n = 64
	msgs := [][]byte{
		[]byte(""),
		[]byte("n42-bls-reseal"),
		bytes.Repeat([]byte{0xab}, 40), // SigningMessage is 40 bytes (view||hash)
	}
	for mi, msg := range msgs {
		precomp := PrecomputeHash(msg)
		for i := 0; i < n; i++ {
			var ikm [32]byte
			binary.BigEndian.PutUint64(ikm[24:], uint64(i*7919+1))
			ikm[0] = byte(i) // ensure varied, valid-range scalars
			sk, err := SecretKeyFromBytes(deriveValidScalar(i))
			if err != nil {
				t.Fatalf("msg %d key %d: %v", mi, i, err)
			}
			want := sk.Sign(msg).Marshal()
			got := precomp.SignWith(sk).Marshal()
			if !bytes.Equal(want, got) {
				t.Fatalf("msg %d key %d: precomputed sign != sk.Sign\n  want=%x\n  got =%x", mi, i, want, got)
			}
		}
	}
}

// deriveValidScalar produces a deterministic non-zero 32-byte big-endian scalar
// below the curve order (high bytes zeroed keeps it in range).
func deriveValidScalar(i int) []byte {
	b := make([]byte, 32)
	binary.BigEndian.PutUint64(b[24:], uint64(i)*0x9E3779B97F4A7C15+1)
	b[8] = byte(i + 1) // add entropy in a safe (low) region, keep < curve order
	return b
}

// TestPrecomputedHashAggregateIdentical confirms the aggregate over precomputed
// signatures equals the aggregate over individually-signed signatures (the value
// actually committed in ConsensusEvidence).
func TestPrecomputedHashAggregateIdentical(t *testing.T) {
	msg := bytes.Repeat([]byte{0x5c}, 40)
	precomp := PrecomputeHash(msg)
	var a, b []common.Signature
	for i := 0; i < 128; i++ {
		sk, err := SecretKeyFromBytes(deriveValidScalar(i + 1000))
		if err != nil {
			t.Fatal(err)
		}
		a = append(a, sk.Sign(msg))
		b = append(b, precomp.SignWith(sk))
	}
	aggA := AggregateSignatures(a).Marshal()
	aggB := AggregateSignatures(b).Marshal()
	if !bytes.Equal(aggA, aggB) {
		t.Fatalf("aggregate mismatch:\n  individual=%x\n  precomputed=%x", aggA, aggB)
	}
}

// TestAggregateSignWithEquivalence: the scalar-sum fast path must produce a
// BYTE-IDENTICAL aggregate to aggregating individual signatures, across
// committee sizes including the production 512. This is the correctness gate
// for replacing 512 G2 multiplications per block with one.
func TestAggregateSignWithEquivalence(t *testing.T) {
	msg := []byte("hotstuff-signing-message-for-aggregate-equivalence")
	for _, n := range []int{1, 2, 7, 64, 512} {
		sks := make([]common.SecretKey, n)
		for i := range sks {
			k, err := RandKey()
			if err != nil {
				t.Fatal(err)
			}
			sks[i] = k
		}
		precomp := PrecomputeHash(msg)

		// Reference: individual sk*H(m) signatures aggregated as points.
		sigs := make([]common.Signature, n)
		for i, sk := range sks {
			sigs[i] = precomp.SignWith(sk)
		}
		ref := AggregateSignatures(sigs)

		fast := precomp.AggregateSignWith(sks)
		if !bytes.Equal(ref.Marshal(), fast.Marshal()) {
			t.Fatalf("n=%d: scalar-sum aggregate diverged from point aggregate", n)
		}
		// And both must equal aggregating plain sk.Sign(msg).
		plain := make([]common.Signature, n)
		for i, sk := range sks {
			plain[i] = sk.Sign(msg)
		}
		if !bytes.Equal(AggregateSignatures(plain).Marshal(), fast.Marshal()) {
			t.Fatalf("n=%d: fast aggregate diverged from plain-sign aggregate", n)
		}
	}
}
