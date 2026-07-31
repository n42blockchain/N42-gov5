// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package blspool

import (
	"sync"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// TestVerifyCEConcurrent runs many verifications at once. VerifyCE used to hold
// the pool mutex across the pairing, so this was fully serialised; the pairing
// now runs outside the lock and only the committee/pubkey resolution is guarded.
// Run under -race to check the split did not expose p.scratch or p.pks.
func TestVerifyCEConcurrent(t *testing.T) {
	p := testPool(t)

	const nBlocks = 24
	ces := make([]*rawdb.ConsensusEvidence, nBlocks)
	for i := range ces {
		var bh, rr types.Hash
		bh[0], rr[0] = byte(i+1), 0xbb
		ce, missing, err := p.BuildCE(uint64(i+1), bh, rr, nil)
		if err != nil || len(missing) != 0 {
			t.Fatalf("build %d: err=%v missing=%v", i, err, missing)
		}
		ces[i] = ce
	}

	// Serial baseline first, so the parallel figure below has something to
	// mean: with the pairing under the pool mutex the two were the same.
	const rounds = 4
	serialStart := time.Now()
	for r := 0; r < rounds; r++ {
		for _, ce := range ces {
			if _, ok, err := p.VerifyCE(ce); err != nil || !ok {
				t.Fatalf("serial verify: ok=%v err=%v", ok, err)
			}
		}
	}
	serial := time.Since(serialStart)

	const workers = 8
	start := time.Now()
	var wg sync.WaitGroup
	errCh := make(chan string, workers*rounds*nBlocks)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				for i, ce := range ces {
					covered, ok, err := p.VerifyCE(ce)
					if err != nil || !ok || covered != 64 {
						errCh <- "block " + string(rune('0'+i)) + ": verify failed"
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for msg := range errCh {
		t.Fatal(msg)
	}
	parallel := time.Since(start)
	perSerial := serial / (rounds * nBlocks)
	perParallel := parallel / (workers * rounds * nBlocks)
	t.Logf("serial:   %4d verifications in %8s (%s each)", rounds*nBlocks, serial.Truncate(time.Millisecond), perSerial.Truncate(time.Microsecond))
	t.Logf("parallel: %4d verifications in %8s (%s each, %d goroutines) → %.1fx",
		workers*rounds*nBlocks, parallel.Truncate(time.Millisecond), perParallel.Truncate(time.Microsecond),
		workers, float64(perSerial)/float64(perParallel))
}
