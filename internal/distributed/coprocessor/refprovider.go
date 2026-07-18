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
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/compute/wasm"
	"github.com/n42blockchain/N42/log"
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
	// This provider submits its raw WASM output as the proof (proofData ==
	// publicOutputs), which is exactly what the optimistic tier verifies. A
	// ZK/TEE task expects a proof envelope, not raw output, so serving one
	// here would submit garbage and the verifier would drive the task to
	// Failed. Only serve optimistic-tier tasks; leave the rest untouched.
	if task.VerificationTier != TierOptimistic {
		return nil, fmt.Errorf("task %s tier %s not servable by WASM provider", taskID.Hex()[:10], task.VerificationTier)
	}

	// Claim atomically if nobody holds it yet.
	claimed := false
	if task.AssignedProvider == (types.Address{}) {
		if err := p.svc.Tasks().ClaimIfPending(taskID, p.addr, 0); err != nil {
			return nil, fmt.Errorf("claim: %w", err)
		}
		claimed = true
	} else if task.AssignedProvider != p.addr {
		return nil, fmt.Errorf("task %s assigned to another provider", taskID.Hex()[:10])
	}

	// From here on, any failure before a successful proof submission must
	// release the claim we just took — otherwise the task sits Pending with
	// our address on it until it expires, and slashExpiredAssignments slashes
	// us for a task we never could have completed (e.g. malformed input the
	// program rejects). Release only what we claimed this call.
	release := func() {
		if claimed {
			_ = p.svc.Tasks().ReleaseAssignment(taskID, p.addr)
		}
	}

	bytecode, ok := p.bytecodeFor(task.ProgramHash)
	if !ok {
		release()
		return nil, fmt.Errorf("no bytecode registered for program %s", task.ProgramHash.Hex()[:10])
	}

	res, err := p.engine.Execute(ctx, task.ProgramHash, bytecode, p.funcName, task.Input, gasLimit)
	if err != nil {
		release()
		return nil, fmt.Errorf("wasm execute: %w", err)
	}
	// The optimistic tier stores the claimed result as the "proof"; a
	// challenger who recomputes a different output wins the challenge.
	if _, err := p.svc.SubmitProof(taskID, res.Output, res.Output); err != nil {
		release()
		return nil, fmt.Errorf("submit proof: %w", err)
	}
	return res.Output, nil
}

// Address returns the provider's identity.
func (p *ReferenceProvider) Address() types.Address { return p.addr }

// bytecodeFor resolves a program's bytecode: locally supplied bytecode wins,
// otherwise fall back to the bytecode attached to the program registration
// (hash-verified by Registry.SetBytecode).
func (p *ReferenceProvider) bytecodeFor(programHash types.Hash) ([]byte, bool) {
	p.mu.RLock()
	bytecode, ok := p.programs[programHash]
	p.mu.RUnlock()
	if ok {
		return bytecode, true
	}
	return p.svc.Registry().Bytecode(programHash)
}

// AttachProvider runs prov as this node's resident compute provider: a
// background loop polls for unassigned optimistic-tier Pending tasks whose
// program bytecode is available (locally or via the program registry) and
// serves them. The loop lives under the service's lifecycle — Stop()
// terminates it and then calls cleanup (e.g. closing the provider's WASM
// engine). This is the "node opts in to being a provider" switch the wiring
// plan left open; register the provider (stake + capabilities) before attaching.
func (s *Service) AttachProvider(prov *ReferenceProvider, poll time.Duration, gasLimit uint64, cleanup func()) {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if cleanup != nil {
			defer cleanup()
		}
		// Tasks whose execution failed on this provider (e.g. malformed
		// input the program rejects). We release the claim on failure, so
		// without this the loop would re-claim and re-fail the same task
		// every poll. Bounded by task lifetime — pruned entries never
		// re-appear in ListByStatus(TaskPending).
		failed := make(map[types.Hash]struct{})
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.serveClaimable(prov, gasLimit, failed)
			}
		}
	}()
	log.Info("Coprocessor resident provider attached",
		"provider", prov.Address().Hex(), "poll", poll)
}

// serveClaimable runs one poll round: serve every unassigned optimistic-tier
// Pending task this provider has bytecode for and has not already failed on.
// Non-optimistic tiers, tasks without bytecode, and previously-failed tasks
// are left alone; failures are logged at debug level to keep a busy queue
// from flooding the logs.
func (s *Service) serveClaimable(prov *ReferenceProvider, gasLimit uint64, failed map[types.Hash]struct{}) {
	for _, task := range s.tasks.ListByStatus(TaskPending) {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		snap, ok := s.tasks.GetTaskSnapshot(task.ID)
		if !ok || snap.AssignedProvider != (types.Address{}) {
			continue
		}
		if snap.VerificationTier != TierOptimistic {
			continue
		}
		if _, done := failed[snap.ID]; done {
			continue
		}
		if _, ok := prov.bytecodeFor(snap.ProgramHash); !ok {
			continue
		}
		if _, err := prov.Serve(s.ctx, snap.ID, gasLimit); err != nil {
			failed[snap.ID] = struct{}{}
			log.Debug("Resident provider serve failed",
				"task", snap.ID.Hex()[:10], "err", err)
		}
	}
}
