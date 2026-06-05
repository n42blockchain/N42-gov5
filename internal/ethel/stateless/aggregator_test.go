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

package stateless

import (
	"crypto/ecdsa"
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

func mustAttest(t *testing.T, key *ecdsa.PrivateKey, n uint64, sr, rr types.Hash) *Attestation {
	t.Helper()
	a, err := SignAttestation(key, n, sr, rr)
	if err != nil {
		t.Fatalf("SignAttestation: %v", err)
	}
	return a
}

// TestAttestationWireRoundTrip: encode → decode preserves fields and the signer
// still recovers from the decoded form.
func TestAttestationWireRoundTrip(t *testing.T) {
	key, _ := crypto.GenerateKey()
	signer := crypto.PubkeyToAddress(key.PublicKey)
	sr := types.HexToHash("0x11")
	rr := types.HexToHash("0x22")
	a := mustAttest(t, key, 42, sr, rr)

	wire, err := EncodeAttestation(a)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if len(wire) != attestationWireSize {
		t.Fatalf("wire size %d, want %d", len(wire), attestationWireSize)
	}
	got, err := DecodeAttestation(wire)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Number != 42 || got.StateRoot != sr || got.ReceiptRoot != rr {
		t.Fatalf("decoded fields mismatch: %+v", got)
	}
	rec, err := got.Recover()
	if err != nil || rec != signer {
		t.Fatalf("recover = %x,%v want %x", rec, err, signer)
	}

	if _, err := DecodeAttestation(wire[:10]); err == nil {
		t.Fatal("decode accepted a short buffer")
	}
}

// TestAggregatorFinalizesAtThreshold: distinct signers accumulate; finalization
// fires at the threshold; a duplicate signer does not double-count.
func TestAggregatorFinalizesAtThreshold(t *testing.T) {
	agg := NewAggregator(nil, 3, 0)
	sr := types.HexToHash("0xaa")
	rr := types.HexToHash("0xbb")

	var keys []*ecdsa.PrivateKey
	for i := 0; i < 3; i++ {
		k, _ := crypto.GenerateKey()
		keys = append(keys, k)
	}

	for i, k := range keys {
		res, err := agg.Submit(mustAttest(t, k, 100, sr, rr))
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		if !res.Counted {
			t.Fatalf("submit %d: not counted", i)
		}
		wantFinal := i == 2
		if res.Finalized != wantFinal {
			t.Fatalf("submit %d: finalized=%v want %v (count=%d)", i, res.Finalized, wantFinal, res.Count)
		}
	}

	// Duplicate signer: not counted, count stays at 3.
	res, _ := agg.Submit(mustAttest(t, keys[0], 100, sr, rr))
	if res.Counted || res.Count != 3 {
		t.Fatalf("duplicate signer: counted=%v count=%d want false,3", res.Counted, res.Count)
	}
}

// TestAggregatorForkSplit: attestations claiming different roots for the same
// block are counted as separate groups, so a lying minority cannot ride the
// honest group's count.
func TestAggregatorForkSplit(t *testing.T) {
	agg := NewAggregator(nil, 2, 0)
	honest := types.HexToHash("0x0a")
	liar := types.HexToHash("0x0b")
	rr := types.HexToHash("0xcc")

	for i := 0; i < 2; i++ {
		k, _ := crypto.GenerateKey()
		agg.Submit(mustAttest(t, k, 7, honest, rr))
	}
	kLiar, _ := crypto.GenerateKey()
	agg.Submit(mustAttest(t, kLiar, 7, liar, rr))

	if c, fin := agg.Status(7, honest, rr); c != 2 || !fin {
		t.Fatalf("honest group: count=%d fin=%v want 2,true", c, fin)
	}
	if c, fin := agg.Status(7, liar, rr); c != 1 || fin {
		t.Fatalf("liar group: count=%d fin=%v want 1,false", c, fin)
	}
}

// TestAggregatorAllowlist: only allowlisted signers count.
func TestAggregatorAllowlist(t *testing.T) {
	good, _ := crypto.GenerateKey()
	bad, _ := crypto.GenerateKey()
	goodAddr := crypto.PubkeyToAddress(good.PublicKey)
	agg := NewAggregator(map[types.Address]bool{goodAddr: true}, 1, 0)
	sr := types.HexToHash("0x1")
	rr := types.HexToHash("0x2")

	if res, _ := agg.Submit(mustAttest(t, bad, 1, sr, rr)); res.Counted {
		t.Fatal("non-allowlisted signer was counted")
	}
	if res, _ := agg.Submit(mustAttest(t, good, 1, sr, rr)); !res.Counted || !res.Finalized {
		t.Fatalf("allowlisted signer: counted=%v finalized=%v", res.Counted, res.Finalized)
	}
}

// TestAggregatorRollingWindow: old blocks fall out of the retention window.
func TestAggregatorRollingWindow(t *testing.T) {
	agg := NewAggregator(nil, 1, 10) // keep most recent 10 blocks
	sr := types.HexToHash("0x9")
	rr := types.HexToHash("0x8")

	k, _ := crypto.GenerateKey()
	agg.Submit(mustAttest(t, k, 5, sr, rr))   // old
	agg.Submit(mustAttest(t, k, 100, sr, rr)) // advances highest → prunes < 90

	if c, _ := agg.Status(5, sr, rr); c != 0 {
		t.Fatalf("block 5 should be pruned, count=%d", c)
	}
	if c, _ := agg.Status(100, sr, rr); c != 1 {
		t.Fatalf("block 100 should remain, count=%d", c)
	}
}

// TestAggregatorConcurrentSubmit: many distinct signers submit concurrently
// (run under -race) and the final count is exact.
func TestAggregatorConcurrentSubmit(t *testing.T) {
	const n = 64
	agg := NewAggregator(nil, n, 0)
	sr := types.HexToHash("0xfeed")
	rr := types.HexToHash("0xbeef")

	keys := make([]*ecdsa.PrivateKey, n)
	for i := range keys {
		keys[i], _ = crypto.GenerateKey()
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(k *ecdsa.PrivateKey) {
			defer wg.Done()
			if _, err := agg.Submit(mustAttest(t, k, 9, sr, rr)); err != nil {
				t.Errorf("concurrent submit: %v", err)
			}
		}(keys[i])
	}
	wg.Wait()

	if c, fin := agg.Status(9, sr, rr); c != n || !fin {
		t.Fatalf("concurrent: count=%d fin=%v want %d,true", c, fin, n)
	}
}
