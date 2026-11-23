// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/common/block"
)

func testPeerIDs(t *testing.T, n int) []peer.ID {
	t.Helper()
	out := make([]peer.ID, n)
	for i := range out {
		id, err := test.RandPeerID()
		if err != nil {
			t.Fatal(err)
		}
		out[i] = id
	}
	return out
}

func pipeBlocks(blocks []block.IBlock, seg uint64, bytes uint64) []pipeBlock {
	out := make([]pipeBlock, len(blocks))
	for i, b := range blocks {
		out[i] = pipeBlock{blk: b, bytes: bytes, seg: seg}
	}
	return out
}

// ---- reorder buffer ----

func TestReorderBufferOutOfOrderAndRuns(t *testing.T) {
	all := harnessBase(40)
	b := newReorderBuffer(64, 1<<30)
	// Slots arrive out of order: 21-30 (seg 2), then 11-20 (seg 1).
	b.admit(11, pipeBlocks(all[21:31], 2, 100))
	if run := b.take(11, 64); run != nil {
		t.Fatal("took a run with the cursor block missing")
	}
	b.admit(11, pipeBlocks(all[11:21], 1, 100))
	run := b.take(11, 64)
	if len(run) != 10 || run[0].blk.Number64().Uint64() != 11 || run[9].blk.Number64().Uint64() != 20 {
		t.Fatalf("first run = %d blocks", len(run))
	}
	// The run stops at the response boundary, then continues with seg 2.
	run = b.take(21, 4)
	if len(run) != 4 || run[3].blk.Number64().Uint64() != 24 {
		t.Fatalf("second run = %d blocks", len(run))
	}
	if b.len() != 6 || b.size() != 600 {
		t.Fatalf("left %d blocks / %d bytes", b.len(), b.size())
	}
	// A duplicate copy and blocks below the cursor are skipped.
	if n, _ := b.admit(25, pipeBlocks(all[20:31], 3, 100)); n != 0 {
		t.Fatalf("admitted %d duplicates", n)
	}
	if dropped := b.dropSeg(2); dropped != 6 || b.len() != 0 || b.size() != 0 {
		t.Fatalf("dropSeg removed %d, left %d / %d", dropped, b.len(), b.size())
	}
}

// Bounds in blocks and bytes; the importer's next block is always admitted.
func TestReorderBufferBoundsAndCursorAdmission(t *testing.T) {
	all := harnessBase(100)
	b := newReorderBuffer(8, 1<<30)
	n, full := b.admit(1, pipeBlocks(all[11:21], 1, 10))
	if n != 8 || !full || b.len() != 8 {
		t.Fatalf("block bound: admitted %d full %v", n, full)
	}
	// Full, but block 1 is what the importer waits for.
	if n, _ := b.admit(1, pipeBlocks(all[1:3], 2, 10)); n != 1 || !b.has(1) || b.has(2) {
		t.Fatalf("cursor admission: admitted %d", n)
	}
	b2 := newReorderBuffer(100, 250)
	n, full = b2.admit(1, pipeBlocks(all[11:21], 1, 100))
	if n != 2 || !full || b2.size() != 200 {
		t.Fatalf("byte bound: admitted %d (%d bytes) full %v", n, b2.size(), full)
	}
	b2.dropBelow(13)
	if b2.len() != 0 {
		t.Fatal("dropBelow kept consumed blocks")
	}
}

// ---- scheduler ----

func schedCfg() pipelineConfig {
	return pipelineConfig{slotBlocks: 64, slotBytes: 64 << 20, bufferBlocks: 256, bufferBytes: 2 << 30, peerInflight: 2}
}

func cands(ids []peer.ID, claim uint64) []pipeCandidate {
	out := make([]pipeCandidate, len(ids))
	for i, id := range ids {
		out[i] = pipeCandidate{pid: id, claim: claim, known: true}
	}
	return out
}

