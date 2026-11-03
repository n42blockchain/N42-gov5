// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"math"
	"os"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/log"
)

// N42_AVAIL_ADMIT_GAS (default off): the availability check's gas-limit
// test reads the gas sum the frame resolver took per frame on its decode
// workers (frame_blocks.go) instead of a serial pass over the block's
// transactions. The serial pass is cache-miss bound (two dependent loads
// per transaction, the transaction and its inner data, both cold by the
// time the check runs): ~6-7 ms of the check's "result" phase at ~160k
// transactions. "1" uses the resolver's sum; "check" (or "2") computes both,
// logs any disagreement and decides with the serial pass.
//
// The decision is the serial pass's exactly: a saturating sum within the
// limit means every prefix is within it and nothing wrapped, so the serial
// pass passes too; a sum over the limit falls back to the serial pass (the
// refusal path, rare), which keeps its wrap-around semantics verbatim.
const (
	availAdmitGasOff = iota
	availAdmitGasOn
	availAdmitGasCheck
)

var availAdmitGasMode = parseAvailAdmitGas(os.Getenv("N42_AVAIL_ADMIT_GAS"))

func parseAvailAdmitGas(v string) int {
	switch v {
	case "1", "true", "on":
		return availAdmitGasOn
	case "2", "check":
		return availAdmitGasCheck
	}
	return availAdmitGasOff
}

// SetAvailAdmitGasForTest sets the N42_AVAIL_ADMIT_GAS mode and returns the
// restore.
func SetAvailAdmitGasForTest(mode int) func() {
	prev := availAdmitGasMode
	availAdmitGasMode = mode
	return func() { availAdmitGasMode = prev }
}

// availAdmitGasHits counts checks decided from the resolver's sum;
// availAdmitGasMismatches counts "check"-mode disagreements (must stay 0).
var availAdmitGasHits, availAdmitGasMismatches atomic.Uint64

func saturatingAdd(a, b uint64) uint64 {
	if s := a + b; s >= a {
		return s
	}
	return math.MaxUint64
}

// saturatingGasSum is the sum of txs' gas limits, capped at MaxUint64.
func saturatingGasSum(txs []*transaction.Transaction) uint64 {
	var gas uint64
	for _, t := range txs {
		gas = saturatingAdd(gas, t.Gas())
	}
	return gas
}

// serialGasWithin is the availability check's original gas test, verbatim:
// false as soon as the running (wrapping) sum exceeds limit.
func serialGasWithin(txs []*transaction.Transaction, limit uint64) bool {
	var gas uint64
	for _, t := range txs {
		gas += t.Gas()
		if gas > limit {
			return false
		}
	}
	return true
}

// availGasWithin reports whether body's transactions fit limit, with the
// serial test's result in every mode; fast says the resolver's sum decided.
func availGasWithin(body *block.Body, limit uint64) (within, fast bool) {
	switch availAdmitGasMode {
	case availAdmitGasOn:
		if g, ok := body.ResolvedGas(); ok && g <= limit {
			availAdmitGasHits.Add(1)
			return true, true
		}
	case availAdmitGasCheck:
		serial := serialGasWithin(body.Txs, limit)
		if g, ok := body.ResolvedGas(); ok {
			if (g <= limit && !serial) || g != saturatingGasSum(body.Txs) {
				availAdmitGasMismatches.Add(1)
				log.Error("availability: resolver gas sum disagrees with the serial pass", "resolved", g,
					"serial", saturatingGasSum(body.Txs), "limit", limit, "serialWithin", serial, "txs", len(body.Txs))
			}
		}
		return serial, false
	}
	return serialGasWithin(body.Txs, limit), false
}
