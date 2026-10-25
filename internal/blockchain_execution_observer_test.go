package internal

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestExecutionIntervalObserverIsRequestScoped(t *testing.T) {
	start := time.Now().Add(-time.Millisecond)
	want := errors.New("evm failed")
	called := 0
	ctx := WithExecutionIntervalObserver(context.Background(), func(gotStart, end time.Time, err error) {
		called++
		if gotStart != start || end.Before(start) || err != want {
			t.Fatalf("execution interval %s..%s err=%v", gotStart, end, err)
		}
	})
	observeExecutionInterval(context.Background(), start, want)
	if called != 0 {
		t.Fatal("observer leaked to uninstrumented request")
	}
	observeExecutionInterval(ctx, start, want)
	if called != 1 {
		t.Fatalf("observer calls %d, want 1", called)
	}
}
