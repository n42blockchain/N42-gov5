// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package parallel

import (
	"os"
	"runtime"
	"sync"
)

// N42_WAVE_PARTITION_FAST=1 (r65a): the LPT wave partition and its steal
// chains are computed by partitionForFast instead of partitionByAffinity +
// buildChains. The result is identical, field for field.
//
// Why: under load the lag executor's child block is queued ~8 ms before its
// parent publishes the snapshot it runs on, and its preparation
// (ahead_prep.go: affinity keys + this partition, prepComputeUs p50 ~15 ms
// at 163k transfers) is still running when the ahead run claims it
// (prepWaitMs p50 8, p90 21; E1N26 run 29). The keys are computed on many
// goroutines; the partition was one goroutine walking two hash maps over
// every transaction (partitionByAffinity's grouping, then buildChains
// grouping each worker's queue again): ~8 ms of the ~15. A run that finds
// no usable preparation pays the same partition inside its setup
// (setupAff p90 ~13 ms).
//
// How (same output):
//   - the grouping is sharded by key over goroutines, each shard keeping
//     its keys' first position and count; the chains are then numbered in
//     first-appearance order, exactly as the serial grouping numbers them;
//   - the LPT order (stable by count, descending) and the greedy
//     assignment are the serial code, unchanged;
//   - every key lands on exactly one worker, so a worker's queue holds
//     whole chains and buildChains' per-worker grouping is the global
//     chain list restricted to that worker: its chains in LPT order (count
//     descending, ties by first appearance -- the same total order), each
//     chain's transactions in index order. No second map is needed;
//   - queues, chain lists and chain positions are filled per worker on
//     goroutines.
//
// Default off: partitionFor runs the serial code.
var wavePartitionFast = os.Getenv("N42_WAVE_PARTITION_FAST") == "1"

// WavePartitionFastOn reports N42_WAVE_PARTITION_FAST.
func WavePartitionFastOn() bool { return wavePartitionFast }

// SetWavePartitionFastForTest switches N42_WAVE_PARTITION_FAST and returns
// the restore.
func SetWavePartitionFastForTest(on bool) func() {
	prev := wavePartitionFast
	wavePartitionFast = on
	return func() { wavePartitionFast = prev }
}

// partitionFastMin is the smallest transaction count partitionForFast
// takes; below it the serial code is as fast.
const partitionFastMin = 8192

// partitionFastShardTxs is the transactions per grouping goroutine the
// sharding aims for.
const partitionFastShardTxs = 16384

// partitionFastMaxShards bounds the grouping goroutines.
const partitionFastMaxShards = 16 // <= 256 (shardOf is a uint8)

// partitionFastGoroutinesForTest, when positive, overrides the grouping
// goroutine count (tests: exercise one shard and many on small inputs).
var partitionFastGoroutinesForTest int

func partitionFastShards(n int) int {
	if partitionFastGoroutinesForTest > 0 {
		return partitionFastGoroutinesForTest
	}
	g := n / partitionFastShardTxs
	if p := runtime.GOMAXPROCS(0); g > p {
		g = p
	}
	if g > partitionFastMaxShards {
		g = partitionFastMaxShards
	}
	if g < 1 {
		g = 1
	}
	return g
}

// shardOfKey spreads affinity keys (a sender's first 8 bytes, or an index)
// over shards.
func shardOfKey(k uint64, shards int) int {
	k ^= k >> 33
	k *= 0xff51afd7ed558ccd
	k ^= k >> 33
	return int(k % uint64(shards))
}

// fastChain is one affinity key's chain during the fast partition.
type fastChain struct {
	key   uint64
	first int32 // position in txIndices of its first transaction
	count int32
	shard int32 // its grouping shard and index there
	li    int32
}

