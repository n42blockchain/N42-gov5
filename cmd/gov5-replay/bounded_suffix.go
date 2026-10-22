package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

const boundedSuffixJournalKind = "gov5-bounded-suffix-original-frame-ack-v1"

// A suffix schedule is fixed by the already independently verified original
// whitelist and canonical baseline. Later canonical progress cannot enlarge it.
type boundedSuffixSchedule struct {
	Kind   string                          `json:"kind"`
	PlanID types.Hash                      `json:"plan_id"`
	Epoch  [16]byte                        `json:"epoch"`
	Plan   sharedexec.BoundedResumePlan    `json:"plan"`
	Frames []sharedexec.BoundedResumeFrame `json:"original_frames"`
}

type boundedSuffixResult struct {
	ScheduleSHA256 types.Hash              `json:"schedule_sha256"`
	OfferedUnit    string                  `json:"offered_unit"`
	Shards         []boundedSuffixProgress `json:"shards"`
}

type boundedSuffixPending struct {
	Frame sharedexec.BoundedResumeFrame `json:"frame"`
	Stage string                        `json:"stage"`
}

type boundedSuffixProgress struct {
	ScanNextSequence    uint64                `json:"scan_next_original_sequence"`
	AcknowledgedFrames  uint64                `json:"acknowledged_selected_frames"`
	Admitted            uint64                `json:"acknowledged_new_transactions"`
	OmittedFrames       uint64                `json:"omitted_fully_canonical_frames"`
	OmittedTransactions uint64                `json:"omitted_fully_canonical_transactions"`
	OmittedBytes        uint64                `json:"omitted_fully_canonical_encoded_bytes"`
	Pending             *boundedSuffixPending `json:"unacknowledged_tail,omitempty"`
	Exhausted           bool                  `json:"source_exhausted"`
}

type boundedSuffixReceipt struct {
	Kind           string                        `json:"kind"`
	ScheduleSHA256 types.Hash                    `json:"schedule_sha256"`
	PlanID         types.Hash                    `json:"plan_id"`
	Epoch          [16]byte                      `json:"epoch"`
	Frame          sharedexec.BoundedResumeFrame `json:"frame"`
	Ack            sharedexec.ResumeIngressAck   `json:"ack"`
}

func boundedSuffixOmitted(f sharedexec.BoundedResumeFrame) (bool, error) {
	if f.Transactions != 500 || uint32(f.CanonicalSkipped)+uint32(f.NewlyAdmitted) != 500 {
		return false, errors.New("suffix frame classification differs from complete original frame")
	}
	return f.NewlyAdmitted == 0, nil
}

func newBoundedSuffix(dir string, p sharedexec.BoundedResumePlan, frames []sharedexec.BoundedResumeFrame, planID types.Hash, epoch [16]byte, shards int) (*boundedSuffixResult, error) {
	for _, f := range frames {
		if _, err := boundedSuffixOmitted(f); err != nil {
			return nil, err
		}
	}
	schedule := boundedSuffixSchedule{"gov5-bounded-suffix-fixed-original-schedule-v1", planID, epoch, p, frames}
	h := sha256.New()
	// Encoder's complete JSON plus newline is also the exact exclusive file.
	if err := json.NewEncoder(h).Encode(schedule); err != nil {
		return nil, err
	}
	if err := exclusiveJSON(filepath.Join(dir, "suffix-schedule.json"), schedule); err != nil {
		return nil, err
	}
	var digest types.Hash
	copy(digest[:], h.Sum(nil))
	return &boundedSuffixResult{digest, "original transactions dispatched to owner; omissions are not offered, ACKed or crypto-verified", make([]boundedSuffixProgress, shards)}, nil
}

