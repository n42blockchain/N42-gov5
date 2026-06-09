// Command n42-qmdb-bench compares the QMDB-twig store (lib/qmdb) against the JMT
// (lib/jmt) on a realistic block-by-block churn workload, reporting persistent
// footprint (all-history + after compaction), per-block root-compute time, and
// the parallel-vs-sequential twig-root speedup.
//
//	n42-qmdb-bench -keys 200000 -blocks 2000
package main

import (
	"flag"
	"fmt"
	"time"

	"lukechampine.com/blake3"

	"github.com/n42blockchain/N42/lib/jmt"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// countStore is a jmt.NodeStore that retains every node so we can total the
// "all-history" node bytes (matches docs/bench_state_report.md, no GC).
type countStore struct{ m map[jmt.Hash][]byte }

func newCountStore() *countStore { return &countStore{m: make(map[jmt.Hash][]byte)} }
func (c *countStore) Get(h jmt.Hash) ([]byte, error) {
	if d, ok := c.m[h]; ok {
		return d, nil
	}
	return nil, jmt.ErrNotFound
}
func (c *countStore) Put(h jmt.Hash, d []byte) error { c.m[h] = d; return nil }
func (c *countStore) Delete(jmt.Hash) error          { return nil }
func (c *countStore) Has(h jmt.Hash) (bool, error)   { _, ok := c.m[h]; return ok, nil }
func (c *countStore) bytes() int {
	n := 0
	for h, d := range c.m {
		n += len(h) + len(d)
	}
	return n
}

func qkey(i uint64) qmdb.Hash {
	var s [8]byte
	for j := 0; j < 8; j++ {
		s[j] = byte(i >> (8 * j))
	}
	return qmdb.Hash(blake3.Sum256(s[:]))
}
func jkeyFrom(q qmdb.Hash) jmt.Hash { return jmt.Hash(q) }
func valOf(i, r uint64) []byte {
	b := make([]byte, 32)
	for j := 0; j < 8; j++ {
		b[31-j] = byte((i*1000 + r) >> (8 * j))
	}
	return b
}

func main() {
	keys := flag.Uint64("keys", 200000, "live key count")
	blocks := flag.Int("blocks", 2000, "number of blocks (dirty sets)")
	perBlock := flag.Int("perblock", 200, "writes per block")
	flag.Parse()

	// Deterministic workload: each block overwrites `perblock` random keys, with
	// occasional deletes+reinserts, so old slots accumulate as obsolete (the case
	// compaction targets).
	rng := uint64(0x243F6A8885A308D3)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }

	q := qmdb.New()
	jtree := jmt.New(newCountStore())
	jStore := jtree.Store().(*countStore)

	var qTimePar, qTimeSeq, jTime time.Duration
	round := uint64(0)
	// seed all keys once
	{
		for i := uint64(0); i < *keys; i++ {
			q.Set(qkey(i), valOf(i, 0))
			_ = jtree.Put(jkeyFrom(qkey(i)), valOf(i, 0))
		}
		q.Root()
		jtree.SetVersion(0)
		_, _ = jtree.BatchUpdate(nil)
	}
	for b := 0; b < *blocks; b++ {
		round++
		var qEntries []qmdb.Hash
		var jEntries []jmt.BatchEntry
		for op := 0; op < *perBlock; op++ {
			k := next() % *keys
			v := valOf(k, round)
			q.Set(qkey(k), v)
			qEntries = append(qEntries, qkey(k))
			jEntries = append(jEntries, jmt.BatchEntry{KeyHash: jkeyFrom(qkey(k)), Value: v})
		}
		_ = qEntries
		// qmdb parallel root
		qmdb.ParallelRoot = true
		t0 := time.Now()
		q.Root()
		qTimePar += time.Since(t0)
		// jmt root
		t1 := time.Now()
		_, _ = jtree.BatchUpdate(jEntries)
		jtree.SetVersion(uint64(b + 1))
		jTime += time.Since(t1)
	}
	// Full-forest recompute (every twig dirty) — sequential vs parallel.
	qmdb.ParallelRoot = false
	q.ForceDirty()
	ts := time.Now()
	q.Root()
	qTimeSeq = time.Since(ts)
	qmdb.ParallelRoot = true
	q.ForceDirty()
	tp := time.Now()
	q.Root()
	qTimePar2 := time.Since(tp)

	// Footprint
	qEntryB, qTwigB := q.OnDiskBytes()
	if err := jtree.Flush(); err != nil {
		fmt.Println("jmt flush:", err)
	}
	jBytes := jStore.bytes()

	statBefore := q.Stats()
	pruned := q.Compact(0.5)
	statAfter := q.Stats()
	cEntryB, cTwigB := q.OnDiskBytes()

	fmt.Printf("=== n42-qmdb-bench: keys=%d blocks=%d perblock=%d ===\n", *keys, *blocks, *perBlock)
	fmt.Printf("\n-- persistent footprint (all-history) --\n")
	fmt.Printf("  QMDB:  entryLog=%.1f MB  twigMeta=%.1f MB  total=%.1f MB  (twigs=%d)\n",
		mb(qEntryB), mb(qTwigB), mb(qEntryB+qTwigB), statBefore.Twigs)
	fmt.Printf("  JMT:   nodes=%.1f MB  (count=%d)\n", mb(jBytes), len(jStore.m))
	fmt.Printf("\n-- after QMDB compaction (prune sparse twigs) --\n")
	fmt.Printf("  pruned twigs=%d  residentSlots: %d -> %d  (%.0f%% reclaimed)\n",
		pruned, statBefore.StoredSlots, statAfter.StoredSlots,
		100*float64(statBefore.StoredSlots-statAfter.StoredSlots)/float64(statBefore.StoredSlots))
	fmt.Printf("  QMDB footprint: %.1f MB -> %.1f MB\n", mb(qEntryB+qTwigB), mb(cEntryB+cTwigB))
	fmt.Printf("\n-- per-block root-compute time (avg over %d blocks) --\n", *blocks)
	fmt.Printf("  QMDB parallel: %v/block\n", (qTimePar / time.Duration(*blocks)).Round(time.Microsecond))
	fmt.Printf("  JMT:           %v/block\n", (jTime / time.Duration(*blocks)).Round(time.Microsecond))
	fmt.Printf("\n-- parallel vs sequential twig recompute (single heavy root) --\n")
	fmt.Printf("  sequential: %v   parallel: %v   speedup: %.1fx\n",
		qTimeSeq.Round(time.Microsecond), qTimePar2.Round(time.Microsecond),
		float64(qTimeSeq)/float64(qTimePar2))
}

func mb(b int) float64 { return float64(b) / (1024 * 1024) }
