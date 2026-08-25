package ethel

import (
	"context"
	"testing"
	"time"
)

func TestReplayInputReservoirUsesLowHighHysteresis(t *testing.T) {
	r := newReplayInputReservoir(40, 100)
	ctx := context.Background()
	if err := r.reserve(ctx, 60); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- r.reserve(ctx, 50) }()
	select {
	case err := <-done:
		t.Fatalf("reservation crossed high without draining: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	r.release(10) // 50: still above low
	select {
	case err := <-done:
		t.Fatalf("reservation refilled before low watermark: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	r.release(10) // 40: refill is now allowed
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reservation did not refill after reaching low watermark")
	}
	current, peak, waits, _ := r.stats()
	if current != 90 || peak != 90 || waits != 1 {
		t.Fatalf("stats current=%d peak=%d waits=%d, want 90/90/1", current, peak, waits)
	}
}

func TestReplayInputReservoirAllowsOversizedJob(t *testing.T) {
	r := newReplayInputReservoir(40, 100)
	if err := r.reserve(context.Background(), 150); err != nil {
		t.Fatal(err)
	}
	current, peak, _, _ := r.stats()
	if current != 150 || peak != 150 {
		t.Fatalf("oversized reservation current=%d peak=%d", current, peak)
	}
}

func TestRelayWitnessJobsPreservesOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in := make(chan WitnessJob)
	out := make(chan WitnessJob)
	go relayWitnessJobs(ctx, in, out)
	go func() {
		for i := uint64(1); i <= 100; i++ {
			in <- WitnessJob{BlockNum: i}
		}
		close(in)
	}()
	var want uint64 = 1
	for job := range out {
		if job.BlockNum != want {
			t.Fatalf("got block %d, want %d", job.BlockNum, want)
		}
		want++
	}
	if want != 101 {
		t.Fatalf("received %d jobs, want 100", want-1)
	}
}
