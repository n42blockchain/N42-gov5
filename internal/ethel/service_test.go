// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"errors"
	"testing"
)

func TestServiceFuncNameStartStop(t *testing.T) {
	var started, stopped bool
	svc := newServiceFunc("widget",
		func(ctx context.Context) error { started = true; return nil },
		func() error { stopped = true; return nil },
	)

	if got := svc.Name(); got != "widget" {
		t.Fatalf("Name() = %q, want %q", got, "widget")
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("start closure was not invoked")
	}
	if err := svc.Stop(); err != nil {
		t.Fatal(err)
	}
	if !stopped {
		t.Fatal("stop closure was not invoked")
	}
}

func TestServiceFuncNilStopIsNoop(t *testing.T) {
	svc := newServiceFunc("one-shot", func(ctx context.Context) error { return nil }, nil)
	if err := svc.Stop(); err != nil {
		t.Fatalf("Stop() with nil stop closure = %v, want nil", err)
	}
}

func TestServiceFuncPropagatesStartAndStopErrors(t *testing.T) {
	startErr := errors.New("start failed")
	stopErr := errors.New("stop failed")
	svc := newServiceFunc("flaky",
		func(ctx context.Context) error { return startErr },
		func() error { return stopErr },
	)
	if err := svc.Start(context.Background()); !errors.Is(err, startErr) {
		t.Fatalf("Start() = %v, want %v", err, startErr)
	}
	if err := svc.Stop(); !errors.Is(err, stopErr) {
		t.Fatalf("Stop() = %v, want %v", err, stopErr)
	}
}
