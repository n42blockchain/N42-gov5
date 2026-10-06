// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S39 (docs/QS_BLOCK_TIME_BUDGET.md 6dr/6ds): gated on the SAME
// N42_CONTENTION_DIAG switch S14 introduced (internal/consensus/hotstuff's
// own contentionDiagEnabled), internal/miner's and internal/sync's own
// copies: read independently here, one var per package, same pattern as
// every other shared-name switch in this campaign. Off by default -- the
// insertChain-side import-timing stamp this step adds is then never even
// called (no extra time.Now(), no behaviour change).

package internal

import "os"

var contentionDiagEnabled = os.Getenv("N42_CONTENTION_DIAG") == "1"
