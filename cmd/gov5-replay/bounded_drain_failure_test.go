package main

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"
)

func TestDrainDeadlineCanReturnRawValidatorStatusError(t *testing.T) {
	parent := context.Background()
	drain, cancelDrain := context.WithDeadline(parent, time.Now().Add(-time.Second))
	defer cancelDrain()
	probe, cancelProbe := context.WithTimeout(drain, 2*time.Second)
	defer cancelProbe()
	// Same ordering as bounded_window.go: observe returns before drain.Err is checked.
	rpcErr := &url.Error{Op: "Post", URL: "http://unix/v1/status", Err: probe.Err()}
	const want = `Post "http://unix/v1/status": context deadline exceeded`
	if rpcErr.Error() != want || !errors.Is(rpcErr, context.DeadlineExceeded) {
		t.Fatalf("raw error differs: %v", rpcErr)
	}
	diagnostic := boundedDrainFailureAt(parent, drain, "validator_status", rpcErr)
	if diagnostic.OperationError != want || diagnostic.ParentContextError != "" || diagnostic.DrainContextError != context.DeadlineExceeded.Error() || !diagnostic.DrainDeadlineKnown || diagnostic.DrainDeadlineRemainingNS >= 0 {
		t.Fatalf("drain context observation differs: %+v", diagnostic)
	}
	t.Logf("original_error=%q drain_context_error=%q parent_context_error=%q", rpcErr.Error(), diagnostic.DrainContextError, diagnostic.ParentContextError)
}

func TestOperationDeadlineDoesNotImplyDrainDeadline(t *testing.T) {
	drain, stop := context.WithTimeout(context.Background(), time.Hour)
	defer stop()
	probe, cancel := context.WithDeadline(drain, time.Now().Add(-time.Second))
	defer cancel()
	err := &url.Error{Op: "Post", URL: "http://unix/v1/status", Err: probe.Err()}
	diagnostic := boundedDrainFailureAt(context.Background(), drain, "validator_status", err)
	if diagnostic.DrainContextError != "" || diagnostic.ParentContextError != "" || diagnostic.DrainDeadlineRemainingNS <= 0 {
		t.Fatalf("probe deadline mislabeled as drain deadline: %+v", diagnostic)
	}
}

func TestParentCancellationAndTransportErrorRemainDistinct(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	drain, stop := context.WithTimeout(parent, time.Hour)
	defer stop()
	cancel()
	diagnostic := boundedDrainFailureAt(parent, drain, "owner_status", context.Canceled)
	if diagnostic.ParentContextError != context.Canceled.Error() || diagnostic.DrainContextError != context.Canceled.Error() {
		t.Fatalf("parent cancellation lost: %+v", diagnostic)
	}
	sentinel := errors.New("transport failed")
	diagnostic = boundedDrainFailureAt(context.Background(), context.Background(), "validator_status", sentinel)
	if diagnostic.OperationError != sentinel.Error() || diagnostic.ParentContextError != "" || diagnostic.DrainContextError != "" || diagnostic.DrainDeadlineKnown {
		t.Fatalf("transport error mislabeled: %+v", diagnostic)
	}
}
