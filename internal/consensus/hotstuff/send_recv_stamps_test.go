// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S17 unit tests: sender-side (recordSendStamp/takeSendStamps) and
// receiver-side (recordVoteRx, PrepareQC dedup) aggregation.
// docs/QS_BLOCK_TIME_BUDGET.md 6cl.

package hotstuff

import (
	"strings"
	"testing"
	"time"
)

// TestRecordVoteRxFirstArrivalWithDuplicates checks that a second arrival
// from the SAME voter (the "gossip is always sent" duplicate path) is
// counted as a duplicate and does not overwrite the first arrival's stamps
// or double-count toward maxRx2Arr.
func TestRecordVoteRxFirstArrivalWithDuplicates(t *testing.T) {
	base := time.Now()
	var rx rxStamps
	var mask uint64
	var kth kthRxStamps

	// Voter 3's vote arrives via gossip first (rx2arr = 10ms), then a
	// duplicate arrives via rotor (rx2arr = 3ms -- faster, but must NOT
	// replace the first-arrival stamps).
	mt1 := msgTiming{arrive: base.Add(10 * time.Millisecond), rx: base, via: "gossip"}
	recordVoteRx(&rx, &mask, &kth, 3, mt1, false)
	mt2 := msgTiming{arrive: base.Add(3 * time.Millisecond), rx: base, via: "rotor"}
	recordVoteRx(&rx, &mask, &kth, 3, mt2, false)

	if rx.dupN != 1 {
		t.Fatalf("dupN = %d, want 1", rx.dupN)
	}
	if !kth.maxOK || kth.maxRx2Arr != 10*time.Millisecond {
		t.Fatalf("maxRx2Arr = %v (maxOK=%v), want 10ms from the FIRST arrival only", kth.maxRx2Arr, kth.maxOK)
	}
}

// TestRecordVoteRxKthVoterSelection checks that the k-th (quorum-completing)
// vote is the first one whose quorumReached=true, and that its own voter/
// via/rx2arr are recorded -- a later vote (even with quorumReached=true
// again, e.g. a stale re-check) must not overwrite it.
func TestRecordVoteRxKthVoterSelection(t *testing.T) {
	base := time.Now()
	var rx rxStamps
	var mask uint64
	var kth kthRxStamps

	// Voter 0 and 1 arrive before quorum; voter 2 completes it.
	recordVoteRx(&rx, &mask, &kth, 0, msgTiming{arrive: base.Add(time.Millisecond), rx: base, via: "gossip"}, false)
	recordVoteRx(&rx, &mask, &kth, 1, msgTiming{arrive: base.Add(2 * time.Millisecond), rx: base, via: "gossip"}, false)
	recordVoteRx(&rx, &mask, &kth, 2, msgTiming{arrive: base.Add(50 * time.Millisecond), rx: base, via: "rotor"}, true)
	// A later vote also reports quorumReached=true (e.g. redundant call) --
	// must not replace voter 2 as the k-th.
	recordVoteRx(&rx, &mask, &kth, 4, msgTiming{arrive: base.Add(90 * time.Millisecond), rx: base, via: "gossip"}, true)

	if !kth.ok {
		t.Fatal("kth.ok = false, want true")
	}
	if kth.voter != 2 {
		t.Fatalf("kth.voter = %d, want 2 (the first quorum-completing vote)", kth.voter)
	}
	if kth.via != "rotor" {
		t.Fatalf("kth.via = %q, want %q", kth.via, "rotor")
	}
	if kth.rx2Arr != 50*time.Millisecond {
		t.Fatalf("kth.rx2Arr = %v, want 50ms", kth.rx2Arr)
	}
	// max must reflect the SLOWEST of all four (deduped, non-quorum-gated) votes.
	if kth.maxRx2Arr != 90*time.Millisecond {
		t.Fatalf("kth.maxRx2Arr = %v, want 90ms (voter 4's, the slowest)", kth.maxRx2Arr)
	}
	if rx.dupN != 0 {
		t.Fatalf("dupN = %d, want 0 (four distinct voters)", rx.dupN)
	}
}

// TestRecordVoteRxClampsNegativeRx2Arr checks a non-monotonic (arrive
// before rx) pair is clamped to zero rather than going negative.
func TestRecordVoteRxClampsNegativeRx2Arr(t *testing.T) {
	base := time.Now()
	var rx rxStamps
	var mask uint64
	var kth kthRxStamps
	recordVoteRx(&rx, &mask, &kth, 0, msgTiming{arrive: base, rx: base.Add(5 * time.Millisecond), via: "gossip"}, true)
	if kth.rx2Arr != 0 {
		t.Fatalf("rx2Arr = %v, want 0 (clamped)", kth.rx2Arr)
	}
}

