package main

import (
	"context"
	"time"
)

// Context state is observed when the operation fails. It is diagnostic evidence,
// not proof of why an RPC was slow; the original error and failure remain intact.
type boundedDrainFailure struct {
	Operation                string `json:"operation"`
	OperationError           string `json:"operation_error"`
	ParentContextError       string `json:"parent_context_error,omitempty"`
	DrainContextError        string `json:"drain_context_error,omitempty"`
	DrainDeadlineKnown       bool   `json:"drain_deadline_known"`
	DrainDeadlineRemainingNS int64  `json:"drain_deadline_remaining_ns"`
}

func boundedDrainFailureAt(parent, drain context.Context, operation string, err error) *boundedDrainFailure {
	out := &boundedDrainFailure{Operation: operation, OperationError: err.Error()}
	if err := parent.Err(); err != nil {
		out.ParentContextError = err.Error()
	}
	if err := drain.Err(); err != nil {
		out.DrainContextError = err.Error()
	}
	if deadline, ok := drain.Deadline(); ok {
		out.DrainDeadlineKnown = true
		out.DrainDeadlineRemainingNS = time.Until(deadline).Nanoseconds()
	}
	return out
}
