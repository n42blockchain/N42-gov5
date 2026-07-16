// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// JSON-RPC surface for the coprocessor (project_distributed_compute_storage_
// wiring_plan §19-25 item 1). The Service was constructed and Start()ed by the
// node but had no external door — nothing but Stop() ever touched it. This
// namespace exposes the already-implemented task/provider/verification managers
// so a submitter can post work, a provider can register and claim, and anyone
// can query status and file a fraud challenge. Read methods take value args and
// return plain maps/structs so they cross the JSON-RPC boundary cleanly.

package coprocessor

import (
	"github.com/n42blockchain/N42/common/types"
)

// API is the coprocessor_* JSON-RPC service. Register it with namespace
// "coprocessor" from the node.
type API struct {
	svc *Service
}

// NewAPI builds the RPC service over a coprocessor Service.
func NewAPI(svc *Service) *API { return &API{svc: svc} }

// RegisterProgram registers a program (verification key + name) so tasks may be
// submitted against programHash.
func (a *API) RegisterProgram(programHash types.Hash, verificationKey []byte, name string) error {
	return a.svc.RegisterProgram(programHash, verificationKey, name)
}

// SubmitTask posts a compute task against a registered program (default ZK
// tier). Returns the task ID.
func (a *API) SubmitTask(programHash types.Hash, input []byte, submitter types.Address) (types.Hash, error) {
	return a.svc.SubmitTask(programHash, input, submitter)
}

// SubmitOptimisticTask posts a task routed through the optimistic tier
// (bond + challenge window) — the tier a WASM/general provider uses. Requires a
// non-zero bond. Returns the task ID.
func (a *API) SubmitOptimisticTask(programHash types.Hash, input []byte, submitter types.Address, bond uint64) (types.Hash, error) {
	return a.svc.SubmitTaskWithTier(programHash, input, submitter, TierOptimistic, bond)
}

// RegisterProvider registers the caller as a compute provider with stake and
// capabilities. Capabilities are the numeric Capability codes (0=ZK, 1=WASM,
// 2=AI, 3=General).
func (a *API) RegisterProvider(addr types.Address, stake uint64, capabilities []int) error {
	caps := make([]Capability, 0, len(capabilities))
	for _, c := range capabilities {
		caps = append(caps, Capability(c))
	}
	return a.svc.RegisterProvider(addr, stake, caps)
}

// ClaimTask assigns an unassigned Pending task to a provider.
func (a *API) ClaimTask(taskID types.Hash, providerAddr types.Address) error {
	return a.svc.ClaimTask(taskID, providerAddr)
}

// SubmitProof submits a provider's result+proof for a task, running tiered
// verification. Returns whether it was accepted.
func (a *API) SubmitProof(taskID types.Hash, proofData, publicOutputs []byte) (bool, error) {
	return a.svc.SubmitProof(taskID, proofData, publicOutputs)
}

// SubmitChallenge files a fraud challenge against an optimistic-verified task
// within its challenge window. Returns the challenge ID.
func (a *API) SubmitChallenge(taskID types.Hash, challenger types.Address, bond uint64, reason string) (types.Hash, error) {
	return a.svc.SubmitChallenge(taskID, challenger, bond, reason)
}

// GetTaskStatus returns a task's current state for polling.
func (a *API) GetTaskStatus(taskID types.Hash) (map[string]any, error) {
	t, ok := a.svc.Tasks().GetTaskSnapshot(taskID)
	if !ok {
		return nil, ErrTaskNotFound
	}
	return map[string]any{
		"id":               t.ID.Hex(),
		"programHash":      t.ProgramHash.Hex(),
		"status":           t.Status.String(),
		"tier":             t.VerificationTier.String(),
		"bond":             t.Bond,
		"assignedProvider": t.AssignedProvider.Hex(),
		"rewardAmount":     t.RewardAmount,
		"publicOutputs":    t.PublicOutputs,
		"error":            t.Error,
	}, nil
}

// Status reports coprocessor gauges.
func (a *API) Status() map[string]any {
	return map[string]any{
		"pendingTasks":   a.svc.Tasks().PendingCount(),
		"totalTasks":     a.svc.Tasks().TotalCount(),
		"activeProviders": a.svc.Providers().ActiveCount(),
		"totalRewarded":  a.svc.Slasher().TotalRewarded(),
		"totalSlashed":   a.svc.Slasher().TotalSlashed(),
	}
}
