// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// N42_TX_INGEST_HIGH_WATER: a local admission gate on the RPC submit path,
// ported from n42-rs's own supply mechanism (docs/QS_BLOCK_TIME_BUDGET.md
// 6f0: each node there admits only while its pool is below a local
// high-water mark, 5/6 of the pool's slots, and keeps 98.4% occupancy with
// no generator-side throttle at all). gov5's own generators instead
// self-throttle on an ESTIMATED depth (cmd/txflood/main.go) and the pool
// silently discards whatever slips through anyway -- this gate makes the
// node itself the source of truth, cheaply, before any per-tx work is
// spent on a submission that would be rejected regardless.
//
// SendRawTransaction/BatchRawTransaction (internal/api/api_transaction.go)
// decode and ECDSA-recover the sender BEFORE the pool ever sees the
// transaction (S49 PART 1: seedRecoveredSender's own comment puts one
// recovery at ~50us; a batch of 200 pays that 200 times). When the pool is
// already at its 600k/200k caps that whole cost is spent only to have
// `pool.add` (internal/txspool/txs_pool.go:463-485) discard the result as
// ErrUnderpriced or ErrTxPoolOverflow. This gate checks the pool's own
// cheap Stats() (an RLock + summing per-account list lengths -- NOT
// Content(), which copies every transaction) BEFORE decode, and rejects
// the whole request with one distinct, cheap error when the fleet no
// longer needs it. Unset (0) disables the gate -- today's behaviour,
// unconditionally.
package api

import (
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/log"
)

var (
	txIngestHighWaterOnce sync.Once
	txIngestHighWaterMark uint64 // 0 = off (today)

	txIngestHighWaterLogGate int64 // unix seconds of the last log line, CAS-guarded
)

// errAboveHighWater is returned, without decoding the submission, when the
// pool's own pending+queued count has reached N42_TX_INGEST_HIGH_WATER.
var errAboveHighWater = errors.New("txpool: above high water")

// txIngestHighWater returns the configured admission ceiling, parsed once.
// 0 means the gate is off.
func txIngestHighWater() uint64 {
	txIngestHighWaterOnce.Do(func() {
		v := os.Getenv("N42_TX_INGEST_HIGH_WATER")
		if v == "" {
			return
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return
		}
		txIngestHighWaterMark = n
	})
	return txIngestHighWaterMark
}

// rejectAboveHighWater reports whether pool is at or above the configured
// high-water mark. The caller is expected to return errAboveHighWater
// immediately, before decoding anything, when this returns true.
func rejectAboveHighWater(pool common.ITxsPool) bool {
	return rejectAboveHighWaterMark(pool, txIngestHighWater())
}

// rejectAboveHighWaterMark is the pure decision behind rejectAboveHighWater,
// taking the mark as a parameter so it (and the logging gate) can be
// exercised in a test without going through the env-parsed, sync.Once
// singleton.
func rejectAboveHighWaterMark(pool common.ITxsPool, mark uint64) bool {
	if mark == 0 || pool == nil {
		return false
	}
	_, pending, _, queued := pool.Stats()
	if uint64(pending+queued) < mark {
		return false
	}
	logAboveHighWaterRateLimited(pending, queued, mark)
	return true
}

// logAboveHighWaterRateLimited logs at most once a minute across all
// rejecting goroutines -- a flat-out generator can hit this gate thousands
// of times a second once the pool is full.
func logAboveHighWaterRateLimited(pending, queued int, mark uint64) {
	now := time.Now().Unix()
	last := atomic.LoadInt64(&txIngestHighWaterLogGate)
	if now-last < 60 {
		return
	}
	if atomic.CompareAndSwapInt64(&txIngestHighWaterLogGate, last, now) {
		log.Warn("txpool: above high water, rejecting new submissions without decoding",
			"pending", pending, "queued", queued, "mark", mark)
	}
}
