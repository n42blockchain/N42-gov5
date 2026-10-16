package parallel

import (
	"math/rand"
	"testing"
)

// partitionByAffinityRef is the pre-P13a implementation, kept to pin the
// optimized partition to identical queues.
func partitionByAffinityRef(txIndices []int, workers int, affinity func(int) uint64) (queues [][]int, keys, topChain int) {
	queues = make([][]int, workers)
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
	sortStable(order, func(a, b int) bool { return chains[a].count > chains[b].count })
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

func sortStable(order []int, less func(a, b int) bool) {
	// insertion-free stable merge via sort.SliceStable semantics.
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && less(order[j], order[j-1]); j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}
}

func TestPartitionMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for it := 0; it < 40; it++ {
		n := rng.Intn(3000) + 1
		nk := rng.Intn(200) + 1
		keyOf := make([]uint64, n)
		for i := range keyOf {
			keyOf[i] = uint64(rng.Intn(nk)*rng.Intn(nk+1)) + 1
		}
		var idxs []int
		for i := 0; i < n; i++ {
			if it%2 == 0 || rng.Intn(3) > 0 {
				idxs = append(idxs, i)
			}
		}
		workers := rng.Intn(33) + 1
		aff := func(i int) uint64 { return keyOf[i] }
		wq, wk, wt := partitionByAffinityRef(idxs, workers, aff)
		gq, gk, gt := partitionByAffinity(idxs, workers, aff, true)
		if wk != gk || wt != gt {
			t.Fatalf("it %d: keys/top %d/%d want %d/%d", it, gk, gt, wk, wt)
		}
		for w := range wq {
			if len(wq[w]) != len(gq[w]) {
				t.Fatalf("it %d worker %d: len %d want %d", it, w, len(gq[w]), len(wq[w]))
			}
			for j := range wq[w] {
				if wq[w][j] != gq[w][j] {
					t.Fatalf("it %d worker %d pos %d differs", it, w, j)
				}
			}
		}
	}
}

func benchPartitionKeys(n, nk int) []uint64 {
	rng := rand.New(rand.NewSource(1))
	k := make([]uint64, n)
	for i := range k {
		k[i] = rng.Uint64()%uint64(nk) + 1
	}
	return k
}

func BenchmarkPartitionLPT(b *testing.B) {
	keys := benchPartitionKeys(163000, 8000)
	idxs := make([]int, len(keys))
	for i := range idxs {
		idxs[i] = i
	}
	aff := func(i int) uint64 { return keys[i] }
	b.Run("ref", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			partitionByAffinityRef(idxs, 32, aff)
		}
	})
	b.Run("new", func(b *testing.B) {
		for n := 0; n < b.N; n++ {
			partitionByAffinity(idxs, 32, aff, true)
		}
	})
}
