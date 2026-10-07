package parallel

import "testing"

func TestPartitionByAffinity(t *testing.T) {
	cases := []struct {
		name    string
		workers int
		lens    []int
	}{
		{"skewed40", 32, skew(40)},
		{"few", 32, []int{5000, 4000, 3000}},
		{"one", 32, []int{100}},
		{"uniform", 8, []int{10, 10, 10, 10, 10, 10, 10, 10, 10}},
	}
	for _, tc := range cases {
		// Interleave chains so indices of one key are spread across the block.
		var keyOf []uint64
		remaining := append([]int(nil), tc.lens...)
		for left := len(tc.lens); left > 0; {
			left = 0
			for k := range remaining {
				if remaining[k] > 0 {
					keyOf = append(keyOf, uint64(k)*7919+3)
					remaining[k]--
					left++
				}
			}
		}
		aff := func(i int) uint64 { return keyOf[i] }
		idxs := make([]int, len(keyOf))
		for i := range idxs {
			idxs[i] = i
		}
		for _, lpt := range []bool{true, false} {
			q, keys, top := partitionByAffinity(idxs, tc.workers, aff, lpt)
			seen := make(map[int]bool)
			owner := make(map[uint64]int)
			maxQ := 0
			for w, qs := range q {
				if len(qs) > maxQ {
					maxQ = len(qs)
				}
				for j, idx := range qs {
					if seen[idx] {
						t.Fatalf("%s lpt=%v: idx %d twice", tc.name, lpt, idx)
					}
					seen[idx] = true
					if j > 0 && qs[j-1] >= idx {
						t.Fatalf("%s lpt=%v: queue %d not increasing", tc.name, lpt, w)
					}
					if o, ok := owner[aff(idx)]; ok && o != w {
						t.Fatalf("%s lpt=%v: key split across workers", tc.name, lpt)
					}
					owner[aff(idx)] = w
				}
			}
			if len(seen) != len(idxs) {
				t.Fatalf("%s lpt=%v: %d of %d indices placed", tc.name, lpt, len(seen), len(idxs))
			}
			if !lpt {
				for w, qs := range q {
					for _, idx := range qs {
						if int(aff(idx)%uint64(tc.workers)) != w {
							t.Fatalf("%s: modulo partition mismatch", tc.name)
						}
					}
				}
				continue
			}
			wantTop := 0
			for _, l := range tc.lens {
				if l > wantTop {
					wantTop = l
				}
			}
			if keys != len(tc.lens) || top != wantTop {
				t.Fatalf("%s: keys/top = %d/%d want %d/%d", tc.name, keys, top, len(tc.lens), wantTop)
			}
			avg := (len(idxs) + tc.workers - 1) / tc.workers
			bound := top
			if avg > bound {
				bound = avg
			}
			bound += top
			if maxQ > bound {
				t.Fatalf("%s: max queue %d > bound %d", tc.name, maxQ, bound)
			}
		}
	}
}

func skew(n int) []int {
	l := make([]int, n)
	for i := range l {
		l[i] = 4000/(i+1) + 1
	}
	return l
}
