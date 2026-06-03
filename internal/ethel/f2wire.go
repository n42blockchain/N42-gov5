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
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/bodyf2"
	"github.com/n42blockchain/N42/internal/history"
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

// defaultF2HashIdx is the process-wide MPHF tx-hash -> (block,index) index that
// lets getTransactionByHash resolve F2 ledger txs (F1.5). nil = not configured.
var defaultF2HashIdx *history.MPHFReader

// SetF2HashIndex installs the process-wide F2 tx-hash index (call once at startup).
func SetF2HashIndex(r *history.MPHFReader) { defaultF2HashIdx = r }

// F2HashIndex returns the installed index (may be nil).
func F2HashIndex() *history.MPHFReader { return defaultF2HashIdx }

// defaultF2Hashes is the optional per-block canonical tx-hash sidecar that lets
// fullTx=false hash lists (and per-tx hashes in fullTx listings) be served. nil
// = not configured (those responses omit/skip hashes).
var defaultF2Hashes *bodyf2.HashReader

// SetF2Hashes installs the process-wide F2 tx-hash sidecar (call once at startup).
func SetF2Hashes(r *bodyf2.HashReader) { defaultF2Hashes = r }

// F2Hashes returns the installed tx-hash sidecar (may be nil).
func F2Hashes() *bodyf2.HashReader { return defaultF2Hashes }

// F2BlockHashes returns the canonical tx hashes for a block from the sidecar.
func F2BlockHashes(block uint64) ([][32]byte, error) {
	if defaultF2Hashes == nil {
		return nil, fmt.Errorf("ethel: no F2 tx-hash sidecar configured")
	}
	return defaultF2Hashes.BlockHashes(block)
}

// F2TxLocByHash resolves a tx hash to (block, index) via the MPHF index. The
// 4-byte fingerprint inside the index rejects out-of-set hashes, so a false ok
// is ~1/2^32. Returns ok=false when no index is configured or the hash is absent.
func F2TxLocByHash(h types.Hash) (block uint64, index uint64, ok bool) {
	if defaultF2HashIdx == nil {
		return 0, 0, false
	}
	blob, found, err := defaultF2HashIdx.Get(h[:])
	if err != nil || !found {
		return 0, 0, false
	}
	b, n := binary.Uvarint(blob)
	if n <= 0 {
		return 0, 0, false
	}
	idx, _ := binary.Uvarint(blob[n:])
	return b, idx, true
}
