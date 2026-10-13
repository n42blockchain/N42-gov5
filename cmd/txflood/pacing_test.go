package main

import "testing"

func TestSubmissionPacerExactLowAndHighRates(t *testing.T) {
	for _, rate := range []int{1, 2, 99, 100, 101, 250, 400000, 4000001} {
		for _, batch := range []int{1, 2, 200} {
			pacer := submissionPacer{rate: rate, batch: batch}
			total := 0
			for tick := 0; tick < 1000; tick++ {
				total += pacer.tick()
			}
			if total != rate*10/batch {
				t.Fatalf("rate=%d batch=%d permits=%d", rate, batch, total)
			}
			if pacer.tickRemainder != 0 || pacer.batchRemainder != rate*10%batch {
				t.Fatal("fractional credit lost")
			}
		}
	}
}

func TestSubmissionPacerDoesNotRoundTwoTPSUpToHundred(t *testing.T) {
	pacer := submissionPacer{rate: 2, batch: 1}
	for tick := 0; tick < 49; tick++ {
		if pacer.tick() != 0 {
			t.Fatal("released before half second")
		}
	}
	if pacer.tick() != 1 {
		t.Fatal("missing half-second permit")
	}
}

func TestSubmissionPermitsBoundStalledWorkerCredit(t *testing.T) {
	for _, tc := range []struct{ rate, batch, capacity int }{
		{2, 1, 1}, {2, 200, 1}, {400000, 200, 20}, {4000001, 200, 201},
	} {
		pacer := submissionPacer{rate: tc.rate, batch: tc.batch}
		permits := make(chan struct{}, submissionPermitCapacity(tc.rate, tc.batch))
		for tick := 0; tick < 20000; tick++ {
			for n := pacer.tick(); n > 0; n-- {
				select {
				case permits <- struct{}{}:
				default:
				}
			}
		}
		if len(permits) != tc.capacity {
			t.Fatalf("rate=%d batch=%d accumulated %d permits, want %d", tc.rate, tc.batch, len(permits), tc.capacity)
		}
	}
}
