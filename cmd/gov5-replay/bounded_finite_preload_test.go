package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

type finiteFailWriter struct{ err error }

func (w finiteFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestBoundedFiniteReceiptFailureStopsAndRetainsChargedBudget(t *testing.T) {
	for _, failure := range []string{"unknown", "journal", "advanced", "pending"} {
		t.Run(failure, func(t *testing.T) {
			frame := strictTestFrame(t)
			planned := sharedexec.BoundedResumeFrame{ID: frame.CorpusFrameID, PayloadHash: frame.PayloadHash, PayloadBytes: uint32(len(frame.Payload)), CanonicalSkipped: 41, NewlyAdmitted: 459}
			initial := sharedexec.StrictIngressStatus{Epoch: [16]byte{1}, CanonicalHash: types.Hash{3}, CanonicalNumber: 1240}
			client := &boundedTestAdmitter{epoch: initial.Epoch, id: types.Hash{2}}
			client.admit = func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
				ack := boundedTestAck(client, planned)
				ack.Status = initial
				ack.Status.NewlyAdmitted, ack.Status.PendingTransactions = 459, 459
				switch failure {
				case "unknown":
					return ack, sharedexec.ErrIngressUnknownOutcome
				case "advanced":
					ack.Status.CanonicalNumber++
				case "pending":
					ack.Status.PendingTransactions--
				}
				return ack, nil
			}
			budget := &boundedSenderBudget{plan: sharedexec.BoundedResumePlan{MaxSourceFrames: 2, MaxSourceTransactions: 1000, SourceEncodedBytes: uint64(2 * (84 + len(frame.Payload))), MaxNewTransactions: 918}}
			counters := new(boundedStreamCounters)
			var journal bytes.Buffer
			var err error
			if failure == "journal" {
				_, err = admitBoundedFiniteFrame(context.Background(), client, frame, planned, client.id, initial, counters, budget, finiteFailWriter{errors.New("disk write failed")})
			} else {
				_, err = admitBoundedFiniteFrame(context.Background(), client, frame, planned, client.id, initial, counters, budget, &journal)
			}
			if err == nil || client.calls != 1 || budget.sample().New != 459 || counters.admitted.Load() != 0 || counters.frames.Load() != 0 || journal.Len() != 0 {
				t.Fatal("failed publication was replayed, refunded or reported as acknowledged", err)
			}
			if failure == "unknown" && !errors.Is(err, sharedexec.ErrIngressUnknownOutcome) {
				t.Fatal("unknown classification lost", err)
			}
		})
	}
}

func TestBoundedFiniteKnownReceiptJournalsBeforeProgress(t *testing.T) {
	frame := strictTestFrame(t)
	planned := sharedexec.BoundedResumeFrame{ID: frame.CorpusFrameID, PayloadBytes: uint32(len(frame.Payload)), CanonicalSkipped: 500}
	initial := sharedexec.StrictIngressStatus{Epoch: [16]byte{1}}
	client := &boundedTestAdmitter{epoch: initial.Epoch, id: types.Hash{2}}
	client.admit = func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
		return boundedTestAck(client, planned), nil
	}
	budget := &boundedSenderBudget{plan: sharedexec.BoundedResumePlan{MaxSourceFrames: 1, MaxSourceTransactions: 500, SourceEncodedBytes: uint64(84 + len(frame.Payload))}}
	counters := new(boundedStreamCounters)
	var journal bytes.Buffer
	ack, err := admitBoundedFiniteFrame(context.Background(), client, frame, planned, client.id, initial, counters, budget, &journal)
	if err != nil || ack.CanonicalSkipped != 500 || counters.frames.Load() != 1 || counters.skipped.Load() != 500 || budget.sample().New != 0 || journal.Len() == 0 {
		t.Fatal("all-skipped receipt accounting differs", err)
	}
}

func TestBoundedFiniteCLIRequiresExplicitPins(t *testing.T) {
	for _, args := range [][]string{{"bounded-phase-preload"}, {"bounded-phase-preload", "--offered-tps", "1"}, {"bounded-phase-preload", "--expected-plan-id", "invalid"}} {
		if err := runSupply(context.Background(), args); err == nil {
			t.Fatal("finite command accepted incomplete or rate-based invocation", args)
		}
	}
}
