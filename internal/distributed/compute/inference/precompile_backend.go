// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// PrecompileBackend adapts InferenceService to vm.InferenceBackend, the
// interface internal/vm/contracts_ai_inference.go's 0x0301 precompile calls
// through. Before this file (and the wazero wiring in executor_wazero.go),
// vm.SetInferenceBackend was called only from contracts_ai_inference_test.go
// — never from node startup — so getInferenceBackend() always returned nil
// and every requestInference call failed with errAIInferenceNoBackend.

package inference

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// executionTimeout bounds the background goroutine PrecompileBackend spawns
// per request — independent of the wasm executor's own fuel-derived deadline,
// this is a second, coarser backstop against a leaked goroutine if the
// executor's own bound is somehow bypassed.
const executionTimeout = 60 * time.Second

// PrecompileBackend bridges the EVM-facing precompile contract to
// InferenceService. SubmitRequest resolves inputCAS to bytes, submits
// synchronously (fast, deterministic — safe inside a precompile Run() call
// during block execution), then dispatches the actual model execution on a
// background goroutine so Run() returns immediately; getResult (a separate,
// cheap precompile call) polls status/output later. This mirrors the opML
// pattern already documented in service.go: submit now, verify/finalize
// later.
type PrecompileBackend struct {
	svc      *InferenceService
	casLoad  func(types.Hash) ([]byte, error)
	casStore func([]byte) (types.Hash, error)
}

// NewPrecompileBackend wires an InferenceService (with its executor already
// set via SetExecutor) to the precompile-facing CAS load/store callbacks.
func NewPrecompileBackend(svc *InferenceService, casLoad func(types.Hash) ([]byte, error), casStore func([]byte) (types.Hash, error)) *PrecompileBackend {
	return &PrecompileBackend{svc: svc, casLoad: casLoad, casStore: casStore}
}

// SubmitRequest implements vm.InferenceBackend.
func (b *PrecompileBackend) SubmitRequest(modelHash types.Hash, inputCAS types.Hash, _ uint8, caller types.Address) (types.Hash, error) {
	if b.casLoad == nil {
		return types.Hash{}, fmt.Errorf("inference precompile backend: no CAS load configured")
	}
	input, err := b.casLoad(inputCAS)
	if err != nil {
		return types.Hash{}, fmt.Errorf("inference precompile backend: cas load %s: %w", inputCAS.Hex(), err)
	}

	requestID, err := b.svc.SubmitRequest(modelHash, input, caller)
	if err != nil {
		return types.Hash{}, err
	}

	// Off the critical path: Run() must return quickly and deterministically
	// during block execution. The model may take real time to execute; the
	// caller polls getResult (a separate, cheap precompile call) for the
	// outcome.
	go b.executeAsync(requestID)

	return requestID, nil
}

func (b *PrecompileBackend) executeAsync(requestID types.Hash) {
	ctx, cancel := context.WithTimeout(context.Background(), executionTimeout)
	defer cancel()

	result, err := b.svc.Execute(ctx, requestID)
	if err != nil || result == nil {
		return // status already recorded as Failed by InferenceService.Execute
	}
	if b.casStore != nil {
		// Make the output genuinely retrievable by the hash GetResult reports
		// (InferenceService.Execute already cached outputCAS =
		// keccak256(result.Output); storing under the same hash function keeps
		// the two consistent without needing to touch the cache here).
		_, _ = b.casStore(result.Output)
	}
}

// GetResult implements vm.InferenceBackend.
func (b *PrecompileBackend) GetResult(requestID types.Hash) (status uint8, outputCAS types.Hash, err error) {
	if cached, ok := b.svc.GetCachedResult(requestID); ok {
		return uint8(cached.Status), cached.OutputCAS, nil
	}
	// Not finished (or not found) yet — report the tracked status with a
	// zero output hash so callers can keep polling instead of erroring.
	_, _, st, gerr := b.svc.GetRequest(requestID)
	if gerr != nil {
		return 0, types.Hash{}, gerr
	}
	return uint8(st), types.Hash{}, nil
}

// GetModel implements vm.InferenceBackend.
func (b *PrecompileBackend) GetModel(modelHash types.Hash) (name, format, capabilities string, err error) {
	m, ok := b.svc.Models().Get(modelHash)
	if !ok {
		return "", "", "", fmt.Errorf("inference precompile backend: model %s not registered", modelHash.Hex())
	}
	return m.Name, m.Format.String(), strings.Join(m.Capabilities, ","), nil
}

// ListModels implements vm.InferenceBackend.
func (b *PrecompileBackend) ListModels(capability string) ([]types.Hash, error) {
	var models []*Model
	if capability == "" {
		models = b.svc.Models().List()
	} else {
		models = b.svc.Models().FindByCapability(capability)
	}
	hashes := make([]types.Hash, len(models))
	for i, m := range models {
		hashes[i] = m.Hash
	}
	return hashes, nil
}