// Slots of the gap are spread over several peers, lowest first, one in
// flight per new peer, within the buffer window.
func TestPipeSchedSpreadsSlotsAcrossPeers(t *testing.T) {
	ids := testPeerIDs(t, 3)
	sc := newPipeScheduler(schedCfg())
	now := time.Now()
	buf := newReorderBuffer(256, 2<<30)
	reqs := sc.plan(now, 101, 1000, buf, cands(ids, 1000), 1000)
	if len(reqs) != 3 {
		t.Fatalf("planned %d requests, want 3 (one per peer)", len(reqs))
	}
	seen := map[peer.ID]bool{}
	for i, r := range reqs {
		if r.start != 101+uint64(i)*64 || r.count() != 64 || seen[r.pid] {
			t.Fatalf("request %d: %d-%d from a repeated peer %v", i, r.start, r.end, seen[r.pid])
		}
		seen[r.pid] = true
	}
	// A fast first reply raises that peer's limit; the rest of the window
	// (to cursor + 255 = 356) goes to it.
	sc.complete(reqs[0], true, 64, 64*1000, 5*time.Millisecond, 50*time.Millisecond, now)
	buf.admit(101, pipeBlocks(harnessBase(164)[101:], 1, 1000))
	more := sc.plan(now, 101, 1000, buf, cands(ids, 1000), 1000)
	if len(more) == 0 || more[0].pid != reqs[0].pid || more[0].start != 293 {
		t.Fatalf("next slot = %+v, want 293.. from the fast peer", more)
	}
	for _, r := range more {
		if r.end > 356 {
			t.Fatalf("request beyond the buffer window: %d-%d", r.start, r.end)
		}
	}
}

// A failed slot is re-planned to another peer; the failing peer is backed
// off for the run.
func TestPipeSchedReassignsFailedSlot(t *testing.T) {
	ids := testPeerIDs(t, 2)
	sc := newPipeScheduler(schedCfg())
	now := time.Now()
	buf := newReorderBuffer(256, 2<<30)
	reqs := sc.plan(now, 1, 64, buf, cands(ids, 64), 64)
	if len(reqs) != 1 {
		t.Fatalf("planned %d", len(reqs))
	}
	failed := reqs[0]
	sc.complete(failed, false, 0, 0, 0, time.Second, now)
	again := sc.plan(now, 1, 64, buf, cands(ids, 64), 64)
	if len(again) != 1 || again[0].pid == failed.pid || again[0].start != 1 {
		t.Fatalf("re-plan = %+v, want slot 1 on the other peer", again)
	}
	if st := sc.stats(failed.pid); !now.Before(st.until) || st.limit != 1 {
		t.Fatal("failed peer not backed off")
	}
}

// A slow peer's share shrinks: the lowest slot goes to the fastest peer
// and the slow one keeps a single request in flight.
func TestPipeSchedPrefersFastPeers(t *testing.T) {
	ids := testPeerIDs(t, 2)
	fast, slow := ids[0], ids[1]
	sc := newPipeScheduler(schedCfg())
	now := time.Now()
	for _, x := range []struct {
		pid  peer.ID
		took time.Duration
	}{{fast, 10 * time.Millisecond}, {slow, 2 * time.Second}} {
		r := sc.issue(x.pid, 1, 10, now, false)
		sc.complete(r, true, 10, 10<<20, x.took/4, x.took, now)
	}
	if sc.stats(slow).limit != 1 || sc.stats(fast).limit != 2 {
		t.Fatalf("limits fast %d slow %d", sc.stats(fast).limit, sc.stats(slow).limit)
	}
	reqs := sc.plan(now, 11, 2000, newReorderBuffer(256, 2<<30), cands(ids, 2000), 2000)
	if len(reqs) < 2 || reqs[0].pid != fast || reqs[1].pid != fast {
		t.Fatalf("the fast peer did not get the lowest slots: %+v", reqs)
	}
}

// Back-pressure in bytes: buffered plus reserved bytes stay in the budget,
// and slots are sized from the measured block size.
func TestPipeSchedBytesBackPressure(t *testing.T) {
	ids := testPeerIDs(t, 4)
	cfg := schedCfg()
	cfg.bufferBytes = 100 << 20
	cfg.slotBytes = 32 << 20
	sc := newPipeScheduler(cfg)
	sc.blockBytes = 10 << 20
	now := time.Now()
	buf := newReorderBuffer(256, cfg.bufferBytes)
	reqs := sc.plan(now, 1, 1000, buf, cands(ids, 1000), 1000)
	total := uint64(0)
	for _, r := range reqs {
		if r.count() > 3 {
			t.Fatalf("slot of %d blocks, want <= 3 (32 MiB / 10 MiB)", r.count())
		}
		total += r.count()
	}
	if total == 0 || total > 10 {
		t.Fatalf("reserved %d blocks, want 1..10 within 100 MiB", total)
	}
	// Buffered bytes count too: a full buffer stops new requests.
	sc2 := newPipeScheduler(cfg)
	sc2.blockBytes = 10 << 20
	buf2 := newReorderBuffer(256, cfg.bufferBytes)
	buf2.admit(5, pipeBlocks(harnessBase(20)[5:15], 1, 10<<20))
	if reqs := sc2.plan(now, 1, 1000, buf2, cands(ids, 1000), 1000); len(reqs) != 0 {
		t.Fatalf("planned %d requests over a full byte budget", len(reqs))
	}
	// An empty pipeline always fetches the next block.
	sc3 := newPipeScheduler(pipelineConfig{slotBlocks: 64, slotBytes: 1 << 20, bufferBlocks: 256, bufferBytes: 1 << 20, peerInflight: 1})
	sc3.blockBytes = 50 << 20
	if reqs := sc3.plan(now, 1, 1000, newReorderBuffer(256, 1<<20), cands(ids, 1000), 1000); len(reqs) != 1 || reqs[0].count() != 1 {
		t.Fatalf("empty pipeline with a tiny budget planned %+v", reqs)
	}
}

