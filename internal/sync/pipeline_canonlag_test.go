// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/syncanchor"
)

// A fresh node catches up from genesis with the pipeline on. Block n embeds
// the QC of n-2 and the canonical head trails the executed blocks by two (it
// moves only with a CommitQC), as on a profile-1 chain. In enforce mode the
// first slot (1..64) executes 1..62 and holds 63..64 while the canonical
// head is 60, below the tail's parent. The importer must keep its cursor
// above the executed frontier (and the held tail), not re-take 61..64
// forever: the S1 stall at 62/63, "range not fully anchored" in a hot loop.
// Observe and off had the same loop one slot later (cursor at 63 with 64
// executed).
func TestPipelineCanonLagFromGenesis(t *testing.T) {
	for _, mode := range []syncanchor.Mode{syncanchor.Enforce, syncanchor.Observe, syncanchor.Off} {
		t.Run(mode.String(), func(t *testing.T) {
			savedStall := pipeStallTimeout
			pipeStallTimeout = 500 * time.Millisecond
			t.Cleanup(func() { pipeStallTimeout = savedStall })

			all := anchorChain(300, allVerified)
			net, _ := newPipelineNet(t, mode, all[:1])
			net.chain.canonLag = 2
			var ps []*harnessPeer
			for i := 0; i < 3; i++ {
				ps = append(ps, net.addPeer(300, harnessHonest, all...))
			}

			done := make(chan struct{})
			go func() {
				defer close(done)
				runPipeline(net, 300)
			}()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatalf("pipeline run did not end: applied head %d", net.chain.appliedNumber())
			}
			want := uint64(300)
			if mode == syncanchor.Enforce {
				want = 298 // 299 and 300 wait for an anchor above them
			}
			if got := net.chain.appliedNumber(); got != want {
				t.Fatalf("applied head = %d, want %d", got, want)
			}
			asked := 0
			for _, p := range ps {
				asked += p.askedCount()
			}
			// 1..300 in 64-block slots is 5 requests; allow head-of-line
			// duplicates but not a re-fetch loop.
			if asked > 20 {
				t.Fatalf("%d range requests for 300 blocks: re-fetch loop", asked)
			}
			assertNoScores(t, net, ps...)
			net.chain.mu.Lock()
			defer net.chain.mu.Unlock()
			for h, n := range net.chain.perHash {
				if n != 1 {
					t.Fatalf("block %x executed %d times", h[:4], n)
				}
			}
		})
	}
}
