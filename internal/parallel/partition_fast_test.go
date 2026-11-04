package parallel

import (
	"fmt"
	"math/rand"
	"testing"
)

// samePartition fails unless a and b are the same partition, field for
// field (chains compared by key and members; their claim state is run
// state).
func samePartition(t *testing.T, name string, a, b *wavePartition) {
	t.Helper()
	if a.keys != b.keys || a.topChain != b.topChain {
		t.Fatalf("%s: keys/top %d/%d vs %d/%d", name, a.keys, a.topChain, b.keys, b.topChain)
	}
	if len(a.queues) != len(b.queues) {
		t.Fatalf("%s: %d vs %d queues", name, len(a.queues), len(b.queues))
	}
	for w := range a.queues {
		if (a.queues[w] == nil) != (b.queues[w] == nil) || len(a.queues[w]) != len(b.queues[w]) {
			t.Fatalf("%s: queue %d: len %d vs %d", name, w, len(a.queues[w]), len(b.queues[w]))
		}
		for p := range a.queues[w] {
			if a.queues[w][p] != b.queues[w][p] {
				t.Fatalf("%s: queue %d pos %d: %d vs %d", name, w, p, a.queues[w][p], b.queues[w][p])
			}
		}
	}
	if (a.chains == nil) != (b.chains == nil) || (a.chainAt == nil) != (b.chainAt == nil) {
		t.Fatalf("%s: steal chains present %v vs %v", name, a.chains != nil, b.chains != nil)
	}
	for w := range a.chains {
		if (a.chains[w] == nil) != (b.chains[w] == nil) || len(a.chains[w]) != len(b.chains[w]) {
			t.Fatalf("%s: worker %d: %d vs %d chains", name, w, len(a.chains[w]), len(b.chains[w]))
		}
		for r := range a.chains[w] {
			ca, cb := &a.chains[w][r], &b.chains[w][r]
			if ca.key != cb.key || len(ca.txs) != len(cb.txs) || cap(ca.txs) != cap(cb.txs) {
				t.Fatalf("%s: worker %d chain %d: key %x/%x len %d/%d", name, w, r, ca.key, cb.key, len(ca.txs), len(cb.txs))
			}
			for i := range ca.txs {
				if ca.txs[i] != cb.txs[i] {
					t.Fatalf("%s: worker %d chain %d tx %d: %d vs %d", name, w, r, i, ca.txs[i], cb.txs[i])
				}
			}
		}
		if (a.chainAt[w] == nil) != (b.chainAt[w] == nil) || len(a.chainAt[w]) != len(b.chainAt[w]) {
			t.Fatalf("%s: worker %d chainAt len %d vs %d", name, w, len(a.chainAt[w]), len(b.chainAt[w]))
		}
		for p := range a.chainAt[w] {
			if a.chainAt[w][p] != b.chainAt[w][p] {
				t.Fatalf("%s: worker %d chainAt %d: %d vs %d", name, w, p, a.chainAt[w][p], b.chainAt[w][p])
			}
		}
	}
}

// partitionKeys draws numTxs affinity keys over senders senders: uniform,
// Zipf-skewed, or one sender per transaction (index keys).
func partitionKeys(rng *rand.Rand, numTxs, senders int, dist string) []uint64 {
	keys := make([]uint64, numTxs)
	var z *rand.Zipf
	if dist == "zipf" {
		z = rand.NewZipf(rng, 1.3, 2, uint64(senders-1))
	}
	for i := range keys {
		switch dist {
		case "zipf":
			keys[i] = z.Uint64()*0x9e3779b97f4a7c15 + 1
		case "index":
			keys[i] = uint64(i)
		default:
			keys[i] = uint64(rng.Intn(senders))<<20 | 7
		}
	}
	return keys
}

