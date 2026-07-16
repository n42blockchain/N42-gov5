// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Reference compute provider (project_distributed_compute_storage_wiring_plan
// §19-25 item 2). The coprocessor's engine — task manager, marketplace,
// tiered verifier, slashing — was fully implemented but had no provider ever
// executing work end to end. This is that missing piece: a provider that
// registers with stake, claims (or is assigned) a task, runs the task's WASM
// program on the real wazero engine (the P0-wired execution backend), and
// submits the result through the optimistic tier (bond + challenge window),
// where settlement rewards it on finalize.
//
// It is deliberately minimal and self-contained so the whole submit → claim →
// execute → verify → settle loop can be exercised in one test, and so a node
// that opts in to being a provider has a working default executor.

package coprocessor

import (
	"context"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/compute/wasm"
)

// ReferenceProvider executes coprocessor tasks with the WASM engine and
// submits results. One provider identity (addr) backed by one engine.
type ReferenceProvider struct {
	svc     *Service
	addr    types.Address
	engine  *wasm.Engine
	funcName string

	mu       sync.RWMutex
	programs map[types.Hash][]byte // programHash → WASM bytecode
}

// NewReferenceProvider builds a provider bound to a service and identity. The
// engine executes every claimed task; funcName is the guest export invoked
// (e.g. "run"). Register the provider with the service separately (stake +
// capabilities) before serving tasks.
func NewReferenceProvider(svc *Service, addr types.Address, engine *wasm.Engine, funcName string) *ReferenceProvider {
	return &ReferenceProvider{
		svc:      svc,
		addr:     addr,
		engine:   engine,
		funcName: funcName,
		programs: make(map[types.Hash][]byte),
	}
}

// RegisterBytecode maps a program hash to the WASM bytecode this provider runs
// for tasks against that program. In production the bytecode would be fetched
// from CAS by the program's registered hash; here it is supplied directly.
func (p *ReferenceProvider) RegisterBytecode(programHash types.Hash, bytecode []byte) {
	p.mu.Lock()
	p.programs[programHash] = bytecode
	p.mu.Unlock()
}

// Serve runs one task to completion from this provider's side: claim it (if
// unassigned), execute the program on its input, and submit the result. The
// task must already be assigned to this provider or be an unassigned Pending
// task this provider is allowed to claim. Returns the execution output.
func (p *ReferenceProvider) Serve(ctx context.Context, taskID types.Hash, gasLimit uint64) ([]byte, error) {
	task, ok := p.svc.Tasks().GetTaskSnapshot(taskID)
	if !ok {
		return nil, ErrTaskNotFound
	}
	// Claim if nobody holds it yet.
	if task.AssignedProvider == (types.Address{}) {
		if err := p.svc.ClaimTask(taskID, p.addr); err != nil {
			return nil, fmt.Errorf("claim: %w", err)
		}
	} else if task.AssignedProvider != p.addr {
		return nil, fmt.Errorf("task %s assigned to another provider", taskID.Hex()[:10])
	}

	p.mu.RLock()
	bytecode, ok := p.programs[task.ProgramHash]
	p.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no bytecode registered for program %s", task.ProgramHash.Hex()[:10])
	}

	res, err := p.engine.Execute(ctx, task.ProgramHash, bytecode, p.funcName, task.Input, gasLimit)
	if err != nil {
		return nil, fmt.Errorf("wasm execute: %w", err)
	}
	// The optimistic tier stores the claimed result as the "proof"; a
	// challenger who recomputes a different output wins the challenge.
	if _, err := p.svc.SubmitProof(taskID, res.Output, res.Output); err != nil {
		return nil, fmt.Errorf("submit proof: %w", err)
	}
	return res.Output, nil
}

// Address returns the provider's identity.
func (p *ReferenceProvider) Address() types.Address { return p.addr }
