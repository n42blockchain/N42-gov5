package inference

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// validWasmBytes is a minimal buffer carrying the WASM magic number, enough
// to pass LoadModel's validation without being a real executable module.
func validWasmBytes(extra ...byte) []byte {
	return append([]byte{0x00, 0x61, 0x73, 0x6d}, extra...)
}

// TestNewWASMExecutor_Defaults covers the default-size/fuel branches.
func TestNewWASMExecutor_Defaults(t *testing.T) {
	e := NewWASMExecutor(0, 0)
	if e.maxModelSize != DefaultMaxModelSize {
		t.Fatalf("maxModelSize = %d, want default %d", e.maxModelSize, DefaultMaxModelSize)
	}
	if e.fuelLimit != DefaultFuelLimit {
		t.Fatalf("fuelLimit = %d, want default %d", e.fuelLimit, DefaultFuelLimit)
	}
}

// TestWASMExecutor_LoadModel_Validation covers the empty, oversized and
// bad-magic-number rejection branches, plus the success path.
func TestWASMExecutor_LoadModel_Validation(t *testing.T) {
	e := NewWASMExecutor(16, DefaultFuelLimit)

	if err := e.LoadModel(types.HexToHash("0x01"), nil); err == nil {
		t.Fatal("LoadModel() error = nil, want error for empty bytecode")
	}

	if err := e.LoadModel(types.HexToHash("0x02"), validWasmBytes(make([]byte, 32)...)); err == nil {
		t.Fatal("LoadModel() error = nil, want error for oversized module")
	}

	if err := e.LoadModel(types.HexToHash("0x03"), []byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Fatal("LoadModel() error = nil, want error for bad magic number")
	}

	modelHash := types.HexToHash("0x04")
	if err := e.LoadModel(modelHash, validWasmBytes(0x01, 0x02)); err != nil {
		t.Fatalf("LoadModel() error = %v, want nil", err)
	}

	loaded := e.LoadedModels()
	found := false
	for _, h := range loaded {
		if h == modelHash {
			found = true
		}
	}
	if !found {
		t.Fatalf("LoadedModels() = %v, want to contain %x", loaded, modelHash)
	}
}

// TestWASMExecutor_LoadedModels_Empty covers the nil-return branch when no
// models are cached.
func TestWASMExecutor_LoadedModels_Empty(t *testing.T) {
	e := NewWASMExecutor(0, 0)
	if got := e.LoadedModels(); got != nil {
		t.Fatalf("LoadedModels() = %v, want nil", got)
	}
}

// TestWASMExecutor_UnloadModel covers removing a cached model, including the
// no-op case of unloading something never loaded.
func TestWASMExecutor_UnloadModel(t *testing.T) {
	e := NewWASMExecutor(0, 0)
	modelHash := types.HexToHash("0x05")
	if err := e.LoadModel(modelHash, validWasmBytes()); err != nil {
		t.Fatalf("LoadModel() error = %v", err)
	}

	e.UnloadModel(modelHash)
	if got := e.LoadedModels(); len(got) != 0 {
		t.Fatalf("LoadedModels() after Unload = %v, want empty", got)
	}

	// Unloading again (already gone) must not panic.
	e.UnloadModel(modelHash)
}

// TestWASMExecutor_Execute_ModelNotLoaded covers the not-loaded error
// branch.
func TestWASMExecutor_Execute_ModelNotLoaded(t *testing.T) {
	e := NewWASMExecutor(0, 0)
	_, err := e.Execute(context.Background(), types.HexToHash("0xnotloaded"), []byte("in"))
	if err == nil {
		t.Fatal("Execute() error = nil, want error for unloaded model")
	}
}

// TestWASMExecutor_Execute_ContextCanceled covers the early ctx.Done() guard
// in Execute.
func TestWASMExecutor_Execute_ContextCanceled(t *testing.T) {
	e := NewWASMExecutor(0, 0)
	modelHash := types.HexToHash("0x06")
	if err := e.LoadModel(modelHash, validWasmBytes()); err != nil {
		t.Fatalf("LoadModel() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Execute(ctx, modelHash, []byte("in"))
	if err == nil {
		t.Fatal("Execute() error = nil, want context canceled error")
	}
}

// TestWASMExecutor_Execute_Success covers the full success path, including
// determinism: identical model+input must yield identical output and
// request id.
func TestWASMExecutor_Execute_Success(t *testing.T) {
	e := NewWASMExecutor(0, 0)
	modelHash := types.HexToHash("0x07")
	if err := e.LoadModel(modelHash, validWasmBytes()); err != nil {
		t.Fatalf("LoadModel() error = %v", err)
	}

	r1, err := e.Execute(context.Background(), modelHash, []byte("input-data"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	r2, err := e.Execute(context.Background(), modelHash, []byte("input-data"))
	if err != nil {
		t.Fatalf("Execute() second call error = %v", err)
	}

	if r1.RequestID != r2.RequestID {
		t.Fatalf("RequestID not deterministic: %x != %x", r1.RequestID, r2.RequestID)
	}
	if string(r1.Output) != string(r2.Output) {
		t.Fatal("Output not deterministic across identical calls")
	}
	if r1.Confidence != 1.0 {
		t.Fatalf("Confidence = %f, want 1.0", r1.Confidence)
	}
	if r1.GasUsed == 0 {
		t.Fatal("GasUsed = 0, want > 0")
	}
}

// TestWASMExecutor_Execute_FuelLimitExceeded covers executeWASM's fuel-limit
// rejection branch by using a tiny fuel budget against a large input.
func TestWASMExecutor_Execute_FuelLimitExceeded(t *testing.T) {
	e := NewWASMExecutor(0, 500) // base cost alone (1000) already exceeds this
	modelHash := types.HexToHash("0x08")
	if err := e.LoadModel(modelHash, validWasmBytes()); err != nil {
		t.Fatalf("LoadModel() error = %v", err)
	}

	_, err := e.Execute(context.Background(), modelHash, []byte("x"))
	if err == nil {
		t.Fatal("Execute() error = nil, want fuel limit exceeded error")
	}
}
