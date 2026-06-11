// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"lukechampine.com/blake3"
)

func shKey(i uint64) Hash {
	var s [8]byte
	for j := 0; j < 8; j++ {
		s[j] = byte(i >> (8 * j))
	}
	return Hash(blake3.Sum256(s[:]))
}

func shVal(i, r uint64) []byte {
	b := make([]byte, 32)
	for j := 0; j < 8; j++ {
		b[31-j] = byte((i*1000 + r) >> (8 * j))
	}
	return b
}

// TestShardedDeterminism: the same canonically-ordered batches produce the
// same world root regardless of HOW they are applied (sequential Set vs
// parallel ApplyBatch) — the cross-node agreement requirement.
func TestShardedDeterminism(t *testing.T) {
	a, _ := NewSharded(16)
	b, _ := NewSharded(16)

	var ops []Op
	for i := uint64(0); i < 20000; i++ {
		ops = append(ops, Op{KeyHash: shKey(i), Value: shVal(i, 0)})
	}
	// a: sequential routing; b: parallel batch.
	for _, op := range ops {
		a.Set(op.KeyHash, op.Value)
	}
	ra := a.Root()
	rb := b.ApplyBatch(ops)
	if ra != rb {
		t.Fatalf("sequential %x != parallel %x", ra[:8], rb[:8])
	}

	// Churn batches with deletes, applied in the same per-shard order.
	var churn []Op
	for i := uint64(0); i < 5000; i++ {
		churn = append(churn, Op{KeyHash: shKey(i * 3 % 20000), Value: shVal(i, 1)})
		if i%10 == 0 {
			churn = append(churn, Op{KeyHash: shKey(i * 7 % 20000)}) // delete
		}
	}
	for _, op := range churn {
		if len(op.Value) == 0 {
			a.Delete(op.KeyHash)
		} else {
			a.Set(op.KeyHash, op.Value)
		}
	}
	ra = a.Root()
	rb = b.ApplyBatch(churn)
	if ra != rb {
		t.Fatalf("churn: sequential %x != parallel %x", ra[:8], rb[:8])
	}
}

// TestShardedProofRoundTrip: proofs verify against the world root and reject
// tampering; absent keys yield no proof.
func TestShardedProofRoundTrip(t *testing.T) {
	tr, _ := NewSharded(16)
	var ops []Op
	for i := uint64(0); i < 8000; i++ {
		ops = append(ops, Op{KeyHash: shKey(i), Value: shVal(i, 0)})
	}
	root := tr.ApplyBatch(ops)

	for i := uint64(0); i < 8000; i += 97 {
		p, ok := tr.GetProof(shKey(i))
		if !ok {
			t.Fatalf("no proof for live key %d", i)
		}
		if !VerifyShardedProof(root, p) {
			t.Fatalf("proof for key %d does not verify", i)
		}
		// Tamper value.
		p.Inner.Value[0] ^= 0xff
		if VerifyShardedProof(root, p) {
			t.Fatalf("tampered proof for key %d accepted", i)
		}
	}
	var absent Hash
	absent[0] = 0xaa
	absent[31] = 0x55
	if _, ok := tr.GetProof(absent); ok {
		t.Fatal("proof produced for absent key")
	}
}

// TestShardedFlushLoadRoundTrip: persistence with per-shard key prefixes
// reproduces the exact world root on reload.
func TestShardedFlushLoadRoundTrip(t *testing.T) {
	tr, _ := NewSharded(4)
	var ops []Op
	for i := uint64(0); i < 6000; i++ {
		ops = append(ops, Op{KeyHash: shKey(i), Value: shVal(i, 0)})
	}
	root := tr.ApplyBatch(ops)

	store := newMapStore()
	if _, _, err := tr.FlushTo(store, nil); err != nil {
		t.Fatalf("flush: %v", err)
	}

	re, _ := NewSharded(4)
	if err := re.LoadFrom(store); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := re.Root(); got != root {
		t.Fatalf("reloaded root %x != %x", got[:8], root[:8])
	}
	// Reloaded tree keeps proving.
	p, ok := re.GetProof(shKey(123))
	if !ok || !VerifyShardedProof(root, p) {
		t.Fatal("reloaded proof failed")
	}
}

// TestShardedScaling reports the parallel speedup of ApplyBatch vs the
// single-tree baseline on an identical churn workload (informational; run
// with -v).
func TestShardedScaling(t *testing.T) {
	if testing.Short() {
		t.Skip("scaling measurement")
	}
	const keys = 1_000_000
	const blocks = 200
	const perBlock = 2000

	rng := uint64(0x243F6A8885A308D3)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }

	// Baseline: single tree.
	single := New()
	for i := uint64(0); i < keys; i++ {
		single.Set(shKey(i), shVal(i, 0))
	}
	single.Root()
	t0 := time.Now()
	for b := 0; b < blocks; b++ {
		for op := 0; op < perBlock; op++ {
			k := next() % keys
			single.Set(shKey(k), shVal(k, uint64(b)+1))
		}
		single.Root()
	}
	singleDur := time.Since(t0)

	for _, shards := range []int{4, 16, 64} {
		rng = 0x243F6A8885A308D3
		tr, _ := NewSharded(shards)
		var seed []Op
		for i := uint64(0); i < keys; i++ {
			seed = append(seed, Op{KeyHash: shKey(i), Value: shVal(i, 0)})
		}
		tr.ApplyBatch(seed)
		t1 := time.Now()
		ops := make([]Op, 0, perBlock)
		for b := 0; b < blocks; b++ {
			ops = ops[:0]
			for op := 0; op < perBlock; op++ {
				k := next() % keys
				ops = append(ops, Op{KeyHash: shKey(k), Value: shVal(k, uint64(b)+1)})
			}
			tr.ApplyBatch(ops)
		}
		dur := time.Since(t1)
		fmt.Printf("  [scaling] shards=%-3d  %d upd in %s = %.2fM upd/s  (single: %s = %.2fM upd/s, speedup %.1fx, cores=%d)\n",
			shards, blocks*perBlock, dur.Round(time.Millisecond),
			float64(blocks*perBlock)/dur.Seconds()/1e6,
			singleDur.Round(time.Millisecond),
			float64(blocks*perBlock)/singleDur.Seconds()/1e6,
			float64(singleDur)/float64(dur), runtime.GOMAXPROCS(0))
	}
}
