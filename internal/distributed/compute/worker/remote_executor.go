// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// RemoteExecutor carries scheduler execution attempts over a Transport to
// remote workers, awaits their signed responses, and verifies the signature
// binds (request, worker, output) before returning. Implements
// scheduler.Executor, so the slice-1 pipeline (lease, hedge, reassign,
// quorum, challenge) drives remote fleets unchanged.

package worker

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/compute/scheduler"
)

// RemoteExecutor is the scheduler-side transport client.
type RemoteExecutor struct {
	transport Transport
	replyAddr types.Address

	mu      sync.Mutex
	pending map[types.Hash]chan *taskResponse // reqID → waiter
	started bool
}

// NewRemoteExecutor creates an executor with a fresh ephemeral reply address.
func NewRemoteExecutor(transport Transport) *RemoteExecutor {
	var reply types.Address
	_, _ = rand.Read(reply[:])
	return &RemoteExecutor{
		transport: transport,
		replyAddr: reply,
		pending:   make(map[types.Hash]chan *taskResponse),
	}
}

// ensureStarted lazily registers the reply endpoint on first use.
func (r *RemoteExecutor) ensureStarted() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return nil
	}
	if err := r.transport.Register(r.replyAddr, r.onResponse); err != nil {
		return err
	}
	r.started = true
	return nil
}

// onResponse routes an inbound response to its waiter; responses for
// abandoned requests (waiter already gone) are dropped on the floor.
func (r *RemoteExecutor) onResponse(_ types.Address, payload []byte) {
	var resp taskResponse
	if err := decode(payload, &resp); err != nil || resp.Ver != protocolVersion {
		return
	}
	r.mu.Lock()
	ch, ok := r.pending[resp.ReqID]
	if ok {
		delete(r.pending, resp.ReqID)
	}
	r.mu.Unlock()
	if ok {
		ch <- &resp // buffered(1): never blocks
	}
}

// Execute implements scheduler.Executor: one attempt on one remote worker.
// Silence is resolved by ctx (the scheduler's lease); refusals and runner
// errors come back as errors so the scheduler reassigns immediately.
func (r *RemoteExecutor) Execute(ctx context.Context, workerAddr types.Address, spec *scheduler.TaskSpec) ([]byte, error) {
	if err := r.ensureStarted(); err != nil {
		return nil, err
	}

	var reqID types.Hash
	if _, err := rand.Read(reqID[:]); err != nil {
		return nil, fmt.Errorf("worker: request id: %w", err)
	}
	req := &taskRequest{
		Ver:     protocolVersion,
		ReqID:   reqID,
		ReplyTo: r.replyAddr,
		Spec:    toWireSpec(spec),
	}
	payload, err := encode(req)
	if err != nil {
		return nil, err
	}

	ch := make(chan *taskResponse, 1)
	r.mu.Lock()
	r.pending[reqID] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, reqID)
		r.mu.Unlock()
	}()

	if err := r.transport.Send(r.replyAddr, workerAddr, payload); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		if err := verifyResponse(resp, workerAddr); err != nil {
			return nil, err
		}
		if resp.ErrMsg != "" {
			return nil, fmt.Errorf("worker %s: %s", workerAddr.Hex(), resp.ErrMsg)
		}
		return resp.Output, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
