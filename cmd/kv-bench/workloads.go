// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
)

// runWorkload dispatches to the named workload. Returns a Result with
// percentiles and memory stats already populated.
func runWorkload(name string, db kv.RwDB, keys uint64, valueSize int, commitEvery uint64, seed int64) (*Result, error) {
	switch name {
	case "random_write":
		return wlRandomWrite(db, keys, valueSize, commitEvery, seed)
	case "random_read_warm":
		return wlRandomReadWarm(db, keys, valueSize, seed)
	case "random_read_cold":
		return wlRandomReadCold(db, keys, valueSize, seed)
	case "range_scan":
		return wlRangeScan(db, keys, valueSize, seed)
	case "state_sim":
		return wlStateSim(db, keys, seed)
	case "mixed_rw":
		return wlMixedRW(db, keys, valueSize, seed)
	default:
		return nil, fmt.Errorf("unknown workload: %s", name)
	}
}

// genKey produces a deterministic 32-byte key from the (seed, i) pair
// using SHA-256. The output looks-random distribution stresses the
// B+tree like real address/storage keys would.
func genKey(seed int64, i uint64) []byte {
	var buf [16]byte
	binary.BigEndian.PutUint64(buf[0:8], uint64(seed))
	binary.BigEndian.PutUint64(buf[8:16], i)
	h := sha256.Sum256(buf[:])
	return h[:]
}

// genValue produces a deterministic byte slice of valueSize bytes.
func genValue(seed int64, i uint64, sz int) []byte {
	v := make([]byte, sz)
	rng := rand.New(rand.NewSource(seed ^ int64(i)))
	rng.Read(v)
	return v
}

// sampleLatency adds a latency to r if (i % stride == 0). Stride
// keeps total samples ≤ 100K regardless of workload size, capping
// memory at ~800 KB.
func sampleLatency(r *Result, i, stride uint64, dur time.Duration) {
	if stride > 0 && i%stride != 0 {
		return
	}
	r.LatNs = append(r.LatNs, dur.Nanoseconds())
}

func latencyStride(keys uint64) uint64 {
	stride := keys / 100_000
	if stride == 0 {
		stride = 1
	}
	return stride
}

// wlRandomWrite inserts `keys` (key, value) pairs with random keys.
// Commits every `commitEvery` (or once at end if 0). This is the
// stressing workload — random keys force B+tree splits.
func wlRandomWrite(db kv.RwDB, keys uint64, valueSize int, commitEvery uint64, seed int64) (*Result, error) {
	r := &Result{Op: "write"}
	stride := latencyStride(keys)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	t0 := time.Now()
	var inThisTx uint64
	for i := uint64(0); i < keys; i++ {
		k := genKey(seed, i)
		v := genValue(seed, i, valueSize)

		opStart := time.Now()
		if err := tx.Put(benchTable, k, v); err != nil {
			return nil, fmt.Errorf("Put @ i=%d: %w", i, err)
		}
		sampleLatency(r, i, stride, time.Since(opStart))

		inThisTx++
		if commitEvery > 0 && inThisTx >= commitEvery {
			if err := tx.Commit(); err != nil {
				return nil, fmt.Errorf("intermediate commit @ i=%d: %w", i, err)
			}
			tx, err = db.BeginRw(ctx)
			if err != nil {
				return nil, fmt.Errorf("reopen tx @ i=%d: %w", i, err)
			}
			inThisTx = 0
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("final commit: %w", err)
	}
	tx = nil

	r.Wall = time.Since(t0)
	r.Count = keys
	r.OpsPerSec = float64(keys) / r.Wall.Seconds()
	computePercentiles(r)
	snapshotMem(r)
	return r, nil
}

// wlRandomReadWarm first writes `keys` then reads them back in a
// shuffled order. The OS page cache and MDBX's internal cache stay
// warm, so this is the upper-bound read throughput.
func wlRandomReadWarm(db kv.RwDB, keys uint64, valueSize int, seed int64) (*Result, error) {
	if _, err := wlRandomWrite(db, keys, valueSize, 0, seed); err != nil {
		return nil, fmt.Errorf("warm-up write: %w", err)
	}

	r := &Result{Op: "read"}
	stride := latencyStride(keys)
	ctx := context.Background()

	tx, err := db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rng := rand.New(rand.NewSource(seed ^ 0xa5a5))
	order := rng.Perm(int(keys))

	t0 := time.Now()
	for off, i := range order {
		k := genKey(seed, uint64(i))

		opStart := time.Now()
		if _, err := tx.GetOne(benchTable, k); err != nil {
			return nil, fmt.Errorf("Get @ off=%d i=%d: %w", off, i, err)
		}
		sampleLatency(r, uint64(off), stride, time.Since(opStart))
	}
	r.Wall = time.Since(t0)
	r.Count = keys
	r.OpsPerSec = float64(keys) / r.Wall.Seconds()
	computePercentiles(r)
	snapshotMem(r)
	return r, nil
}

// wlRandomReadCold reads keys that were NEVER written. Every Get
// misses the B+tree and exercises the leaf-page-fault path. This is
// the upper-bound on cache-miss latency.
func wlRandomReadCold(db kv.RwDB, keys uint64, valueSize int, seed int64) (*Result, error) {
	// First seed half the keys so the tree has structure; then probe
	// for keys 2× larger than seeded — guaranteed misses.
	if _, err := wlRandomWrite(db, keys/2, valueSize, 0, seed); err != nil {
		return nil, fmt.Errorf("seed write: %w", err)
	}

	r := &Result{Op: "read"}
	stride := latencyStride(keys)
	ctx := context.Background()

	tx, err := db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	t0 := time.Now()
	for i := uint64(0); i < keys; i++ {
		// Use a different seed-base so keys don't collide with seeded ones.
		k := genKey(seed^0xdead_beef, i)

		opStart := time.Now()
		v, err := tx.GetOne(benchTable, k)
		if err != nil {
			return nil, fmt.Errorf("Get @ i=%d: %w", i, err)
		}
		_ = v // expected nil
		sampleLatency(r, i, stride, time.Since(opStart))
	}
	r.Wall = time.Since(t0)
	r.Count = keys
	r.OpsPerSec = float64(keys) / r.Wall.Seconds()
	computePercentiles(r)
	snapshotMem(r)
	return r, nil
}

// wlRangeScan writes `keys`, then scans the entire table with a
// cursor. Mirrors the state-root computation path.
func wlRangeScan(db kv.RwDB, keys uint64, valueSize int, seed int64) (*Result, error) {
	if _, err := wlRandomWrite(db, keys, valueSize, 0, seed); err != nil {
		return nil, fmt.Errorf("seed write: %w", err)
	}

	r := &Result{Op: "scan"}
	ctx := context.Background()

	tx, err := db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	c, err := tx.Cursor(benchTable)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	t0 := time.Now()
	var n uint64
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			return nil, err
		}
		n++
	}
	r.Wall = time.Since(t0)
	r.Count = n
	r.OpsPerSec = float64(n) / r.Wall.Seconds()
	snapshotMem(r)
	return r, nil
}

