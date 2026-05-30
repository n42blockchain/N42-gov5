package stateless

import (
	"crypto/ecdsa"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func mkRoots(i byte) (types.Hash, types.Hash) {
	var sr, rr types.Hash
	sr[0], rr[0] = i, i+100
	return sr, rr
}

// TestAttestationSignRecover: a signed attestation recovers to its signer; a
// tampered field no longer recovers to the original signer.
func TestAttestationSignRecover(t *testing.T) {
	key, _ := crypto.GenerateKey()
	want := crypto.PubkeyToAddress(key.PublicKey)
	sr, rr := mkRoots(1)
	a, err := SignAttestation(key, 12345, sr, rr)
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Recover()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("recover %x != signer %x", got[:6], want[:6])
	}
	a.Number = 12346 // tamper → recovered addr (if any) must differ
	if bad, err := a.Recover(); err == nil && bad == want {
		t.Fatal("tampered number still recovers to signer")
	}
}

// TestAttestationPoolCount: distinct allowed signers accumulate; duplicate is
// not double-counted; threshold finalizes; non-allowed signer ignored.
func TestAttestationPoolCount(t *testing.T) {
	const N = 5
	keys := make([]*ecdsa.PrivateKey, N)
	allow := map[types.Address]bool{}
	for i := 0; i < N; i++ {
		k, _ := crypto.GenerateKey()
		keys[i] = k
		allow[crypto.PubkeyToAddress(k.PublicKey)] = true
	}
	pool := NewAttestationPool(allow)
	sr, rr := mkRoots(7)
	const num = uint64(999)

	for i := 0; i < N; i++ {
		a, err := SignAttestation(keys[i], num, sr, rr)
		if err != nil {
			t.Fatal(err)
		}
		if _, added, err := pool.Add(a); err != nil || !added {
			t.Fatalf("signer %d: added=%v err=%v", i, added, err)
		}
	}
	if c := pool.Count(num, sr, rr); c != N {
		t.Fatalf("count %d != %d", c, N)
	}

	dup, _ := SignAttestation(keys[0], num, sr, rr)
	if _, added, _ := pool.Add(dup); added {
		t.Fatal("duplicate signer counted twice")
	}
	if c := pool.Count(num, sr, rr); c != N {
		t.Fatalf("count changed after dup: %d", c)
	}
	if !pool.Finalized(num, sr, rr, N) {
		t.Fatal("should be finalized at N")
	}
	if pool.Finalized(num, sr, rr, N+1) {
		t.Fatal("should not be finalized at N+1")
	}

	outsider, _ := crypto.GenerateKey()
	oa, _ := SignAttestation(outsider, num, sr, rr)
	if _, added, _ := pool.Add(oa); added {
		t.Fatal("non-allowed signer counted")
	}
	if c := pool.Count(num, sr, rr); c != N {
		t.Fatalf("count changed after outsider: %d", c)
	}
}

// TestAttestationForkSplit: two groups attesting different roots for the same
// block are counted separately.
func TestAttestationForkSplit(t *testing.T) {
	pool := NewAttestationPool(nil) // accept any
	const num = uint64(42)
	srA, rrA := mkRoots(1)
	srB, rrB := mkRoots(2)
	for i := 0; i < 3; i++ {
		k, _ := crypto.GenerateKey()
		a, _ := SignAttestation(k, num, srA, rrA)
		pool.Add(a)
	}
	for i := 0; i < 2; i++ {
		k, _ := crypto.GenerateKey()
		b, _ := SignAttestation(k, num, srB, rrB)
		pool.Add(b)
	}
	if c := pool.Count(num, srA, rrA); c != 3 {
		t.Fatalf("group A count %d != 3", c)
	}
	if c := pool.Count(num, srB, rrB); c != 2 {
		t.Fatalf("group B count %d != 2", c)
	}
}