// This separate mode still reads and validates every original frame in order.
// Only a fixed fully-canonical classification suppresses dispatch. One worker
// owns each progress record and journal, and retains at most one payload.
func boundedSuffixPumpShard(ctx, dispatch context.Context, source strictSource, d sharedexec.CorpusDescriptor, selected []sharedexec.BoundedResumeFrame, c boundedAdmitter, planID types.Hash, epoch [16]byte, schedule types.Hash, budget *boundedSenderBudget, pacer *supplyPacer, counters *boundedStreamCounters, ready chan<- error, start <-chan struct{}, journal io.Writer, senderNew []uint64, progress *boundedSuffixProgress) error {
	if len(selected) == 0 {
		err := errors.New("suffix shard has no original whitelist")
		ready <- err
		return err
	}
	reader := bufio.NewReaderSize(source.file, 8<<20)
	var scratch []byte
	for index, planned := range selected {
		if dispatch.Err() != nil {
			if index == 0 {
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
		if err == nil && (frame.CorpusFrameID != planned.ID || frame.PayloadHash != planned.PayloadHash || len(frame.Payload) != int(planned.PayloadBytes) || frame.Transactions != 500 || planned.ID.Sequence != progress.ScanNextSequence) {
			err = errors.New("suffix source differs from fixed original whitelist sequence/bytes")
		}
		omitted, classifyErr := boundedSuffixOmitted(planned)
		if err == nil {
			err = classifyErr
		}
		// Count every valid preloaded frame even if dispatch is canceled while
		// waiting at the first-frame barrier. This never grants a reservation.
		if err == nil {
			counters.read.Add(500)
			wire := uint64(sharedexec.CorpusWireHeaderSize + len(frame.Payload))
			counters.encodedBytes.Add(wire)
			progress.ScanNextSequence = planned.ID.Sequence + 1
			if omitted {
				progress.OmittedFrames++
				progress.OmittedTransactions += 500
				progress.OmittedBytes += wire
			} else {
				progress.Pending = &boundedSuffixPending{planned, "read_unsubmitted"}
			}
		}
		if index == 0 {
			ready <- err
			if err == nil {
				select {
				case <-start:
				case <-dispatch.Done():
					if omitted && len(selected) == 1 {
						// Conservatively record pre-start exhaustion without reading
						// the pacer start which the coordinator has not published.
						boundedSuffixExhausted(counters, progress, 1)
					}
					return nil
				}
			}
		}
		if err != nil {
			return err
		}
		if omitted {
			continue
		}
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
		progress.Pending.Stage = "charged_unacknowledged"
		ack, accepted, err := boundedAdmitFrame(ctx, dispatch, c, frame, planned, planID, epoch, counters)
		if err != nil {
			return err
		} // Unknown outcomes retain charge, never replay.
		if !accepted {
			budget.releaseKnownZero(planned)
			progress.Pending.Stage = "known_zero"
			return nil
		}
		progress.Pending.Stage = "admitted_not_journaled"
		if err := json.NewEncoder(journal).Encode(boundedSuffixReceipt{boundedSuffixJournalKind, schedule, planID, epoch, planned, ack}); err != nil {
			return err
		}
		counters.admitted.Add(uint64(ack.NewlyAdmitted))
		counters.skipped.Add(uint64(ack.CanonicalSkipped))
		counters.frames.Add(1)
		senderNew[planned.SenderIndex] += uint64(ack.NewlyAdmitted)
		progress.AcknowledgedFrames++
		progress.Admitted += uint64(ack.NewlyAdmitted)
		progress.Pending = nil
	}
	boundedSuffixExhausted(counters, progress, max(1, time.Since(pacer.start).Nanoseconds()))
	return nil
}

func boundedSuffixExhausted(counters *boundedStreamCounters, progress *boundedSuffixProgress, t int64) {
	for old := counters.firstExhaustedNanos.Load(); old == 0 || t < old; old = counters.firstExhaustedNanos.Load() {
		if counters.firstExhaustedNanos.CompareAndSwap(old, t) {
			break
		}
	}
	counters.exhaustedShards.Add(1)
	progress.Exhausted = true
}
