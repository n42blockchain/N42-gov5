package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/n42blockchain/N42/internal/sharedexec"
)

func TestQueueAdmissionRetriesOnlyKnownZeroOnSameFrame(t *testing.T) {
	frame := sharedexec.CorpusFrame{Payload: []byte{1, 2, 3}, Transactions: 2}
	calls := 0
	ack, retries, err := admitQueueFrame(context.Background(), frame, time.Second, func(ctx context.Context, got sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
		if !reflect.DeepEqual(got, frame) || ctx.Err() != nil {
			t.Fatal("retry changed signed frame or canceled work")
		}
		calls++
		switch calls {
		case 1:
			return sharedexec.StrictIngressAck{}, sharedexec.ErrIngressCreditsExhausted
		case 2:
			return sharedexec.StrictIngressAck{}, sharedexec.ErrIngressSnapshotChanged
		default:
			return sharedexec.StrictIngressAck{NewlyAdmitted: 2}, nil
		}
	})
	if err != nil || calls != 3 || ack.NewlyAdmitted != 2 || retries != (queueAdmissionRetries{Busy: 1, Snapshot: 1}) {
		t.Fatalf("calls=%d ack=%+v retries=%+v err=%v", calls, ack, retries, err)
	}
}

func TestQueueAdmissionNeverRetriesUnknownOrRejection(t *testing.T) {
	for _, failure := range []error{sharedexec.ErrIngressUnknownOutcome, errors.New("nonce gap"), errors.New("canonical accounts are not actual applied state; retry")} {
		calls := 0
		_, retries, err := admitQueueFrame(context.Background(), sharedexec.CorpusFrame{}, time.Second, func(context.Context, sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
			calls++
			return sharedexec.StrictIngressAck{}, failure
		})
		if !errors.Is(err, failure) || calls != 1 || retries != (queueAdmissionRetries{}) {
			t.Fatalf("non-typed error retried: %v calls=%d", err, calls)
		}
	}
}

func TestQueueAdmissionRefusesNonzeroRetry(t *testing.T) {
	calls := 0
	_, retries, err := admitQueueFrame(context.Background(), sharedexec.CorpusFrame{}, time.Second, func(context.Context, sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
		calls++
		return sharedexec.StrictIngressAck{NewlyAdmitted: 1}, sharedexec.ErrIngressSnapshotChanged
	})
	if err == nil || calls != 1 || retries != (queueAdmissionRetries{}) {
		t.Fatal("nonzero admission was replayed")
	}
}

func TestQueueAdmissionSnapshotRetriesAreBounded(t *testing.T) {
	calls := 0
	_, retries, err := admitQueueFrame(context.Background(), sharedexec.CorpusFrame{}, 45*time.Millisecond, func(context.Context, sharedexec.CorpusFrame) (sharedexec.StrictIngressAck, error) {
		calls++
		return sharedexec.StrictIngressAck{}, sharedexec.ErrIngressSnapshotChanged
	})
	if !errors.Is(err, context.DeadlineExceeded) || calls < 1 || calls > 4 || retries.Snapshot != calls {
		t.Fatalf("unbounded retries: calls=%d retries=%+v err=%v", calls, retries, err)
	}
}
