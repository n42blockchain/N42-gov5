package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

type boundedAdmitter interface {
	AdmitBounded(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error)
	Epoch() [16]byte
	PlanID() types.Hash
	Close() error
}

// The sender has a separate nonrefundable budget. Known-zero retry keeps the
// same reservation; only a confirmed zero outcome before dispatch stops can
// return it. An unknown outcome retains its entire budget and stops the run.
type boundedSenderBudget struct {
	mu                                 sync.Mutex
	plan                               sharedexec.BoundedResumePlan
	frames, transactions, bytes, newly uint64
}

type boundedSenderBudgetSample struct {
	Frames       uint64 `json:"charged_original_frames"`
	Transactions uint64 `json:"charged_original_transactions"`
	Bytes        uint64 `json:"charged_original_encoded_bytes"`
	New          uint64 `json:"charged_new_transactions"`
}

func (b *boundedSenderBudget) reserve(f sharedexec.BoundedResumeFrame) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	wire := uint64(sharedexec.CorpusWireHeaderSize) + uint64(f.PayloadBytes)
	for _, item := range [][3]uint64{{b.frames, 1, b.plan.MaxSourceFrames}, {b.transactions, 500, b.plan.MaxSourceTransactions}, {b.bytes, wire, b.plan.SourceEncodedBytes}, {b.newly, uint64(f.NewlyAdmitted), b.plan.MaxNewTransactions}} {
		if item[0] > item[2] || item[1] > item[2]-item[0] {
			return errors.New("sender bounded lifetime source/new/encoded cap exhausted")
		}
	}
	b.frames++
	b.transactions += 500
	b.bytes += wire
	b.newly += uint64(f.NewlyAdmitted)
	return nil
}

func (b *boundedSenderBudget) releaseKnownZero(f sharedexec.BoundedResumeFrame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.frames--
	b.transactions -= 500
	b.bytes -= uint64(sharedexec.CorpusWireHeaderSize) + uint64(f.PayloadBytes)
	b.newly -= uint64(f.NewlyAdmitted)
}
func (b *boundedSenderBudget) sample() boundedSenderBudgetSample {
	b.mu.Lock()
	defer b.mu.Unlock()
	return boundedSenderBudgetSample{b.frames, b.transactions, b.bytes, b.newly}
}

type boundedStreamCounters struct {
	strictStreamCounters
	skipped, frames atomic.Uint64
}

type boundedStreamSample struct {
	strictStreamSample
	Frames           uint64 `json:"acknowledged_original_frames"`
	CanonicalSkipped uint64 `json:"acknowledged_canonical_skipped"`
}

func (c *boundedStreamCounters) boundedSample() boundedStreamSample {
	return boundedStreamSample{c.sample(), c.frames.Load(), c.skipped.Load()}
}

type boundedReceipt struct {
	Frame sharedexec.BoundedResumeFrame `json:"frame"`
	Ack   sharedexec.ResumeIngressAck   `json:"ack"`
}

// Dispatch cancellation is checked between requests. It never cancels an IO
// already started, including a granted final ACK. All IO remains parent-bounded.
func boundedAdmitFrame(ctx, dispatch context.Context, c boundedAdmitter, frame sharedexec.CorpusFrame, selected sharedexec.BoundedResumeFrame, planID types.Hash, epoch [16]byte, counters *boundedStreamCounters) (sharedexec.ResumeIngressAck, bool, error) {
	if c.Epoch() != epoch || c.PlanID() != planID {
		return sharedexec.ResumeIngressAck{}, false, errors.New("bounded welcome plan/epoch changed before dispatch")
	}
	for {
		if err := ctx.Err(); err != nil {
			return sharedexec.ResumeIngressAck{}, false, err
		}
		if dispatch.Err() != nil {
			return sharedexec.ResumeIngressAck{}, false, nil
		}
		work, cancel := context.WithTimeout(ctx, 5*time.Second)
		before := time.Now()
		ack, err := c.AdmitBounded(work, frame, selected)
		cancel()
		counters.admitNanos.Add(uint64(time.Since(before)))
		if err == nil {
			if ack.PlanID != planID || ack.Status.Epoch != epoch || ack.Duplicate || ack.PriorAdmission != int(selected.NewlyAdmitted) || ack.CanonicalSkipped != int(selected.CanonicalSkipped) || ack.NewlyAdmitted != int(selected.NewlyAdmitted) {
				return ack, false, fmt.Errorf("%w: bounded final receipt differs", sharedexec.ErrIngressUnknownOutcome)
			}
			return ack, true, nil
		}
		if !errors.Is(err, sharedexec.ErrIngressCreditsExhausted) && !errors.Is(err, sharedexec.ErrIngressSnapshotChanged) {
			return ack, false, err
		}
		if ack.NewlyAdmitted != 0 || ack.CanonicalSkipped != 0 || ack.PriorAdmission != 0 || ack.Duplicate {
			return ack, false, fmt.Errorf("%w: bounded retry carried nonzero publication", sharedexec.ErrIngressUnknownOutcome)
		}
		snapshot := errors.Is(err, sharedexec.ErrIngressSnapshotChanged)
		if snapshot {
			counters.snapshot.Add(1)
		} else {
			counters.busy.Add(1)
		}
		before = time.Now()
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ack, false, ctx.Err()
		case <-dispatch.Done():
			timer.Stop()
			return ack, false, nil
		case <-timer.C:
		}
		if snapshot {
			counters.snapshotWaitNanos.Add(uint64(time.Since(before)))
		} else {
			counters.busyWaitNanos.Add(uint64(time.Since(before)))
		}
	}
}

