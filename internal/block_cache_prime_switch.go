// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// N42_BLOCK_CACHE_PRIME: gate for priming bc.blockCache at the moment a block
// becomes canonical, with the *block.Block object already decoded in hand
// (docs/QS_BLOCK_TIME_BUDGET.md 6fc, docs/QS_QUEUE.md S64). Unset (default)
// = today's behaviour, byte-for-byte: no fill at commit, readers (sync
// writeBodiesRangeToStream, api getBlockByNumber, miner GetBlocksFromHash)
// re-decode from MDBX on a cache miss. N42_BLOCK_CACHE_BLOCKS (cache size)
// is unaffected by this switch.
package internal

import (
	"os"
	"sync"
)

var (
	blockCachePrimeOnce sync.Once
	blockCachePrimeOn   bool
)

// BlockCachePrimeEnabled reports whether a block object already decoded at
// the moment it becomes canonical should be used to prime bc.blockCache,
// instead of leaving the cache to fill lazily on the next GetBlock miss.
func BlockCachePrimeEnabled() bool {
	blockCachePrimeOnce.Do(func() {
		blockCachePrimeOn = parseBlockCachePrime(os.Getenv("N42_BLOCK_CACHE_PRIME"))
	})
	return blockCachePrimeOn
}

func parseBlockCachePrime(v string) bool {
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	default:
		return false
	}
}
