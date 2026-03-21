package wasm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"sync"
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
	if len(names) != 5 {
		t.Fatalf("host functions = %d, want 5", len(names))
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

func TestEngineExecuteConcurrent(t *testing.T) {
	engine := NewEngine(&mockRuntime{}, 256, 30, 1.0)
	defer engine.Close(context.Background())

	hash := types.HexToHash("0xc0c0")
	bytecode := []byte{0x00, 0x61, 0x73, 0x6d}

	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			args := []byte(fmt.Sprintf("arg-%d", idx))
			result, err := engine.Execute(context.Background(), hash, bytecode, "compute", args, 1000)
			if err != nil {
				errs <- fmt.Errorf("goroutine %d: %w", idx, err)
				return
			}
			if result.GasUsed != 500 {
				errs <- fmt.Errorf("goroutine %d: gasUsed = %d, want 500", idx, result.GasUsed)
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
}

func TestEngineClose(t *testing.T) {
	engine := NewEngine(&mockRuntime{}, 256, 30, 1.0)

	hash := types.HexToHash("0xdead")
	bytecode := []byte{0x00, 0x61, 0x73, 0x6d}

	// Populate cache
	_, err := engine.Execute(context.Background(), hash, bytecode, "f", nil, 100)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if engine.CacheSize() != 1 {
		t.Fatalf("cache size = %d, want 1", engine.CacheSize())
	}

	// Close engine
	if err := engine.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Cache should be cleared
	if engine.CacheSize() != 0 {
		t.Fatalf("cache size = %d, want 0 after close", engine.CacheSize())
	}
}

func TestEngineStats(t *testing.T) {
	engine := NewEngine(&mockRuntime{}, 512, 60, 1.5)
	defer engine.Close(context.Background())

	stats := engine.Stats()

	if stats["cacheSize"] != 0 {
		t.Fatalf("cacheSize = %v, want 0", stats["cacheSize"])
	}
	if stats["maxMemoryMB"] != 512 {
		t.Fatalf("maxMemoryMB = %v, want 512", stats["maxMemoryMB"])
	}
	if stats["gasMultiplier"] != 1.5 {
		t.Fatalf("gasMultiplier = %v, want 1.5", stats["gasMultiplier"])
	}

	// Add a module to cache and check again
	hash := types.HexToHash("0xabcd")
	engine.Execute(context.Background(), hash, []byte{0x00}, "f", nil, 100)

	stats = engine.Stats()
	if stats["cacheSize"] != 1 {
		t.Fatalf("cacheSize after execute = %v, want 1", stats["cacheSize"])
	}
}

func TestHostFunctionSHA256(t *testing.T) {
	hf := NewHostFunctions()

	fn, ok := hf.Get("sha256")
	if !ok {
		t.Fatal("sha256 not found")
	}

	input := []byte("hello world")
	result, cost, err := fn.Handler(input)
	if err != nil {
		t.Fatalf("sha256: %v", err)
	}
	if len(result) != 32 {
		t.Fatalf("sha256 output length = %d, want 32", len(result))
	}
	if cost == 0 {
		t.Fatal("sha256 cost should be > 0")
	}

	// Verify correctness against standard library
	expected := sha256.Sum256(input)
	for i, b := range result {
		if b != expected[i] {
			t.Fatalf("sha256 mismatch at byte %d: got %x, want %x", i, b, expected[i])
		}
	}
}

func TestHostFunctionCASStore(t *testing.T) {
	hf := NewHostFunctions()

	expectedHash := types.HexToHash("0xbeefbeef")
	hf.SetCASCallbacks(
		func(hash types.Hash) ([]byte, error) { return nil, nil },
		func(data []byte) (types.Hash, error) { return expectedHash, nil },
	)

	fn, ok := hf.Get("cas_store")
	if !ok {
		t.Fatal("cas_store not found")
	}

	result, cost, err := fn.Handler([]byte("some data"))
	if err != nil {
		t.Fatalf("cas_store: %v", err)
	}
	if cost == 0 {
		t.Fatal("cas_store cost should be > 0")
	}

	gotHash := types.BytesToHash(result)
	if gotHash != expectedHash {
		t.Fatalf("cas_store hash = %s, want %s", gotHash.Hex(), expectedHash.Hex())
	}
}

func TestHostFunctionCASLoadShortArgs(t *testing.T) {
	hf := NewHostFunctions()

	fn, ok := hf.Get("cas_load")
	if !ok {
		t.Fatal("cas_load not found")
	}

	// Provide less than 32 bytes
	_, _, err := fn.Handler([]byte("short"))
	if err == nil {
		t.Fatal("expected error for short args to cas_load")
	}
}

func TestHostFunctionOverride(t *testing.T) {
	hf := NewHostFunctions()

	// Override keccak256 with a custom implementation
	hf.Register(&HostFunc{
		Name: "keccak256",
		Handler: func(args []byte) ([]byte, uint64, error) {
			return []byte("overridden"), 99, nil
		},
	})

	fn, ok := hf.Get("keccak256")
	if !ok {
		t.Fatal("keccak256 not found after override")
	}

	result, cost, err := fn.Handler([]byte("test"))
	if err != nil {
		t.Fatalf("overridden keccak256: %v", err)
	}
	if string(result) != "overridden" {
		t.Fatalf("result = %q, want %q", string(result), "overridden")
	}
	if cost != 99 {
		t.Fatalf("cost = %d, want 99", cost)
	}
}

func TestModuleRegistryGetBytecode(t *testing.T) {
	r := NewModuleRegistry()

	original := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	hash, err := r.Register("bytecode-test", original, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Get bytecode copy
	bc := r.GetBytecode(hash)
	if bc == nil {
		t.Fatal("GetBytecode returned nil")
	}
	if len(bc) != len(original) {
		t.Fatalf("bytecode length = %d, want %d", len(bc), len(original))
	}

	// Modify the copy and verify the registry is not affected
	bc[0] = 0xFF
	bc2 := r.GetBytecode(hash)
	if bc2[0] != 0x00 {
		t.Fatal("modifying returned bytecode should not affect registry")
	}

	// Non-existent hash should return nil
	nilResult := r.GetBytecode(types.HexToHash("0xffffff"))
	if nilResult != nil {
		t.Fatal("GetBytecode for non-existent hash should return nil")
	}
}

func TestModuleRegistryList(t *testing.T) {
	r := NewModuleRegistry()

	_, err1 := r.Register("mod-a", []byte("bytecode-a"), []string{"run"})
	_, err2 := r.Register("mod-b", []byte("bytecode-b"), []string{"init"})
	_, err3 := r.Register("mod-c", []byte("bytecode-c"), []string{"exec"})
	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatalf("Register errors: %v, %v, %v", err1, err2, err3)
	}

	list := r.List()
	if len(list) != 3 {
		t.Fatalf("List() length = %d, want 3", len(list))
	}

	// Verify all names are present
	nameSet := make(map[string]bool)
	for _, m := range list {
		nameSet[m.Name] = true
	}
	for _, expected := range []string{"mod-a", "mod-b", "mod-c"} {
		if !nameSet[expected] {
			t.Fatalf("List() missing module %q", expected)
		}
	}
}

func TestModuleRegistryConcurrent(t *testing.T) {
	r := NewModuleRegistry()

	var wg sync.WaitGroup
	errs := make(chan error, 50)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Each goroutine registers unique modules, calls Get/Count
			for j := 0; j < 5; j++ {
				name := fmt.Sprintf("mod-%d-%d", idx, j)
				bytecode := []byte(fmt.Sprintf("bytecode-%d-%d", idx, j))
				hash, err := r.Register(name, bytecode, []string{"run"})
				if err != nil {
					errs <- fmt.Errorf("register %s: %w", name, err)
					return
				}
				_, ok := r.Get(hash)
				if !ok {
					errs <- fmt.Errorf("module %s not found immediately after register", name)
					return
				}
				_ = r.Count()
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}

	if r.Count() != 25 {
		t.Fatalf("count = %d, want 25", r.Count())
	}
}

func TestGasSafeOverflow(t *testing.T) {
	// SafeGasAdd overflow
	result := SafeGasAdd(math.MaxUint64, 1)
	if result != math.MaxUint64 {
		t.Fatalf("SafeGasAdd(MaxUint64, 1) = %d, want MaxUint64", result)
	}

	result = SafeGasAdd(math.MaxUint64, math.MaxUint64)
	if result != math.MaxUint64 {
		t.Fatalf("SafeGasAdd(MaxUint64, MaxUint64) = %d, want MaxUint64", result)
	}

	// SafeGasAdd normal
	result = SafeGasAdd(100, 200)
	if result != 300 {
		t.Fatalf("SafeGasAdd(100, 200) = %d, want 300", result)
	}

	// SafeGasMul overflow
	result = SafeGasMul(math.MaxUint64, 2)
	if result != math.MaxUint64 {
		t.Fatalf("SafeGasMul(MaxUint64, 2) = %d, want MaxUint64", result)
	}

	result = SafeGasMul(math.MaxUint64, math.MaxUint64)
	if result != math.MaxUint64 {
		t.Fatalf("SafeGasMul(MaxUint64, MaxUint64) = %d, want MaxUint64", result)
	}

	// SafeGasMul with zero
	result = SafeGasMul(0, math.MaxUint64)
	if result != 0 {
		t.Fatalf("SafeGasMul(0, MaxUint64) = %d, want 0", result)
	}

	result = SafeGasMul(math.MaxUint64, 0)
	if result != 0 {
		t.Fatalf("SafeGasMul(MaxUint64, 0) = %d, want 0", result)
	}

	// SafeGasMul normal
	result = SafeGasMul(100, 200)
	if result != 20000 {
		t.Fatalf("SafeGasMul(100, 200) = %d, want 20000", result)
	}
}

func TestGasTableCosts(t *testing.T) {
	g := DefaultGasTable()

	// Keccak256Cost with non-zero input
	if cost := g.Keccak256Cost(1); cost == 0 {
		t.Fatal("Keccak256Cost(1) should be > 0")
	}
	if cost := g.Keccak256Cost(100); cost == 0 {
		t.Fatal("Keccak256Cost(100) should be > 0")
	}

	// LogMsgCost with non-zero input
	if cost := g.LogMsgCost(1); cost == 0 {
		t.Fatal("LogMsgCost(1) should be > 0")
	}
	if cost := g.LogMsgCost(100); cost == 0 {
		t.Fatal("LogMsgCost(100) should be > 0")
	}

	// MemoryGrowCost with non-zero pages
	if cost := g.MemoryGrowCost(1); cost == 0 {
		t.Fatal("MemoryGrowCost(1) should be > 0")
	}
	if cost := g.MemoryGrowCost(100); cost == 0 {
		t.Fatal("MemoryGrowCost(100) should be > 0")
	}

	// Zero-length still returns base cost (which is non-zero)
	if cost := g.Keccak256Cost(0); cost == 0 {
		t.Fatal("Keccak256Cost(0) should return base cost > 0")
	}
	if cost := g.LogMsgCost(0); cost == 0 {
		t.Fatal("LogMsgCost(0) should return base cost > 0")
	}
	if cost := g.MemoryGrowCost(0); cost == 0 {
		t.Fatal("MemoryGrowCost(0) should return base cost > 0")
	}
}