// TestPrepareQCDedupBothPaths checks processPrepareQC's inline dedup logic
// (mirrored here directly, since it touches e.mu-guarded engine state):
// the first arrival sets Via to its own transport; a duplicate via the
// OTHER transport flips Via to "both" and counts as a dup, without
// touching the original rx2arr/rxAt.
func TestPrepareQCDedupBothPaths(t *testing.T) {
	base := time.Now()
	var rx rxStamps

	// Simulate the exact logic in processPrepareQC's contentionDiagEnabled
	// block (duplicated here since that logic is inline, not its own
	// exported helper -- this test documents and locks in its behavior).
	recordPqc := func(mt msgTiming) {
		if !rx.seenPrepareQC {
			rx.seenPrepareQC = true
			rx.pqcRx2Arr = mt.arrive.Sub(mt.rx)
			if rx.pqcRx2Arr < 0 {
				rx.pqcRx2Arr = 0
			}
			rx.pqcRxAtMs = mt.rx.UnixMilli()
			rx.pqcVia = mt.via
			rx.pqcOK = true
		} else {
			rx.dupN++
			if rx.pqcVia != "" && rx.pqcVia != mt.via {
				rx.pqcVia = "both"
			}
		}
	}

	first := msgTiming{arrive: base.Add(4 * time.Millisecond), rx: base, via: "rotor"}
	recordPqc(first)
	if rx.pqcVia != "rotor" || rx.pqcRx2Arr != 4*time.Millisecond {
		t.Fatalf("after first arrival: via=%q rx2arr=%v, want rotor/4ms", rx.pqcVia, rx.pqcRx2Arr)
	}

	dup := msgTiming{arrive: base.Add(20 * time.Millisecond), rx: base.Add(15 * time.Millisecond), via: "gossip"}
	recordPqc(dup)
	if rx.pqcVia != "both" {
		t.Fatalf("after duplicate: via = %q, want %q", rx.pqcVia, "both")
	}
	if rx.pqcRx2Arr != 4*time.Millisecond {
		t.Fatalf("duplicate must not overwrite the first arrival's rx2arr: got %v, want 4ms", rx.pqcRx2Arr)
	}
	if rx.dupN != 1 {
		t.Fatalf("dupN = %d, want 1", rx.dupN)
	}
}

// TestRecordSendStampAndTake checks the sender-side round trip: record,
// then claim (take) once, with the entry gone afterward.
func TestRecordSendStampAndTake(t *testing.T) {
	e := &ConsensusEngine{}
	base := time.Now()
	emit := base
	deq := base.Add(2 * time.Millisecond)
	pub0 := base.Add(3 * time.Millisecond)
	pub1 := base.Add(20 * time.Millisecond)

	e.recordSendStamp(7, MsgPrepareQC, emit, deq, pub0, pub1, "rotor ok")

	rec := e.takeSendStamps(7)
	if rec == nil {
		t.Fatal("takeSendStamps(7) = nil, want a record")
	}
	if !rec.prepareQC.ok {
		t.Fatal("prepareQC.ok = false, want true")
	}
	if rec.prepareQC.emit2Deq != 2*time.Millisecond {
		t.Fatalf("emit2Deq = %v, want 2ms", rec.prepareQC.emit2Deq)
	}
	if rec.prepareQC.deq2Pub != time.Millisecond {
		t.Fatalf("deq2Pub = %v, want 1ms", rec.prepareQC.deq2Pub)
	}
	if rec.prepareQC.pubDur != 17*time.Millisecond {
		t.Fatalf("pubDur = %v, want 17ms", rec.prepareQC.pubDur)
	}
	if rec.prepareQC.path != "rotor ok" {
		t.Fatalf("path = %q, want %q", rec.prepareQC.path, "rotor ok")
	}
	if rec.prepareQC.pubAtMs != pub1.UnixMilli() {
		t.Fatalf("pubAtMs = %d, want %d", rec.prepareQC.pubAtMs, pub1.UnixMilli())
	}

	// A second take must find nothing -- claimed records are removed.
	if again := e.takeSendStamps(7); again != nil {
		t.Fatalf("takeSendStamps(7) after claim = %+v, want nil", again)
	}
}

