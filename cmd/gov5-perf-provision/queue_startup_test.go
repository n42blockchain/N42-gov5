package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

func queueRecoveryStatus() sharedexec.StrictIngressStatus {
	return sharedexec.StrictIngressStatus{ManifestHash: types.HexToHash("0x1234"), Epoch: [16]byte{1}, Reconciling: true, CanonicalNumber: 1, CanonicalHash: types.HexToHash("0xabcd")}
}

func TestQueueStartupWaitsForZeroAdmissionRecovery(t *testing.T) {
	initial := queueRecoveryStatus()
	calls := 0
	status, err := waitQueueRecovery(context.Background(), initial.ManifestHash, func(ctx context.Context) (sharedexec.StrictIngressStatus, error) {
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		calls++
		next := initial
		if calls == 3 {
			next.Reconciling = false
			next.CanonicalNumber = 2
			next.CanonicalHash = types.HexToHash("0xbeef")
		}
		return next, nil
	})
	if err != nil || calls != 3 || status.Reconciling || status.CanonicalNumber != 2 {
		t.Fatalf("startup wait: %+v %d %v", status, calls, err)
	}
}

func TestQueueStartupWaitRejectsSessionOrAdmissionChanges(t *testing.T) {
	changes := map[string]func(*sharedexec.StrictIngressStatus){
		"epoch":          func(s *sharedexec.StrictIngressStatus) { s.Epoch[0]++ },
		"manifest":       func(s *sharedexec.StrictIngressStatus) { s.ManifestHash[0]++ },
		"new":            func(s *sharedexec.StrictIngressStatus) { s.NewlyAdmitted++ },
		"crypto":         func(s *sharedexec.StrictIngressStatus) { s.CryptoVerified++ },
		"pending":        func(s *sharedexec.StrictIngressStatus) { s.PendingTransactions++ },
		"reserved":       func(s *sharedexec.StrictIngressStatus) { s.ReservedTransactions++ },
		"retained-bytes": func(s *sharedexec.StrictIngressStatus) { s.RetainedEncodedBytes++ },
		"reserved-bytes": func(s *sharedexec.StrictIngressStatus) { s.ReservedEncodedBytes++ },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			initial := queueRecoveryStatus()
			calls := 0
			_, err := waitQueueRecovery(context.Background(), initial.ManifestHash, func(context.Context) (sharedexec.StrictIngressStatus, error) {
				calls++
				next := initial
				if calls > 1 {
					change(&next)
				}
				return next, nil
			})
			if err == nil || calls != 2 {
				t.Fatalf("changed recovery state accepted: calls=%d %v", calls, err)
			}
		})
	}
}

func TestQueueStartupWaitRemainsBoundedWithoutCertificate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Millisecond)
	defer cancel()
	status := queueRecoveryStatus()
	_, err := waitQueueRecovery(ctx, status.ManifestHash, func(context.Context) (sharedexec.StrictIngressStatus, error) { return status, nil })
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no transactions sent") {
		t.Fatalf("blocked startup lacks bounded diagnostic: %v", err)
	}
}

func TestQueueStartupWaitDoesNotRetryProbeFailure(t *testing.T) {
	failure := errors.New("status transport failed")
	calls := 0
	_, err := waitQueueRecovery(context.Background(), queueRecoveryStatus().ManifestHash, func(context.Context) (sharedexec.StrictIngressStatus, error) {
		calls++
		return sharedexec.StrictIngressStatus{}, failure
	})
	if !errors.Is(err, failure) || calls != 1 {
		t.Fatal("ambiguous status/session failure was retried")
	}
}
