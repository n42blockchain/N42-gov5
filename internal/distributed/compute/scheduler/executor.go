// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// LocalWASMExecutor runs task attempts in-process through the sandboxed
// WASM engine (compute/wasm): the IDC single-box case, the referee
// re-execution path, and the determinism reference for everything else.
// Remote executors (messaging-dispatched phone/IDC workers) implement the
// same Executor interface in a later slice.

package scheduler

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/distributed/compute/wasm"
)

// LocalWASMExecutor executes attempts on the local WASM engine. The worker
// address is accepted for interface compatibility but every attempt runs in
// this process. Determinism comes from the engine's sandbox: no filesystem,
// no network, no randomness, fuel-metered — same module + same input ⇒ same
// output on any conforming host, which is exactly the property quorum
// comparison and referee re-execution rely on.
type LocalWASMExecutor struct {
	engine *wasm.Engine
}

// NewLocalWASMExecutor wraps an existing WASM engine.
func NewLocalWASMExecutor(engine *wasm.Engine) *LocalWASMExecutor {
	return &LocalWASMExecutor{engine: engine}
}

// Execute satisfies the Executor interface.
func (l *LocalWASMExecutor) Execute(ctx context.Context, _ types.Address, spec *TaskSpec) ([]byte, error) {
	if l.engine == nil {
		return nil, fmt.Errorf("scheduler: wasm engine not configured")
	}
	res, err := l.engine.Execute(ctx, spec.ProgramHash, spec.Bytecode, spec.FuncName, spec.Input, spec.FuelLimit)
	if err != nil {
		return nil, err
	}
	return res.Output, nil
}