// TestRecordSendStampNoEmitIsNoop checks that a zero t_emit (the diagnostic
// off, or a message this recordSendStamp variant should ignore) records
// nothing.
func TestRecordSendStampNoEmitIsNoop(t *testing.T) {
	e := &ConsensusEngine{}
	e.recordSendStamp(1, MsgVote, time.Time{}, time.Now(), time.Now(), time.Now(), "gossip only")
	if rec := e.takeSendStamps(1); rec != nil {
		t.Fatalf("takeSendStamps(1) = %+v, want nil (zero t_emit must be a no-op)", rec)
	}
}

// TestRecordSendStampPrunesOldestView checks that once sendByView exceeds
// its bound, the LOWEST-numbered (oldest) view is evicted first, so a view
// whose message never gets claimed cannot leak forever.
func TestRecordSendStampPrunesOldestView(t *testing.T) {
	e := &ConsensusEngine{}
	now := time.Now()
	for v := ViewNumber(1); v <= sendStampsMaxViews+5; v++ {
		e.recordSendStamp(v, MsgVote, now, now, now, now, "gossip only")
	}
	if len(e.sendByView) > sendStampsMaxViews {
		t.Fatalf("sendByView has %d entries, want at most %d", len(e.sendByView), sendStampsMaxViews)
	}
	// The earliest views should have been evicted.
	if rec := e.takeSendStamps(1); rec != nil {
		t.Fatal("view 1 should have been pruned as the oldest, but was still present")
	}
	// A recent view should still be present.
	if rec := e.takeSendStamps(sendStampsMaxViews + 5); rec == nil {
		t.Fatal("the most recently recorded view should still be present")
	}
}

// TestMsgSendPhasePathLabelling checks the phase-derivation path label
// passes through unchanged for all three documented values.
func TestMsgSendPhasePathLabelling(t *testing.T) {
	for _, path := range []string{"rotor ok", "rotor failed -> gossip", "gossip only"} {
		s := sendMsgStamp{ok: true, path: path}
		p := s.phase()
		if !p.OK {
			t.Fatalf("phase().OK = false for path %q, want true", path)
		}
		if p.Path != path {
			t.Fatalf("phase().Path = %q, want %q", p.Path, path)
		}
	}
	// Unset stamp derives an all-unmeasured phase.
	var unset sendMsgStamp
	if p := unset.phase(); p.OK {
		t.Fatalf("unset sendMsgStamp.phase() = %+v, want OK=false", p)
	}
}

// TestLogLineRendersSendRecvFields checks the new S17 fields show up in
// LogLine with the documented short names, and stay silent when unset.
func TestLogLineRendersSendRecvFields(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{View: 9, ViewStart: base, ProposalSent: tp(base, 0), PrepareQCFormed: tp(base, 500*time.Millisecond)}
	vt.Contention.send.prepareQC = sendMsgStamp{
		emit2Deq: time.Millisecond, deq2Pub: 2 * time.Millisecond, pubDur: 6 * time.Millisecond,
		pubAtMs: 1234567890123, path: "rotor ok", ok: true,
	}
	vt.Contention.rx.cv = kthRxStamps{
		rx2Arr: 300 * time.Millisecond, rxAtMs: 1234567890000, voter: 5, via: "gossip",
		maxRx2Arr: 320 * time.Millisecond, maxOK: true, ok: true,
	}
	vt.Contention.rx.dupN = 2

	line := vt.Phases().LogLine()
	for _, want := range []string{
		"pqcEmit2Deq=1ms", "pqcDeq2Pub=2ms", "pqcPubDur=6ms", `pqcPath="rotor ok"`, "pqcPubAt=1234567890123",
		"cvKthRx2Arr=300ms", "cvKthRxAt=1234567890000", "cvKthVoter=5", "cvKthVia=gossip", "cvMaxRx2Arr=320ms",
		"dupN=2",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("LogLine() = %q, missing %q", line, want)
		}
	}
}

// TestLogLineSilentWithoutSendRecvData checks an untouched view produces no
// S17 fields at all.
func TestLogLineSilentWithoutSendRecvData(t *testing.T) {
	base := time.Now()
	vt := ViewTiming{View: 1, ViewStart: base, ProposalSent: tp(base, 0), PrepareQCFormed: tp(base, 10*time.Millisecond)}
	line := vt.Phases().LogLine()
	for _, field := range []string{"pqcEmit2Deq", "cvKthRx2Arr", "prEmit2Deq", "pvKthRx2Arr", "dupN"} {
		if strings.Contains(line, field) {
			t.Fatalf("LogLine() = %q, unexpectedly contains %q", line, field)
		}
	}
}
