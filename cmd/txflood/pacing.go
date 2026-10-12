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
	permits := make(chan struct{}, rate)
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
