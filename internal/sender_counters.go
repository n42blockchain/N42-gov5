// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"expvar"
	"sync/atomic"
)

// Sender-recovery source counters (OBSERVABILITY ONLY, always on): where
// block-time sender recovery comes from once admission has had its chance.
// They are cumulative since process start; the "blockimport phases" line
// carries them (senderSrcLogFields) and /debug/vars exposes them as
// "n42_sender_src".
var (
	// recoverTxsTxs: transactions recovered through transaction.Sender
	// inside senderJob.recoverTxs (an owned frame).
	senderRecoverTxsTxs atomic.Uint64
	// framesOwned: frames claimed at block resolution whose senders were
	// not complete (some transaction lacked a cached sender) at claim time.
	senderFramesOwned atomic.Uint64
	// framesNotStored: frames senderJob.frame handled through the
	// not-stored (!Own, no result, no wait) path.
	senderFramesNotStored atomic.Uint64
	// pickedUnreadyAfterGrace: frames the builder picked as ready only
	// because they were older than the ready grace, senders still missing.
	senderPickedUnreadyAfterGrace atomic.Uint64
	// localFillRecovered: transactions of a built block's frames that held
	// no cached sender when the fill decoded them, so the build recovers
	// them inline.
	senderLocalFillRecovered atomic.Uint64

	// recoverTxsLogged is the cumulative recoverTxsTxs at the previous log
	// line; the difference is the per-block figure.
	senderRecoverTxsLogged atomic.Uint64
)

// CountPickedUnreadyAfterGrace adds n frames picked past the ready grace
// with no cached senders (called by the block builder).
func CountPickedUnreadyAfterGrace(n int) {
	if n > 0 {
		senderPickedUnreadyAfterGrace.Add(uint64(n))
	}
}

// CountLocalFillRecovered adds n transactions the block builder will
// recover inline because their frame had no cached sender for them.
func CountLocalFillRecovered(n int) {
	if n > 0 {
		senderLocalFillRecovered.Add(uint64(n))
	}
}

// senderSrcLogFields returns the counters for the "blockimport phases"
// line; recoverTxsBlk is recoverTxsTxs since the previous line.
func senderSrcLogFields() []interface{} {
	cur := senderRecoverTxsTxs.Load()
	prev := senderRecoverTxsLogged.Swap(cur)
	return []interface{}{
		"recoverTxsBlk", cur - prev, "recoverTxsTxs", cur,
		"framesOwned", senderFramesOwned.Load(),
		"framesNotStored", senderFramesNotStored.Load(),
		"pickedUnreadyAfterGrace", senderPickedUnreadyAfterGrace.Load(),
		"localFillRecovered", senderLocalFillRecovered.Load(),
	}
}

func init() {
	expvar.Publish("n42_sender_src", expvar.Func(func() any {
		return map[string]uint64{
			"recoverTxsTxs":           senderRecoverTxsTxs.Load(),
			"framesOwned":             senderFramesOwned.Load(),
			"framesNotStored":         senderFramesNotStored.Load(),
			"pickedUnreadyAfterGrace": senderPickedUnreadyAfterGrace.Load(),
			"localFillRecovered":      senderLocalFillRecovered.Load(),
		}
	}))
}
