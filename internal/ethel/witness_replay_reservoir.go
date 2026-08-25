package ethel

import (
	"context"
	"sync"
	"time"
)

// replayInputReservoir accounts for decoded jobs retained by the input FIFO or
// currently executing. It uses hysteresis: producers fill to high, stop, and
// all wake together only after completions drain the outstanding bytes to low.
// The byte estimate is deliberately based on retained object shape rather than
// compressed input size; its calibration is a runtime tuning parameter.
type replayInputReservoir struct {
	mu       sync.Mutex
	low      int64
	high     int64
	bytes    int64
	peak     int64
	draining bool
	changed  chan struct{}
	waits    uint64
	waited   time.Duration
}

func newReplayInputReservoir(low, high int64) *replayInputReservoir {
	return &replayInputReservoir{
		low:     low,
		high:    high,
		changed: make(chan struct{}),
	}
}

func (r *replayInputReservoir) reserve(ctx context.Context, bytes int64) error {
	if r == nil {
		return nil
	}
	if bytes < 1 {
		bytes = 1
	}
	var waitStart time.Time
	for {
		r.mu.Lock()
		if r.draining && r.bytes <= r.low {
			r.draining = false
		}
		// Permit one job to cross high when currently at/below low. This avoids
		// deadlock for a pathological single job larger than the whole budget
		// and bounds ordinary overshoot to one job.
		if !r.draining && (r.bytes+bytes <= r.high || r.bytes <= r.low) {
			r.bytes += bytes
			if r.bytes > r.peak {
				r.peak = r.bytes
			}
			if r.bytes >= r.high {
				r.draining = true
			}
			if !waitStart.IsZero() {
				r.waited += time.Since(waitStart)
			}
			r.mu.Unlock()
			return nil
		}
		if waitStart.IsZero() {
			waitStart = time.Now()
			r.waits++
		}
		r.draining = true
		changed := r.changed
		r.mu.Unlock()

		select {
		case <-ctx.Done():
			r.mu.Lock()
			r.waited += time.Since(waitStart)
			r.mu.Unlock()
			return ctx.Err()
		case <-changed:
		}
	}
}

func (r *replayInputReservoir) release(bytes int64) {
	if r == nil || bytes <= 0 {
		return
	}
	r.mu.Lock()
	r.bytes -= bytes
	if r.bytes < 0 {
		r.bytes = 0
	}
	if r.draining && r.bytes <= r.low {
		close(r.changed)
		r.changed = make(chan struct{})
	}
	r.mu.Unlock()
}

func (r *replayInputReservoir) stats() (current, peak int64, waits uint64, waited time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes, r.peak, r.waits, r.waited
}

// relayWitnessJobs is an elastic FIFO between input producers and workers.
// Its queue length needs no guessed block cap: every accepted job has already
// reserved bytes from replayInputReservoir, which is the actual bound.
func relayWitnessJobs(ctx context.Context, in <-chan WitnessJob, out chan<- WitnessJob) {
	defer close(out)
	queue := make([]WitnessJob, 0, HeaderSegmentSize)
	head := 0
	for in != nil || head < len(queue) {
		if head == len(queue) {
			queue = queue[:0]
			head = 0
			select {
			case <-ctx.Done():
				return
			case job, ok := <-in:
				if !ok {
					in = nil
					continue
				}
				queue = append(queue, job)
			}
			continue
		}

		select {
		case <-ctx.Done():
			return
		case job, ok := <-in:
			if !ok {
				in = nil
			} else {
				queue = append(queue, job)
			}
		case out <- queue[head]:
			queue[head] = WitnessJob{}
			head++
			// Avoid retaining an arbitrarily long empty prefix after a large
			// refill. Copy only when the reclaimed prefix materially dominates.
			if head >= HeaderSegmentSize && head*2 >= len(queue) {
				copy(queue, queue[head:])
				queue = queue[:len(queue)-head]
				head = 0
			}
		}
	}
}

func estimateWitnessJobBytes(job *WitnessJob) int64 {
	// Header/body wrappers, closure, interfaces and allocator/span overhead.
	bytes := int64(2048 + len(job.Witness) + len(job.Senders)*20)
	if job.Body == nil {
		return bytes
	}
	bytes += int64(len(job.Body.Withdrawals))*64 + int64(len(job.Body.Uncles))*1024
	for _, tx := range job.Body.Transactions {
		if tx == nil {
			continue
		}
		// Transaction + concrete TxData + uint256 values and cache cells. The
		// dynamic columns below are added separately. This coefficient is an
		// estimate by design; high/low GB flags are tuned against RSS/throughput.
		bytes += 1024 + int64(len(tx.Data()))
		accessList := tx.AccessList()
		bytes += int64(len(accessList)) * 64
		for _, tuple := range accessList {
			bytes += int64(len(tuple.StorageKeys)) * 32
		}
		bytes += int64(len(tx.BlobHashes())) * 32
		bytes += int64(len(tx.AuthList())) * 224
	}
	return bytes
}
