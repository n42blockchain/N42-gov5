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
