// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// N42_BLOCK_DECODE_REUSE_POOL: a follower's block-push receive path reuses
// pool-resident transaction objects (by hash) instead of RLP-decoding every
// transaction of a pushed block, when the pool already holds it, decoded,
// with its sender cached (docs/QS_BLOCK_TIME_BUDGET.md 6di/6dj: ~99.4% of a
// pushed block's own transactions, on this fleet's own shape). Off unless
// the variable is set -- unset/"0" decodes exactly as today.

package sync

import (
	"os"
	"sync"
)

var (
	blockDecodeReusePoolOnce sync.Once
	blockDecodeReusePoolOn   bool
)

// BlockDecodeReusePoolOn reports whether the block-push receive path should
// reuse pool-resident transaction objects instead of decoding every
// transaction fresh.
func BlockDecodeReusePoolOn() bool {
	blockDecodeReusePoolOnce.Do(func() {
		blockDecodeReusePoolOn = parseBlockDecodeReusePool(os.Getenv("N42_BLOCK_DECODE_REUSE_POOL"))
	})
	return blockDecodeReusePoolOn
}

func parseBlockDecodeReusePool(v string) bool {
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	default:
		return false
	}
}