// The request holding up the importer gets one duplicate once it is
// overdue; the first copy to arrive wins (the buffer skips the second).
func TestPipeSchedHeadOfLineDuplicate(t *testing.T) {
	ids := testPeerIDs(t, 2)
	sc := newPipeScheduler(pipelineConfig{slotBlocks: 64, slotBytes: 64 << 20, bufferBlocks: 64, bufferBytes: 2 << 30, peerInflight: 2})
	now := time.Now()
	for _, id := range ids {
		r := sc.issue(id, 1000, 1000, now, false)
		sc.complete(r, true, 1, 1<<20, 10*time.Millisecond, 20*time.Millisecond, now)
	}
	buf := newReorderBuffer(64, 2<<30)
	reqs := sc.plan(now, 1, 64, buf, cands(ids, 64), 64)
	if len(reqs) != 1 {
		t.Fatalf("planned %d", len(reqs))
	}
	owner := reqs[0]
	if dup := sc.plan(now.Add(owner.expected), 1, 64, buf, cands(ids, 64), 64); len(dup) != 0 {
		t.Fatal("duplicated a request that is not overdue")
	}
	dup := sc.plan(now.Add(3*owner.expected+time.Millisecond), 1, 64, buf, cands(ids, 64), 64)
	if len(dup) != 1 || !dup[0].dup || dup[0].pid == owner.pid || dup[0].start != 1 || dup[0].end != owner.end {
		t.Fatalf("duplicate = %+v", dup)
	}
	if again := sc.plan(now.Add(time.Hour), 1, 64, buf, cands(ids, 64), 64); len(again) != 0 {
		t.Fatal("more than one duplicate")
	}
}

// Timeouts come from RTT and throughput within the stage-A bounds.
func TestPipeSchedTimeouts(t *testing.T) {
	sc := newPipeScheduler(schedCfg())
	first, whole, exp := sc.timeouts(&pipePeerStats{}, 1<<20)
	if first != pipeFirstChunkUnset || whole != catchUpTimeoutMax || exp != 0 {
		t.Fatalf("unknown peer: %v %v %v", first, whole, exp)
	}
	first, whole, _ = sc.timeouts(&pipePeerStats{samples: 1, rtt: time.Millisecond, tput: 1 << 30}, 1<<20)
	if first != pipeFirstChunkMin || whole != catchUpTimeoutMin {
		t.Fatalf("fast peer: %v %v", first, whole)
	}
	first, whole, _ = sc.timeouts(&pipePeerStats{samples: 1, rtt: time.Minute, tput: 1}, 1<<30)
	if first != pipeFirstChunkMax || whole != catchUpTimeoutMax {
		t.Fatalf("slow peer: %v %v", first, whole)
	}
}

// A peer whose reply ended short goes last for slots above what it served.
func TestPipeSchedShortReplyDeprioritised(t *testing.T) {
	ids := testPeerIDs(t, 2)
	sc := newPipeScheduler(schedCfg())
	now := time.Now()
	r := sc.issue(ids[0], 1, 64, now, false)
	sc.complete(r, true, 10, 10<<10, time.Millisecond, 2*time.Millisecond, now)
	got := sc.rank(cands(ids, 1000), 11, 1000, now)
	if got[0].pid != ids[1] {
		t.Fatal("a peer that just served only up to 10 ranked first for 11..")
	}
	if got := sc.rank(cands(ids, 1000), 5, 1000, now); got[0].pid != ids[0] {
		t.Fatal("short-reply penalty applied below what the peer served")
	}
}
