// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package attest

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// EnvStamps (N42_FRAME_ATTEST_STAMPS=1, default off) times the stages of an
// attestation on both ends, for the "frame attestations" log line:
//
//	attester: admitted -> recovered (recover: the frame's age in the store
//	when this member's recovery reported it), recovered -> flushed (batch:
//	the wait in the outgoing batch), the batch's signing (sign), and
//	queued -> written per peer connection (push);
//	receiver: the frame's age in the local store when a batch carrying it
//	arrived (recv; entries for frames not admitted here are counted as
//	recvEarly), and the batch's verification and application (apply).
//
// Run 72: a gateway's held head frame waited 45-60 ms for attestations; the
// stages tell recovery queueing on the covering member, the batch timer and
// the transport apart. Timing only: nothing else changes.
const EnvStamps = "N42_FRAME_ATTEST_STAMPS"

var stampsOn = os.Getenv(EnvStamps) == "1"

// SetStampsForTest switches the stage stamps and returns the restore.
func SetStampsForTest(on bool) func() {
	prev := stampsOn
	stampsOn = on
	return func() { stampsOn = prev }
}

// stageBucketsMs are the upper bounds of a stage histogram's buckets; the
// last bucket is open.
var stageBucketsMs = [...]int64{1, 2, 5, 10, 20, 50, 100}

// stageHist is a lock-free latency histogram.
type stageHist struct {
	n, sumUs, maxUs atomic.Uint64
	b               [len(stageBucketsMs) + 1]atomic.Uint64
}

func (h *stageHist) observe(d time.Duration) {
	if d < 0 {
		d = 0
	}
	us := uint64(d.Microseconds())
	h.n.Add(1)
	h.sumUs.Add(us)
	for {
		m := h.maxUs.Load()
		if us <= m || h.maxUs.CompareAndSwap(m, us) {
			break
		}
	}
	i := 0
	for i < len(stageBucketsMs) && d > time.Duration(stageBucketsMs[i])*time.Millisecond {
		i++
	}
	h.b[i].Add(1)
}

// String is "n/mean/max ms [le1 le2 ... gt100]".
func (h *stageHist) String() string {
	n := h.n.Load()
	if n == 0 {
		return "0"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d/%.1f/%.1f[", n, float64(h.sumUs.Load())/float64(n)/1e3, float64(h.maxUs.Load())/1e3)
	for i := range h.b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%d", h.b[i].Load())
	}
	sb.WriteByte(']')
	return sb.String()
}

// The stage histograms (process-wide: one pool and one pusher per node).
var (
	stRecover, stBatch, stSign, stPush, stRecv, stApply stageHist
	stRecvEarly                                         atomic.Uint64
)

// StagesString renders the stage histograms (empty when the stamps are off).
func StagesString() string {
	if !stampsOn {
		return ""
	}
	return fmt.Sprintf("recover=%s batch=%s sign=%s push=%s recv=%s recvEarly=%d apply=%s (n/mean/max ms [<=1 2 5 10 20 50 100 >100])",
		stRecover.String(), stBatch.String(), stSign.String(), stPush.String(), stRecv.String(), stRecvEarly.Load(), stApply.String())
}
