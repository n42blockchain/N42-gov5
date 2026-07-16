// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Real wazero-backed implementation of the Runtime/CompiledModule interfaces
// declared in engine.go. Before this file, Runtime had zero concrete
// implementations anywhere in the repo — only a mockRuntime in wasm_test.go —
// so Engine.Execute always ran against a test double, never real WASM
// bytecode, regardless of what called it.
//
// Calling convention: a guest module's exported function must be one of:
//
//   - (i64) -> (i64): a single scalar in, single scalar out. args must be
//     exactly 8 bytes (big-endian uint64), and the result is encoded the
//     same way. Matches simple numeric test/reference modules (e.g. the
//     factorial fixture in testdata/fac.wasm).
//
//   - (i32 ptr, i32 len) -> (i32 outPtr, i32 outLen): byte-blob in, byte-blob
//     out via the module's exported linear memory. Host writes args at
//     memory offset 0 before calling, then reads outLen bytes starting at
//     outPtr from the (possibly grown) memory after the call returns. This
//     is the convention real inference models (arbitrary-length input/output)
//     should use.
//
// Any other exported signature is rejected — this is an intentionally small,
// explicit ABI rather than a general host/guest marshaling layer.
//
// Fuel/gas: wazero has no per-instruction fuel-metering primitive (unlike
// wasmtime's Store.set_fuel) — this is a genuine gap, not an oversight here.
// Execution is bounded instead by a real, enforced wall-clock deadline
// (wazero.RuntimeConfig.WithCloseOnContextDone + context.WithTimeout), and
// "gas used" is reported as elapsed time scaled by gasPerSecond. This is a
// real, working resource bound — but it is NOT deterministic per-instruction
// accounting, and callers must not treat gasUsed as reproducible across runs
// the way EVM gas or wasmtime fuel would be.
package wasm

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// defaultGasPerSecond converts elapsed wall-clock execution time into the
// coarse "gas used" figure returned alongside a real fuel limit, since wazero
// has no native fuel counter to read. 1 fuel unit ≈ 1 microsecond gives
// DefaultFuelLimit (10_000_000) a 10-second execution budget — generous
// enough for real model inference, not so long a hung module stalls the
// caller. Arbitrary but stable; callers needing a different scale should
// convert the returned gasUsed themselves.
const defaultGasPerSecond = 1_000_000

// wazeroRuntime implements Runtime using tetratelabs/wazero.
type wazeroRuntime struct {
	rt wazero.Runtime
}

// NewWazeroRuntime creates a Runtime backed by a real wazero interpreter/
// compiler instance. No WASI, no filesystem, no network — guest modules get
// only what they import explicitly (nothing, in the current sandbox policy),
// matching the "deterministic execution only" contract in this package's doc.
func NewWazeroRuntime(ctx context.Context) Runtime {
	cfg := wazero.NewRuntimeConfig().WithCloseOnContextDone(true)
	return &wazeroRuntime{rt: wazero.NewRuntimeWithConfig(ctx, cfg)}
}

func (w *wazeroRuntime) Compile(ctx context.Context, wasmBytes []byte) (CompiledModule, error) {
	compiled, err := w.rt.CompileModule(ctx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("wazero: compile: %w", err)
	}
	return &wazeroCompiledModule{rt: w.rt, compiled: compiled}, nil
}

func (w *wazeroRuntime) Close(ctx context.Context) error {
	return w.rt.Close(ctx)
}

// wazeroCompiledModule implements CompiledModule. Each Execute call
// instantiates a fresh module instance — isolating linear memory and globals
// between calls (no state leaks between concurrent or repeated executions of
// the same compiled module) — at the cost of re-instantiation overhead per
// call, which is acceptable for the deterministic, sandboxed inference use
// case this executes under.
type wazeroCompiledModule struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
}

func (m *wazeroCompiledModule) Execute(ctx context.Context, funcName string, args []byte, fuel uint64) ([]byte, uint64, error) {
	deadline := time.Duration(fuel) * time.Second / defaultGasPerSecond
	if deadline <= 0 {
		deadline = time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	instance, err := m.rt.InstantiateModule(execCtx, m.compiled, wazero.NewModuleConfig())
	if err != nil {
		return nil, 0, fmt.Errorf("wazero: instantiate: %w", err)
	}
	defer instance.Close(execCtx)

	fn := instance.ExportedFunction(funcName)
	if fn == nil {
		return nil, 0, fmt.Errorf("wazero: function %q not exported", funcName)
	}
	def := fn.Definition()
	params, results := def.ParamTypes(), def.ResultTypes()

	start := time.Now()
	var out []byte
	switch {
	case len(params) == 1 && params[0] == api.ValueTypeI64 &&
		len(results) == 1 && results[0] == api.ValueTypeI64:
		out, err = executeI64Scalar(execCtx, fn, args)
	case len(params) == 2 && params[0] == api.ValueTypeI32 && params[1] == api.ValueTypeI32 &&
		len(results) == 2 && results[0] == api.ValueTypeI32 && results[1] == api.ValueTypeI32:
		out, err = executeBytePtrLen(execCtx, instance, fn, args)
	default:
		err = fmt.Errorf("wazero: unsupported signature for %q (params=%v results=%v)", funcName, params, results)
	}
	if err != nil {
		return nil, 0, err
	}

	gasUsed := uint64(time.Since(start).Seconds() * defaultGasPerSecond)
	if gasUsed > fuel {
		gasUsed = fuel
	}
	return out, gasUsed, nil
}

func executeI64Scalar(ctx context.Context, fn api.Function, args []byte) ([]byte, error) {
	if len(args) != 8 {
		return nil, fmt.Errorf("wazero: i64 scalar function requires 8-byte input, got %d", len(args))
	}
	results, err := fn.Call(ctx, binary.BigEndian.Uint64(args))
	if err != nil {
		return nil, fmt.Errorf("wazero: call: %w", err)
	}
	out := make([]byte, 8)
	binary.BigEndian.PutUint64(out, results[0])
	return out, nil
}

func executeBytePtrLen(ctx context.Context, instance api.Module, fn api.Function, args []byte) ([]byte, error) {
	mem := instance.Memory()
	if mem == nil {
		return nil, fmt.Errorf("wazero: module has no exported memory")
	}
	if uint64(len(args)) > uint64(mem.Size()) {
		return nil, fmt.Errorf("wazero: input %d bytes exceeds guest memory size %d", len(args), mem.Size())
	}
	if !mem.Write(0, args) {
		return nil, fmt.Errorf("wazero: failed to write input to guest memory")
	}
	results, err := fn.Call(ctx, 0, uint64(len(args)))
	if err != nil {
		return nil, fmt.Errorf("wazero: call: %w", err)
	}
	outPtr, outLen := uint32(results[0]), uint32(results[1])
	data, ok := mem.Read(outPtr, outLen)
	if !ok {
		return nil, fmt.Errorf("wazero: failed to read output from guest memory (ptr=%d len=%d)", outPtr, outLen)
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (m *wazeroCompiledModule) Close(ctx context.Context) error {
	return m.compiled.Close(ctx)
}
