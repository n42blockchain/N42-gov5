// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// service.go — Service lifecycle interface used by ethel.Node.
//
// Service is the minimal Start/Stop contract every component plugged
// into a Node must satisfy. Node starts services in registration order
// and stops them in reverse, mirroring the pattern used by the messaging
// stack. Persistent services spawn goroutines in Start and honour
// ctx.Done; one-shot services run to completion synchronously and
// return nil from Stop. Keeping the contract this narrow lets the Node
// treat bootstrap, catchup and engineapi uniformly.

package ethel

import "context"

// Service is the lifecycle contract every component plugged into a Node
// must satisfy. The Node starts services in registration order and stops
// them in reverse, mirroring the pattern in internal/distributed/messaging.
//
// Implementations split into two categories:
//
//   - **Persistent services** spin up background goroutines in Start and
//     respect ctx.Done() inside them. Stop blocks until those goroutines
//     drain. Examples: torrent client, Engine API server, Caplin.
//
//   - **One-shot services** do their entire job inside Start and return
//     when finished. Stop is a no-op. Examples: state-rebuild from leaves,
//     catch-up executor.
//
// Both forms must be safe to call Stop on even if Start failed or was
// never invoked, so that Node.Stop's reverse-walk can blanket-call Stop
// without bookkeeping.
type Service interface {
	// Name returns a stable identifier used in startup/shutdown logs.
	Name() string

	// Start brings the service up. The given context is the Node's master
	// context — services should derive children from it for any background
	// goroutines they spawn.
	Start(ctx context.Context) error

	// Stop releases resources held by the service. Idempotent.
	Stop() error
}

// serviceFunc adapts a plain Start function into a Service. It is used by
// Node helpers that need to wrap one-off setup work (e.g. opening a freezer
// or running RebuildState) in the Service contract without writing a fresh
// type for every step.
type serviceFunc struct {
	name  string
	start func(ctx context.Context) error
	stop  func() error
}

func (s *serviceFunc) Name() string                   { return s.name }
func (s *serviceFunc) Start(ctx context.Context) error { return s.start(ctx) }
func (s *serviceFunc) Stop() error {
	if s.stop == nil {
		return nil
	}
	return s.stop()
}

// newServiceFunc constructs a Service from start and (optional) stop
// closures. Pass nil for stop to register a one-shot.
func newServiceFunc(name string, start func(ctx context.Context) error, stop func() error) Service {
	return &serviceFunc{name: name, start: start, stop: stop}
}
