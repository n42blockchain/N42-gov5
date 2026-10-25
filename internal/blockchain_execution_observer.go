package internal

import (
	"context"
	"time"
)

// ExecutionIntervalObserver observes the actual BuildParallel call. It is
// scoped to one owner request; the observer must be bounded and nonblocking.
type ExecutionIntervalObserver func(start, end time.Time, err error)

type executionObserverKey struct{}

func WithExecutionIntervalObserver(ctx context.Context, observer ExecutionIntervalObserver) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, executionObserverKey{}, observer)
}

func startExecutionInterval(ctx context.Context) time.Time {
	if _, ok := ctx.Value(executionObserverKey{}).(ExecutionIntervalObserver); ok {
		return time.Now()
	}
	return time.Time{}
}

func observeExecutionInterval(ctx context.Context, start time.Time, err error) {
	if start.IsZero() {
		return
	}
	if observer, ok := ctx.Value(executionObserverKey{}).(ExecutionIntervalObserver); ok {
		observer(start, time.Now(), err)
	}
}

// CanonicalWriterObserver distinguishes the enclosing DB.Update call from
// its callback, which begins only after the writer transaction is acquired.
// It is scoped to one commit and must not block the transaction.
type CanonicalWriterObserver func(stage string, at time.Time)
