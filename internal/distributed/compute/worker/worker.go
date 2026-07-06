// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Package worker is the device-side runtime for distributed compute (slice 2
// of docs/distributed-compute-design-2026-07-06.md): a Worker listens on a
// Transport, gates work behind an availability window (the phone
// "charging + unmetered network" policy plugs in here), executes attempts
// through a Runner, and returns results signed with its secp256k1 identity.
// RemoteExecutor is the scheduler-side counterpart implementing
// scheduler.Executor over the same Transport.

package worker

import (
	"context"
	"crypto/ecdsa"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/distributed/compute/scheduler"
)

// Runner executes one attempt on the device. It must honor ctx cancellation
// and be deterministic for identical specs (the quorum/ referee machinery
// depends on it).
type Runner interface {
	Run(ctx context.Context, spec *scheduler.TaskSpec) ([]byte, error)
}

// RunnerFunc adapts a function to the Runner interface.
type RunnerFunc func(ctx context.Context, spec *scheduler.TaskSpec) ([]byte, error)

// Run implements Runner.
func (f RunnerFunc) Run(ctx context.Context, spec *scheduler.TaskSpec) ([]byte, error) {
	return f(ctx, spec)
}

// unavailableMsg is the typed refusal a gated worker returns. The scheduler
// sees it as an instant attempt failure and reassigns without burning the
// full lease.
const unavailableMsg = "worker unavailable"

// Worker is the device-side task endpoint.
type Worker struct {
	key       *ecdsa.PrivateKey
	signKey   *ecdsa.PrivateKey // normally == key; tests may desync to forge
	addr      types.Address
	runner    Runner
	transport Transport
	available func() bool

	ctx    context.Context
	cancel context.CancelFunc
}

// NewWorker creates a worker whose identity (and payout address) is derived
// from key. It is available by default; call SetAvailability to gate it.
func NewWorker(key *ecdsa.PrivateKey, runner Runner, transport Transport) *Worker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Worker{
		key:       key,
		signKey:   key,
		addr:      crypto.PubkeyToAddress(key.PublicKey),
		runner:    runner,
		transport: transport,
		available: func() bool { return true },
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Addr returns the worker's on-network identity.
func (w *Worker) Addr() types.Address { return w.addr }

// SetAvailability installs the availability gate (e.g. the phone policy
// "charging AND on WiFi"). Must be set before Start for race-free use.
func (w *Worker) SetAvailability(f func() bool) { w.available = f }

// overrideSigningKey desyncs the signing key from the identity key.
// Test hook: lets the forged-signature acceptance test exist.
func (w *Worker) overrideSigningKey(k *ecdsa.PrivateKey) { w.signKey = k }

// Start registers the worker on the transport and begins serving requests.
func (w *Worker) Start() error {
	return w.transport.Register(w.addr, w.handle)
}

// Stop deregisters and cancels in-flight work.
func (w *Worker) Stop() {
	w.transport.Unregister(w.addr)
	w.cancel()
}

// handle serves one inbound request. Runs on the transport's delivery
// goroutine; execution stays here (one request = one goroutine already).
func (w *Worker) handle(from types.Address, payload []byte) {
	var req taskRequest
	if err := decode(payload, &req); err != nil || req.Ver != protocolVersion {
		return // undecodable/foreign traffic: drop
	}

	respond := func(output []byte, errMsg string) {
		resp := &taskResponse{
			Ver:    protocolVersion,
			ReqID:  req.ReqID,
			Worker: w.addr,
			Output: output,
			ErrMsg: errMsg,
		}
		if err := signResponse(resp, w.signKey); err != nil {
			return
		}
		b, err := encode(resp)
		if err != nil {
			return
		}
		_ = w.transport.Send(w.addr, req.ReplyTo, b)
	}

	if !w.available() {
		respond(nil, unavailableMsg)
		return
	}

	spec := req.Spec.toTaskSpec()
	ctx := w.ctx
	if req.Spec.LeaseMs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(w.ctx, time.Duration(req.Spec.LeaseMs)*time.Millisecond)
		defer cancel()
	}
	out, err := w.runner.Run(ctx, spec)
	if err != nil {
		respond(nil, err.Error())
		return
	}
	respond(out, "")
}
