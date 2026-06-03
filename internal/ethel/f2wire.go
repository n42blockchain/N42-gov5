// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// f2wire.go — node hook for serving ledger bodies from an F2 (no-signature)
// store. A node configured with an F2 store (--history.f2dir) can answer the
// LEDGER half of body RPCs (from/to/value/nonce/gas/input/accessList) without
// keeping full signed bodies. F2 cannot reproduce signatures or the canonical
// tx hash, so it does NOT feed witness-replay / execution (which use the
// full-tx GethBodyResult path) — it is a serving-side ledger reader only.

package ethel

import (
	"fmt"

	"github.com/n42blockchain/N42/internal/ethel/bodyf2"
)

// defaultF2Reader, if set at startup, is the process-wide F2 ledger reader
// consulted by F2LedgerBody. nil (default) = no F2 store configured.
var defaultF2Reader *bodyf2.Reader

// SetF2Reader installs the process-wide F2 ledger reader (call once at startup).
func SetF2Reader(r *bodyf2.Reader) { defaultF2Reader = r }

// F2Reader returns the installed F2 ledger reader (may be nil).
func F2Reader() *bodyf2.Reader { return defaultF2Reader }

// F2LedgerBody returns the ledger view (no signatures, no canonical hash) of a
// block body from the F2 store. Suitable for serving getBlockByNumber decoded
// txs, getTransactionByBlockNumberAndIndex content, etc. Returns an error when
// no F2 store is configured or the block's segment is absent (bodyf2.ErrF2Absent).
func F2LedgerBody(blockNum uint64) (*bodyf2.F2Block, error) {
	if defaultF2Reader == nil {
		return nil, fmt.Errorf("ethel: no F2 ledger store configured")
	}
	return defaultF2Reader.ReadBlock(blockNum)
}
