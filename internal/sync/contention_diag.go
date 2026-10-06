// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S39 (docs/QS_BLOCK_TIME_BUDGET.md 6dr/6ds): gated on the SAME
// N42_CONTENTION_DIAG switch S14 introduced (internal/consensus/hotstuff's
// own contentionDiagEnabled) and internal/miner's own copy: read
// independently here, one var per package, same pattern as every other
// shared-name switch in this campaign. Off by default -- the import-timing
// stamps this step adds are then never even called (no extra time.Now(),
// no behaviour change).

package sync

import (
	"os"
	"time"
)

var contentionDiagEnabled = os.Getenv("N42_CONTENTION_DIAG") == "1"

// timeNowMs is time.Now().UnixMilli(), named for readability at each of
// this package's own stamp call sites.
func timeNowMs() int64 { return time.Now().UnixMilli() }
