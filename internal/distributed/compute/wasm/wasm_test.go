package wasm

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// mockRuntime implements Runtime for testing.
type mockRuntime struct{}

func (r *mockRuntime) Compile(_ context.Context, wasm []byte) (CompiledModule, error) {
	return &mockCompiledModule{bytecode: wasm}, nil
}

func (r *mockRuntime) Close(_ context.Context) error { return nil }

type mockCompiledModule struct {
	bytecode []byte
}

func (m *mockCompiledModule) Execute(_ context.Context, funcName string, args []byte, fuel uint64) ([]byte, uint64, error) {
	// Echo back the function name + args, consume half the fuel
	output := append([]byte(funcName+":"), args...)
	return output, fuel / 2, nil
}

func (m *mockCompiledModule) Close(_ context.Context) error { return nil }

func TestEngineExecute(t *testing.T) {
	engine := NewEngine(&mockRuntime{}, 256, 30, 1.0)
	defer engine.Close(context.Background())

	hash := types.HexToHash("0x1234")
	bytecode := []byte{0x00, 0x61, 0x73, 0x6d} // mock WASM magic

	result, err := engine.Execute(context.Background(), hash, bytecode, "compute", []byte("input"), 1000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if string(result.Output) != "compute:input" {
		t.Fatalf("output = %q, want %q", string(result.Output), "compute:input")
	}
	if result.GasUsed != 500 {
		t.Fatalf("gasUsed = %d, want 500", result.GasUsed)
	}
}

func TestEngineCache(t *testing.T) {
	engine := NewEngine(&mockRuntime{}, 256, 30, 1.0)
	defer engine.Close(context.Background())

	hash := types.HexToHash("0x5678")
	bytecode := []byte{0x00, 0x61, 0x73, 0x6d}

	// First execution → compile + cache
	engine.Execute(context.Background(), hash, bytecode, "f", nil, 100)
	if engine.CacheSize() != 1 {
		t.Fatalf("cache size = %d, want 1", engine.CacheSize())
	}

	// Second execution → cache hit
	engine.Execute(context.Background(), hash, bytecode, "f", nil, 100)
	if engine.CacheSize() != 1 {
		t.Fatalf("cache size = %d, want 1 (should not grow)", engine.CacheSize())
	}

	// Invalidate
	engine.Invalidate(hash)
	if engine.CacheSize() != 0 {
		t.Fatalf("cache size = %d, want 0 after invalidation", engine.CacheSize())
	}
}

func TestEngineGasMultiplier(t *testing.T) {
	engine := NewEngine(&mockRuntime{}, 256, 30, 2.0) // 2x multiplier
	defer engine.Close(context.Background())

	hash := types.HexToHash("0xaabb")
	result, err := engine.Execute(context.Background(), hash, []byte{0x00}, "f", nil, 2000)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	// Gas limit 2000 / multiplier 2.0 = 1000 fuel, module consumes 500 fuel
	// Gas used = 500 * 2.0 = 1000
	if result.GasUsed != 1000 {
		t.Fatalf("gasUsed = %d, want 1000", result.GasUsed)
	}
}

func TestModuleRegistry(t *testing.T) {
	r := NewModuleRegistry()

	hash, err := r.Register("test-module", []byte("wasm-bytecode"), []string{"compute", "init"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	m, ok := r.Get(hash)
	if !ok {
		t.Fatal("module not found")
	}
	if m.Name != "test-module" {
		t.Fatalf("name = %q, want %q", m.Name, "test-module")
	}
	if m.Size != uint64(len("wasm-bytecode")) {
		t.Fatalf("size = %d, want %d", m.Size, len("wasm-bytecode"))
	}

	// Idempotent register
	hash2, _ := r.Register("test-module-dup", []byte("wasm-bytecode"), nil)
	if hash2 != hash {
		t.Fatal("same bytecode should produce same hash")
	}
	if r.Count() != 1 {
		t.Fatalf("count = %d, want 1", r.Count())
	}

	// HasExport
	if !r.HasExport(hash, "compute") {
		t.Fatal("should have export 'compute'")
	}
	if r.HasExport(hash, "nonexistent") {
		t.Fatal("should not have export 'nonexistent'")
	}
}

func TestModuleRegistryEmptyBytecode(t *testing.T) {
	r := NewModuleRegistry()
	_, err := r.Register("empty", nil, nil)
	if err == nil {
		t.Fatal("expected error on empty bytecode")
	}
}

func TestGasTable(t *testing.T) {
	g := DefaultGasTable()

	kCost := g.Keccak256Cost(100)
	expected := g.HostKeccak256 + g.HostKeccak256B*100
	if kCost != expected {
		t.Fatalf("keccak256 cost = %d, want %d", kCost, expected)
	}

	logCost := g.LogMsgCost(50)
	expected = g.HostLogMsg + g.HostLogMsgB*50
	if logCost != expected {
		t.Fatalf("log cost = %d, want %d", logCost, expected)
	}

	growCost := g.MemoryGrowCost(10)
	expected = g.MemoryGrow + g.MemoryPageCost*10
	if growCost != expected {
		t.Fatalf("grow cost = %d, want %d", growCost, expected)
	}
}

func TestHostFunctions(t *testing.T) {
	hf := NewHostFunctions()

	// List default functions
	names := hf.List()
	if len(names) != 4 {
		t.Fatalf("host functions = %d, want 4", len(names))
	}

	// Test keccak256
	keccak, ok := hf.Get("keccak256")
	if !ok {
		t.Fatal("keccak256 not found")
	}
	result, cost, err := keccak.Handler([]byte("hello"))
	if err != nil {
		t.Fatalf("keccak256: %v", err)
	}
	if len(result) != 32 {
		t.Fatalf("keccak256 output length = %d, want 32", len(result))
	}
	if cost == 0 {
		t.Fatal("keccak256 cost should be > 0")
	}

	// Test log_msg
	logFn, _ := hf.Get("log_msg")
	logFn.Handler([]byte("hello world"))
	logs := hf.LogOutput()
	if len(logs) != 1 || logs[0] != "hello world" {
		t.Fatalf("log output = %v, want [hello world]", logs)
	}
	// Second call should be empty
	logs = hf.LogOutput()
	if len(logs) != 0 {
		t.Fatalf("log output should be empty after drain, got %v", logs)
	}

	// Test cas_load without callback
	casLoad, _ := hf.Get("cas_load")
	_, _, err = casLoad.Handler(make([]byte, 32))
	if err == nil {
		t.Fatal("cas_load should fail without callback")
	}

	// Set CAS callbacks
	hf.SetCASCallbacks(
		func(hash types.Hash) ([]byte, error) { return []byte("data"), nil },
		func(data []byte) (types.Hash, error) { return types.HexToHash("0xaa"), nil },
	)

	result, _, err = casLoad.Handler(make([]byte, 32))
	if err != nil {
		t.Fatalf("cas_load with callback: %v", err)
	}
	if string(result) != "data" {
		t.Fatalf("cas_load result = %q, want %q", string(result), "data")
	}
}

func TestEngineNilRuntime(t *testing.T) {
	// Engine with nil runtime should return an error on Execute
	engine := NewEngine(nil, 256, 30, 1.0)

	hash := types.HexToHash("0xccdd")
	_, err := engine.Execute(context.Background(), hash, []byte{0x00}, "main", nil, 1000)
	if err == nil {
		t.Fatal("expected error when runtime is nil")
	}
}

func TestHostFunctionRegisterCustom(t *testing.T) {
	hf := NewHostFunctions()

	customCalled := false
	hf.Register(&HostFunc{
		Name: "custom_op",
		Handler: func(args []byte) ([]byte, uint64, error) {
			customCalled = true
			return []byte("custom-result"), 42, nil
		},
	})

	fn, ok := hf.Get("custom_op")
	if !ok {
		t.Fatal("custom_op not found after registration")
	}

	result, cost, err := fn.Handler([]byte("arg"))
	if err != nil {
		t.Fatalf("custom_op handler: %v", err)
	}
	if string(result) != "custom-result" {
		t.Fatalf("result = %q, want %q", string(result), "custom-result")
	}
	if cost != 42 {
		t.Fatalf("cost = %d, want 42", cost)
	}
	if !customCalled {
		t.Fatal("custom handler was not called")
	}

	// Should now appear in List()
	names := hf.List()
	found := false
	for _, n := range names {
		if n == "custom_op" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("custom_op not found in List()")
	}
}

func TestModuleRegistryUnregister(t *testing.T) {
	r := NewModuleRegistry()

	// Register a module
	bytecode := []byte{0x00, 0x61, 0x73, 0x6d, 0x01}
	hash, err := r.Register("my-module", bytecode, []string{"run"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if r.Count() != 1 {
		t.Fatalf("count = %d, want 1", r.Count())
	}

	// Unregister the existing module
	if err := r.Unregister(hash); err != nil {
		t.Fatalf("Unregister: %v", err)
	}
	if r.Count() != 0 {
		t.Fatalf("count = %d, want 0 after unregister", r.Count())
	}

	_, ok := r.Get(hash)
	if ok {
		t.Fatal("module should not be found after unregister")
	}

	// Unregister non-existent module should return an error
	err = r.Unregister(types.HexToHash("0xdeadbeef"))
	if err == nil {
		t.Fatal("expected error when unregistering non-existent module")
	}
}