// TestPartitionFastMatchesSerial: N42_WAVE_PARTITION_FAST's partition is
// the serial one, field for field, for every steal mode, worker count,
// key distribution, grouping shard count and index subset (later waves
// partition a subset).
func TestPartitionFastMatchesSerial(t *testing.T) {
	rng := rand.New(rand.NewSource(65))
	defer func() { partitionFastGoroutinesForTest = 0 }()
	for _, steal := range []bool{false, true} {
		withWaveModes(t, true, steal)
		for _, numTxs := range []int{1, 3, 50, 1000, 20000} {
			for _, senders := range []int{1, 7, 300, 5000} {
				for _, dist := range []string{"uniform", "zipf", "index"} {
					keys := partitionKeys(rng, numTxs, senders, dist)
					for _, subset := range []bool{false, true} {
						idx := allIndices(numTxs)
						if subset {
							var sub []int
							for i := 0; i < numTxs; i++ {
								if rng.Intn(3) == 0 {
									sub = append(sub, i)
								}
							}
							idx = sub
						}
						for _, workers := range []int{1, 5, 48} {
							want := partitionForSerial(idx, workers, keys)
							for _, shards := range []int{1, 3, 16} {
								partitionFastGoroutinesForTest = shards
								name := fmt.Sprintf("steal=%v n=%d senders=%d %s subset=%v workers=%d shards=%d",
									steal, numTxs, senders, dist, subset, workers, shards)
								samePartition(t, name, want, partitionForFast(idx, workers, keys, steal))
							}
						}
					}
				}
			}
		}
	}
}

// TestPartitionFastFlagDispatch: with the flag on, partitionFor takes the
// fast path only in LPT mode at partitionFastMin transactions or more, and
// returns the serial result either way; off, it is the serial code.
func TestPartitionFastFlagDispatch(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, lpt := range []bool{false, true} {
		withWaveModes(t, lpt, lpt)
		for _, numTxs := range []int{partitionFastMin - 1, partitionFastMin, 163000} {
			keys := partitionKeys(rng, numTxs, 8000, "zipf")
			idx := allIndices(numTxs)
			off := partitionForSerial(idx, 48, keys)
			restore := SetWavePartitionFastForTest(true)
			on := partitionFor(idx, 48, keys)
			restore()
			samePartition(t, fmt.Sprintf("lpt=%v n=%d", lpt, numTxs), off, on)
		}
	}
}

// TestPartitionFastExecutorMatches runs a conflicting workload with the
// flag off and on (prepared and per-wave partitions) and compares the
// final state and the per-worker queues.
func TestPartitionFastExecutorMatches(t *testing.T) {
	const numTxs, S, workers = 12000, 300, 8
	sender := func(i int) int { return (i * i) % S }
	run := func(fast bool) ([]uint64, [][]int) {
		withWaveModes(t, true, true)
		defer SetWavePartitionFastForTest(fast)()
		var ex *Executor
		ex = NewExecutor(numTxs, workers, func(i int, rw *ReadWriteSet) error {
			k := LocationKey{Address: benchAddr(sender(i)), Field: FieldBalance}
			var cur uint64
			if v, wtx, winc, found := ex.MVS().Read(k, i); found {
				rw.RecordReadValue(k, wtx, winc, false, v)
				cur = uint64(v[31]) | uint64(v[30])<<8 | uint64(v[29])<<16
			} else {
				rw.RecordReadValue(k, -1, 0, true, nil)
			}
			rw.RecordWrite(k, benchVal(cur+1))
			return nil
		})
		ex.SetAffinity(func(i int) uint64 { return uint64(sender(i)) })
		ex.PrepareAffinity()
		_, queues := ex.PreparedAffinityForTest()
		ex.Run()
		out := make([]uint64, S)
		for s := 0; s < S; s++ {
			v, _, _, _ := ex.MVS().Read(LocationKey{Address: benchAddr(s), Field: FieldBalance}, numTxs)
			if v == nil {
				continue
			}
			out[s] = uint64(v[31]) | uint64(v[30])<<8 | uint64(v[29])<<16
		}
		return out, queues
	}
	a, qa := run(false)
	b, qb := run(true)
	for s := range a {
		if a[s] != b[s] {
			t.Fatalf("sender %d: %d vs %d", s, a[s], b[s])
		}
	}
	if len(qa) != len(qb) {
		t.Fatalf("queues %d vs %d", len(qa), len(qb))
	}
	for w := range qa {
		if fmt.Sprint(qa[w]) != fmt.Sprint(qb[w]) {
			t.Fatalf("worker %d queue differs", w)
		}
	}
}

// BenchmarkPartition163k: the serial and the fast partition of a
// 163k-transfer block over 8000 senders on 48 workers (steal on).
func BenchmarkPartition163k(b *testing.B) {
	ol, os := waveLPT, waveSteal
	waveLPT, waveSteal = true, true
	defer func() { waveLPT, waveSteal = ol, os }()
	rng := rand.New(rand.NewSource(1))
	keys := partitionKeys(rng, 163000, 8000, "uniform")
	idx := allIndices(len(keys))
	b.Run("serial", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			partitionForSerial(idx, 48, keys)
		}
	})
	b.Run("fast", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			partitionForFast(idx, 48, keys, true)
		}
	})
}
