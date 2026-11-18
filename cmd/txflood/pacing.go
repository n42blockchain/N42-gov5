package main

import "time"

// submissionPacer keeps fractional transactions between 10 ms ticks and
// fractional batches between releases. A slow requested rate stays slow.
type submissionPacer struct {
	rate, batch                   int
	tickRemainder, batchRemainder int
}

func (p *submissionPacer) tick() int {
	transactions := p.rate / 100
	p.tickRemainder += p.rate % 100
	transactions += p.tickRemainder / 100
	p.tickRemainder %= 100
	permits := transactions / p.batch
	p.batchRemainder += transactions % p.batch
	permits += p.batchRemainder / p.batch
	p.batchRemainder %= p.batch
	return permits
}

func pacedSubmissionPermits(rate, batch int) chan struct{} {
	// Permits release batches, not individual transactions. Retain at most
	// one tick of credit so a slow RPC cannot bank seconds of burst traffic.
	permits := make(chan struct{}, submissionPermitCapacity(rate, batch))
	go func() {
		budget := submissionPacer{rate: rate, batch: batch}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			for n := budget.tick(); n > 0; n-- {
				select {
				case permits <- struct{}{}:
				default: // A stalled submitter cannot accumulate unbounded credit.
				}
			}
		}
	}()
	return permits
}

func submissionPermitCapacity(rate, batch int) int {
	transactions := rate / 100
	if rate%100 != 0 {
		transactions++
	}
	capacity := transactions / batch
	if transactions%batch != 0 {
		capacity++
	}
	return max(capacity, 1)
}