// partitionForFast is partitionFor in LPT mode (steal: with the steal
// chains), for txIndices ascending. Identical result.
func partitionForFast(txIndices []int, workers int, keys []uint64, steal bool) *wavePartition {
	n := len(txIndices)
	if n == 0 || workers < 1 {
		return partitionForSerial(txIndices, max(workers, 1), keys)
	}
	shards := partitionFastShards(n)

	// 1. Grouping, sharded by key: chainOf[j] is first the shard-local
	// chain index of txIndices[j], then (step 2) the global one.
	chainOf := make([]int32, n)
	local := make([][]fastChain, shards)
	// shardOf[j]: the shard of txIndices[j]'s key, hashed once.
	var shardOf []uint8
	if shards > 1 {
		shardOf = make([]uint8, n)
		parallelChunks(n, shards, func(lo, hi int) {
			for j := lo; j < hi; j++ {
				shardOf[j] = uint8(shardOfKey(keys[txIndices[j]], shards))
			}
		})
	}
	var wg sync.WaitGroup
	group := func(s int) {
		pos := make(map[uint64]int32, n/shards/8+16)
		var cs []fastChain
		for j, idx := range txIndices {
			if shardOf != nil && shardOf[j] != uint8(s) {
				continue
			}
			k := keys[idx]
			i, ok := pos[k]
			if !ok {
				i = int32(len(cs))
				pos[k] = i
				cs = append(cs, fastChain{key: k, first: int32(j), shard: int32(s), li: i})
			}
			cs[i].count++
			chainOf[j] = i
		}
		local[s] = cs
	}
	if shards == 1 {
		group(0)
	} else {
		for s := 0; s < shards; s++ {
			wg.Add(1)
			go func(s int) {
				defer wg.Done()
				group(s)
			}(s)
		}
		wg.Wait()
	}

	// 2. Number the chains in first-appearance order (the serial
	// grouping's numbering).
	total := 0
	for _, cs := range local {
		total += len(cs)
	}
	var chains []fastChain
	if shards == 1 {
		chains = local[0]
	} else {
		// First positions are distinct and below n: a scan in position
		// order numbers the chains.
		at := make([]*fastChain, n)
		for _, cs := range local {
			for i := range cs {
				at[cs[i].first] = &cs[i]
			}
		}
		chains = make([]fastChain, 0, total)
		for _, c := range at {
			if c != nil {
				chains = append(chains, *c)
			}
		}
	}
	if shards > 1 {
		// Shard-local index -> global index.
		remap := make([][]int32, shards)
		for s, cs := range local {
			remap[s] = make([]int32, len(cs))
		}
		for gi := range chains {
			remap[chains[gi].shard][chains[gi].li] = int32(gi)
		}
		parallelChunks(n, shards, func(lo, hi int) {
			for j := lo; j < hi; j++ {
				chainOf[j] = remap[shardOf[j]][chainOf[j]]
			}
		})
	}

	// 3. LPT: partitionByAffinity's order and assignment, unchanged.
	// The stable sort by count, descending, as a counting sort (the order
	// is unique: count descending, ties by first appearance).
	order := stableOrderByCountDesc(chains)
	load := make([]int, workers)
	assigned := make([]int32, len(chains))
	topChain := 0
	for _, ci := range order {
		best := 0
		for w := 1; w < workers; w++ {
			if load[w] < load[best] {
				best = w
			}
		}
		cnt := int(chains[ci].count)
		load[best] += cnt
		assigned[ci] = int32(best)
		if cnt > topChain {
			topChain = cnt
		}
	}

	// 4. Each worker's chains in LPT order, and each chain's rank on its
	// worker (buildChains' order: count descending, ties by first
	// appearance -- order restricted to the worker).
	perWorker := make([][]int32, workers)
	rank := make([]int32, len(chains))
	for _, ci := range order {
		w := assigned[ci]
		rank[ci] = int32(len(perWorker[w]))
		perWorker[w] = append(perWorker[w], int32(ci))
	}

	// 5. Queues (and, with steal, chain positions): a counting pass per
	// chunk of positions, then each chunk writes its transactions at its
	// offsets, so every queue is in index order.
	chunks := max(shards, 1)
	per := (n + chunks - 1) / chunks
	chunks = (n + per - 1) / per
	cnt := make([][]int32, chunks)
	parallelChunks(chunks, chunks, func(lo, hi int) {
		for c := lo; c < hi; c++ {
			row := make([]int32, workers)
			for j := c * per; j < min((c+1)*per, n); j++ {
				row[assigned[chainOf[j]]]++
			}
			cnt[c] = row
		}
	})
	p := &wavePartition{queues: make([][]int, workers), keys: len(chains), topChain: topChain}
	for w := range p.queues {
		p.queues[w] = make([]int, load[w])
	}
	if steal {
		p.chains = make([][]waveChain, workers)
		p.chainAt = make([][]int32, workers)
		for w := range p.chainAt {
			p.chainAt[w] = make([]int32, load[w])
		}
	}
	// cnt becomes each chunk's start offset per worker.
	for w := 0; w < workers; w++ {
		var off int32
		for c := 0; c < chunks; c++ {
			v := cnt[c][w]
			cnt[c][w] = off
			off += v
		}
	}
	parallelChunks(chunks, chunks, func(lo, hi int) {
		for c := lo; c < hi; c++ {
			row := cnt[c]
			for j := c * per; j < min((c+1)*per, n); j++ {
				ci := chainOf[j]
				w := assigned[ci]
				pos := row[w]
				row[w]++
				p.queues[w][pos] = txIndices[j]
				if steal {
					p.chainAt[w][pos] = rank[ci]
				}
			}
		}
	})
	if !steal {
		return p
	}

	// 6. Each worker's chains: buildChains' layout (one backing array per
	// worker, chains in rank order, members in index order).
	parallelChunks(workers, min(workers, chunks), func(lo, hi int) {
		for w := lo; w < hi; w++ {
			cs := perWorker[w]
			q, at := p.queues[w], p.chainAt[w]
			backing := make([]int, len(q))
			sorted := make([]waveChain, len(cs))
			fill := make([]int, len(cs))
			off := 0
			for r, ci := range cs {
				c := int(chains[ci].count)
				sorted[r].key = chains[ci].key
				sorted[r].txs = backing[off : off+c : off+c]
				off += c
			}
			for pos, idx := range q {
				r := at[pos]
				sorted[r].txs[fill[r]] = idx
				fill[r]++
			}
			p.chains[w] = sorted
		}
	})
	return p
}

// stableOrderByCountDesc returns the chain indices sorted by count,
// descending, ties in index order.
func stableOrderByCountDesc(chains []fastChain) []int {
	maxCount := int32(0)
	for i := range chains {
		maxCount = max(maxCount, chains[i].count)
	}
	// start[c]: the first slot of count c, counts laid out descending.
	start := make([]int, maxCount+2)
	for i := range chains {
		start[chains[i].count]++
	}
	off := 0
	for c := maxCount; c >= 0; c-- {
		v := start[c]
		start[c] = off
		off += v
	}
	order := make([]int, len(chains))
	for i := range chains {
		c := chains[i].count
		order[start[c]] = i
		start[c]++
	}
	return order
}

// parallelChunks runs fn over [0, n) split into at most g chunks, on
// goroutines when g > 1.
func parallelChunks(n, g int, fn func(lo, hi int)) {
	if g <= 1 || n <= 1 {
		fn(0, n)
		return
	}
	per := (n + g - 1) / g
	var wg sync.WaitGroup
	for lo := 0; lo < n; lo += per {
		hi := min(lo+per, n)
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			fn(lo, hi)
		}(lo, hi)
	}
	wg.Wait()
}
