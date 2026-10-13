package parallel

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// The pre-locality scheduler is retained as a behavioral oracle: queue
// ownership and tie order must remain identical for retries and arbitrary keys.
func referenceAffinityPartition(txIndices []int, workers int, affinity func(int) uint64, lpt bool) (queues [][]int, keys, topChain int) {
	queues = make([][]int, workers)
	if !lpt {
		for _, idx := range txIndices {
			w := int(affinity(idx) % uint64(workers))
			queues[w] = append(queues[w], idx)
		}
		return queues, 0, 0
	}
	type chain struct {
		key   uint64
		count int
	}
	pos := make(map[uint64]int)
	var chains []chain
	for _, idx := range txIndices {
		k := affinity(idx)
		i, ok := pos[k]
		if !ok {
			i = len(chains)
			pos[k] = i
			chains = append(chains, chain{key: k})
		}
		chains[i].count++
	}
	order := make([]int, len(chains))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return chains[order[a]].count > chains[order[b]].count })
	load := make([]int, workers)
	assigned := make(map[uint64]int, len(chains))
	for _, ci := range order {
		best := 0
		for w := 1; w < workers; w++ {
			if load[w] < load[best] {
				best = w
			}
		}
		load[best] += chains[ci].count
		assigned[chains[ci].key] = best
		if chains[ci].count > topChain {
			topChain = chains[ci].count
		}
	}
	for _, idx := range txIndices {
		w := assigned[affinity(idx)]
		queues[w] = append(queues[w], idx)
	}
	return queues, len(chains), topChain
}

func TestAffinityLocalityPreservesOriginalQueues(t *testing.T) {
	random := rand.New(rand.NewSource(42))
	for round := 0; round < 120; round++ {
		n := random.Intn(2000)
		indices := make([]int, 0, n)
		keyOf := make([]uint64, n)
		for i := range keyOf {
			// zero, recurring sender runs, interleaving and partial retry waves.
			if i > 0 && round%3 == 0 && random.Intn(8) != 0 {
				keyOf[i] = keyOf[i-1]
			} else {
				keyOf[i] = uint64(random.Intn(80)) * 7919
			}
			if random.Intn(4) != 0 {
				indices = append(indices, i)
			}
		}
		for _, workers := range []int{1, 2, 8, 32} {
			for _, lpt := range []bool{false, true} {
				key := func(i int) uint64 { return keyOf[i] }
				want, wkeys, wtop := referenceAffinityPartition(indices, workers, key, lpt)
				got, gkeys, gtop := partitionByAffinity(indices, workers, key, lpt)
				if !reflect.DeepEqual(got, want) || gkeys != wkeys || gtop != wtop {
					t.Fatalf("round=%d workers=%d lpt=%v: queues or metrics changed", round, workers, lpt)
				}
			}
		}
	}
}

func BenchmarkAffinityLocality(b *testing.B) {
	const n = 163000
	for _, shape := range []string{"sender-runs", "interleaved"} {
		indices := make([]int, n)
		keyOf := make([]uint64, n)
		for i := range indices {
			indices[i] = i
			if shape == "sender-runs" {
				keyOf[i] = uint64(i/20) * 7919
			} else {
				keyOf[i] = uint64(i%8000) * 7919
			}
		}
		key := func(i int) uint64 { return keyOf[i] }
		for _, impl := range []struct {
			name      string
			partition func([]int, int, func(int) uint64, bool) ([][]int, int, int)
		}{{"original", referenceAffinityPartition}, {"locality", partitionByAffinity}} {
			b.Run(shape+"/"+impl.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					q, _, _ := impl.partition(indices, 32, key, true)
					if len(q) != 32 {
						b.Fatal("worker queue count")
					}
				}
			})
		}
	}
}
