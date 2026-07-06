// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Core types for the compute scheduler: device classes for the
// heterogeneous fleet (phones vs IDC), verification modes, task
// specifications with the nested timeout hierarchy (lease < deadline <
// challenge window), and completion records.

package scheduler

import (
	"context"
	"errors"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/coprocessor"
)

// DeviceClass buckets workers by trust/capability profile. Phones are
// numerous but individually untrusted (quorum verification, reputation-only
// penalties); IDC providers are staked and individually accountable
// (optimistic verification, slashable).
type DeviceClass uint8

const (
	ClassPhone DeviceClass = 0
	ClassEdge  DeviceClass = 1
	ClassIDC   DeviceClass = 2
)

func (c DeviceClass) String() string {
	switch c {
	case ClassPhone:
		return "phone"
	case ClassEdge:
		return "edge"
	case ClassIDC:
		return "idc"
	default:
		return "unknown"
	}
}

// VerifyMode selects the acceptance mechanism for a task.
type VerifyMode uint8

const (
	// VerifyQuorum dispatches Redundancy replicas and accepts the strict
	// majority output. Suited to phone-class workers: no stake required,
	// disagreeing minorities take a reputation penalty only.
	VerifyQuorum VerifyMode = 0
	// VerifyOptimistic executes once on an auctioned staked provider and
	// parks the result behind a challenge window (bond + fraud-dispute,
	// resolved by referee re-execution). Suited to IDC-class workers.
	VerifyOptimistic VerifyMode = 1
)

// WorkerInfo is the scheduler-side device profile attached to a registered
// coprocessor provider.
type WorkerInfo struct {
	Addr  types.Address
	Class DeviceClass
	// Group is the sybil-dispersal domain (region/ASN/operator). Quorum
	// replicas are spread across distinct groups when the task requires it.
	Group string
}

// TaskSpec describes one deterministic compute task.
//
// Timeout hierarchy (validated at Submit): Lease bounds a single execution
// attempt; Deadline bounds the whole execution phase (all attempts,
// reassignments and quorum rounds); ChallengeWindow starts only after a
// successful optimistic execution and is not bounded by Deadline.
type TaskSpec struct {
	ProgramHash types.Hash
	Bytecode    []byte // WASM module (opaque payload to non-WASM executors)
	FuncName    string
	Input       []byte
	FuelLimit   uint64

	// MaxPrice is the escrow locked at submission and the reverse-auction
	// price ceiling. Quorum tasks split it evenly across majority members.
	MaxPrice uint64

	Deadline   time.Duration // total execution budget
	Lease      time.Duration // per-attempt budget, must be < Deadline
	HedgeDelay time.Duration // optimistic only: launch a backup copy if the
	// primary hasn't answered after this long (0 = no hedging)

	Mode            VerifyMode
	Redundancy      int  // quorum only: replica count r (odd, >= 1)
	RequireDisperse bool // quorum only: replicas must span r distinct Groups

	ChallengeWindow time.Duration // optimistic only

	Capability coprocessor.Capability
}

// TaskResult is the terminal completion record for a scheduled task.
type TaskResult struct {
	TaskID    types.Hash
	Status    coprocessor.TaskStatus
	Output    []byte
	Workers   []types.Address // the worker(s) whose output was accepted
	Err       string
	Escalated bool // quorum only: a second, larger round was needed
}

// Executor dispatches one execution attempt to a worker and returns its
// output. Implementations must honor ctx cancellation (the scheduler uses it
// for leases, hedging and shutdown). The scheduler treats any error as a
// failed attempt; ctx.DeadlineExceeded specifically marks a lease timeout.
type Executor interface {
	Execute(ctx context.Context, worker types.Address, spec *TaskSpec) ([]byte, error)
}

// Validation errors.
var (
	ErrNilSpec           = errors.New("scheduler: nil task spec")
	ErrLeaseVsDeadline   = errors.New("scheduler: lease must be positive and shorter than deadline")
	ErrBadRedundancy     = errors.New("scheduler: quorum redundancy must be a positive odd number")
	ErrNoChallengeWindow = errors.New("scheduler: optimistic mode requires a positive challenge window")
	ErrZeroPrice         = errors.New("scheduler: max price must be positive")
	ErrUnknownProvider   = errors.New("scheduler: worker is not a registered provider")
	ErrClosed            = errors.New("scheduler: closed")
	ErrTaskUnknown       = errors.New("scheduler: unknown task")
	ErrWaitTimeout       = errors.New("scheduler: wait timed out")
	ErrNotChallengeable  = errors.New("scheduler: task is not in a challengeable state")
	ErrNoReferee         = errors.New("scheduler: no referee configured")
)

// Validate checks the spec's internal consistency.
func (s *TaskSpec) Validate() error {
	if s == nil {
		return ErrNilSpec
	}
	if s.MaxPrice == 0 {
		return ErrZeroPrice
	}
	if s.Lease <= 0 || s.Deadline <= 0 || s.Lease > s.Deadline {
		return ErrLeaseVsDeadline
	}
	switch s.Mode {
	case VerifyQuorum:
		if s.Redundancy < 1 || s.Redundancy%2 == 0 {
			return ErrBadRedundancy
		}
	case VerifyOptimistic:
		if s.ChallengeWindow <= 0 {
			return ErrNoChallengeWindow
		}
	}
	return nil
}
