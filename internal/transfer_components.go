package internal

import (
	"github.com/n42blockchain/N42/common/types"
)

// Endpoint components only. The caller must separately rule out contract,
// precompile, system-call, fee-account and other implicit state dependencies.
// Preserve transaction order within each component and first-seen group order.
func transferComponents(edges [][2]types.Address) [][]int {
	ids := make(map[types.Address]int)
	var parents, sizes []int
	id := func(a types.Address) int {
		if n, ok := ids[a]; ok {
			return n
		}
		n := len(parents)
		ids[a] = n
		parents = append(parents, n)
		sizes = append(sizes, 1)
		return n
	}
	root := func(n int) int {
		for parents[n] != n {
			parents[n] = parents[parents[n]]
			n = parents[n]
		}
		return n
	}
	for _, e := range edges {
		a, b := root(id(e[0])), root(id(e[1]))
		if a == b {
			continue
		}
		if sizes[a] < sizes[b] {
			a, b = b, a
		}
		parents[b] = a
		sizes[a] += sizes[b]
	}
	groups := make([][]int, 0)
	byRoot := make(map[int]int)
	for i, e := range edges {
		r := root(ids[e[0]])
		n, ok := byRoot[r]
		if !ok {
			n = len(groups)
			byRoot[r] = n
			groups = append(groups, nil)
		}
		groups[n] = append(groups[n], i)
	}
	return groups
}