// Only one worker owns a shard, its journal, and its disjoint sender indices.
// Workers retain at most one original frame each; no decoded source corpus is
// retained. Journals are synced at the window boundary and again after all ACKs.
func boundedPumpShard(ctx, dispatch context.Context, source strictSource, d sharedexec.CorpusDescriptor, selected []sharedexec.BoundedResumeFrame, c boundedAdmitter, planID types.Hash, epoch [16]byte, budget *boundedSenderBudget, pacer *supplyPacer, counters *boundedStreamCounters, ready chan<- error, start <-chan struct{}, journal io.Writer, senderNew []uint64, progress *strictShardProgress) error {
	if len(selected) == 0 {
		ready <- errors.New("bounded shard has no original prefix")
		return errors.New("bounded shard has no original prefix")
	}
	reader := bufio.NewReaderSize(source.file, 8<<20)
	var scratch []byte
	for seq, planned := range selected {
		if dispatch.Err() != nil {
			if seq == 0 {
				ready <- dispatch.Err()
			}
			return nil
		}
		before := time.Now()
		frame, next, err := sharedexec.ReadCorpusFrame(reader, scratch)
		scratch = next
		counters.readNanos.Add(uint64(time.Since(before)))
		if err == nil {
			err = frame.Validate(d)
		}
		if err == nil && (frame.CorpusFrameID != planned.ID || frame.PayloadHash != planned.PayloadHash || len(frame.Payload) != int(planned.PayloadBytes) || frame.Transactions != 500) {
			err = errors.New("bounded source differs from fixed original whitelist")
		}
		if seq == 0 {
			ready <- err
			if err == nil {
				select {
				case <-start:
				case <-dispatch.Done():
					return nil
				}
			}
		}
		if err != nil {
			return err
		}
		counters.read.Add(500)
		counters.encodedBytes.Add(uint64(sharedexec.CorpusWireHeaderSize + len(frame.Payload)))
		before = time.Now()
		err = pacer.wait(dispatch, 500)
		counters.paceNanos.Add(uint64(time.Since(before)))
		if err != nil {
			if dispatch.Err() != nil {
				return nil
			}
			return err
		}
		if dispatch.Err() != nil {
			return nil
		}
		if err := budget.reserve(planned); err != nil {
			return err
		}
		ack, accepted, err := boundedAdmitFrame(ctx, dispatch, c, frame, planned, planID, epoch, counters)
		if err != nil {
			return err
		} // Unknown and failures never refund or replay.
		if !accepted {
			budget.releaseKnownZero(planned)
			return nil
		}
		// A journal write failure occurs after a known admission. Preserve the
		// charged budget and stop; never turn it into permission to replay.
		if err := json.NewEncoder(journal).Encode(boundedReceipt{planned, ack}); err != nil {
			return err
		}
		counters.admitted.Add(uint64(ack.NewlyAdmitted))
		counters.skipped.Add(uint64(ack.CanonicalSkipped))
		counters.frames.Add(1)
		senderNew[planned.SenderIndex] += uint64(ack.NewlyAdmitted)
		progress.Sequence = planned.ID.Sequence + 1
		progress.Transactions += uint64(ack.NewlyAdmitted)
	}
	// Count exhaustion immediately after the last known ACK, not after a
	// post-window wait/drain. Even one shard's early exhaustion fails supply.
	t := time.Since(pacer.start).Nanoseconds()
	for old := counters.firstExhaustedNanos.Load(); old == 0 || t < old; old = counters.firstExhaustedNanos.Load() {
		if counters.firstExhaustedNanos.CompareAndSwap(old, t) {
			break
		}
	}
	counters.exhaustedShards.Add(1)
	progress.Exhausted = true
	return nil
}

func syncBoundedJournals(files []*os.File) error {
	var failures []error
	for _, f := range files {
		failures = append(failures, f.Sync())
	}
	return errors.Join(failures...)
}
