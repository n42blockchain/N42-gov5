// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/syncanchor"
)

// Catch-up speed of the serial path and the pipeline in the same harness:
// the stage-A scenario (a validator of a 7-node network restarted at 100,
// six honest peers, one extra peer lying about its height; in enforce mode
// the liar also serves a forged chain without QCs).
//
// Scaled from the measured bench numbers (sync review: import 152 ms/block,
// about 36% of the serial wall outside import): execution 3 ms/block, a
// serving peer 10 ms to the first block plus 1.7 ms/block. The serial path
// fetches a whole range from one peer, then executes it; the pipeline
// overlaps both, so its bound is execution alone.
func TestPipelineCatchUpSpeedVsSerial(t *testing.T) {
	if testing.Short() {
		t.Skip("timing harness")
	}
	// The serial side as described above: whole ranges, no window prefetch
	// (with its windows the serial path overlaps the fetch too).
	t.Cleanup(setCatchUpWindowForTest(maxRequestBlocks))
	const (
		stopped = 100
		top     = 700
	)
	for _, mode := range []syncanchor.Mode{syncanchor.Observe, syncanchor.Enforce} {
		want := uint64(top)
		if mode == syncanchor.Enforce {
			want = top - 2 // the last two wait for the next anchor
		}
		rate := map[bool]float64{}
		for _, pipeline := range []bool{false, true} {
			all := anchorChain(top, allVerified)
			net, _ := newAnchorNet(t, mode, all[:stopped+1])
			restore := setPipelineForTest(pipeline)
			net.chain.insertDelay = 3 * time.Millisecond
			for i := 0; i < 6; i++ {
				p := net.addPeer(top, harnessHonest, all...)
				p.rtt = 10 * time.Millisecond
				p.perBlock = 1700 * time.Microsecond
			}
			if mode == syncanchor.Enforce {
				forged := anchorExtend(all[:stopped+1], 2000, 0xf0, nil)
				net.addPeer(1_000_000_000, harnessHonest, append(append([]block.IBlock{}, all[:stopped+1]...), forged...)...)
			} else {
				net.addPeer(1_000_000_000, harnessEmpty)
			}
			t0 := time.Now()
			for round := 0; round < 10 && net.chain.headNumber() < want; round++ {
				net.svc.enqueueCatchUpNow(top)
			}
			took := time.Since(t0)
			restore()
			if got := net.chain.headNumber(); got != want {
				t.Fatalf("%v pipeline=%v: head = %d, want %d", mode, pipeline, got, want)
			}
			for h, n := range net.chain.perHash {
				if n != 1 {
					t.Fatalf("%v pipeline=%v: block %s executed %d times", mode, pipeline, h.Hex()[:10], n)
				}
			}
			rate[pipeline] = float64(want-stopped) / took.Seconds()
			t.Logf("speed %v pipeline=%v: %d blocks in %v = %.1f blocks/s", mode, pipeline, want-stopped, took.Round(time.Millisecond), rate[pipeline])
		}
		t.Logf("speed %v: pipeline / serial = %.2fx", mode, rate[true]/rate[false])
		if rate[true] <= rate[false] {
			t.Fatalf("%v: pipeline %.1f blocks/s is not faster than serial %.1f", mode, rate[true], rate[false])
		}
	}
}