// wlStateSim simulates one ApplyTo commit interval with a sorted
// cursor.Append insert pattern. Mirrors the production bg-flusher
// closely; the result wall-time is a direct proxy for one bg-flush
// cost at the configured page size.
//
// Key composition: 32-byte synthetic key (matching account / hashed
// account / hashed storage 32-byte keys). value 70 B (acct) /
// 32 B (storage) — we use 50 B avg.
func wlStateSim(db kv.RwDB, keys uint64, seed int64) (*Result, error) {
	r := &Result{Op: "write"}
	stride := latencyStride(keys)
	ctx := context.Background()

	// Pre-generate sorted keys (matches sort+Append in production).
	preGen := time.Now()
	type kvPair struct {
		k []byte
		v []byte
	}
	pairs := make([]kvPair, 0, keys)
	for i := uint64(0); i < keys; i++ {
		pairs = append(pairs, kvPair{
			k: genKey(seed, i),
			v: genValue(seed, i, 50),
		})
	}
	// Sort like ApplyTo does.
	sortStart := time.Now()
	sort.Slice(pairs, func(i, j int) bool {
		return string(pairs[i].k) < string(pairs[j].k)
	})
	sortDur := time.Since(sortStart)
	preGenDur := time.Since(preGen)
	_ = preGenDur
	_ = sortDur

	tx, err := db.BeginRw(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	cur, err := tx.RwCursor(benchTable)
	if err != nil {
		return nil, err
	}

	t0 := time.Now() // measure ONLY the Append + commit, not the sort
	for i, p := range pairs {
		opStart := time.Now()
		if err := cur.Append(p.k, p.v); err != nil {
			cur.Close()
			return nil, fmt.Errorf("Append @ i=%d: %w", i, err)
		}
		sampleLatency(r, uint64(i), stride, time.Since(opStart))
	}
	cur.Close()

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	tx = nil

	r.Wall = time.Since(t0)
	r.Count = keys
	r.OpsPerSec = float64(keys) / r.Wall.Seconds()
	computePercentiles(r)
	snapshotMem(r)
	return r, nil
}

// wlMixedRW: 70% reads / 30% writes against a pre-seeded table.
// Mirrors EVM execution's read-heavy access pattern.
func wlMixedRW(db kv.RwDB, keys uint64, valueSize int, seed int64) (*Result, error) {
	if _, err := wlRandomWrite(db, keys, valueSize, 0, seed); err != nil {
		return nil, fmt.Errorf("seed write: %w", err)
	}

	r := &Result{Op: "mixed"}
	stride := latencyStride(keys)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			tx.Rollback()
		}
	}()

	rng := rand.New(rand.NewSource(seed ^ 0xfeedface))

	t0 := time.Now()
	for i := uint64(0); i < keys; i++ {
		k := genKey(seed, rng.Uint64()%keys)
		opStart := time.Now()
		if rng.Intn(10) < 7 {
			if _, err := tx.GetOne(benchTable, k); err != nil {
				return nil, fmt.Errorf("Get @ i=%d: %w", i, err)
			}
		} else {
			v := genValue(seed^0xbeef, i, valueSize)
			if err := tx.Put(benchTable, k, v); err != nil {
				return nil, fmt.Errorf("Put @ i=%d: %w", i, err)
			}
		}
		sampleLatency(r, i, stride, time.Since(opStart))
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	tx = nil

	r.Wall = time.Since(t0)
	r.Count = keys
	r.OpsPerSec = float64(keys) / r.Wall.Seconds()
	computePercentiles(r)
	snapshotMem(r)
	return r, nil
}
