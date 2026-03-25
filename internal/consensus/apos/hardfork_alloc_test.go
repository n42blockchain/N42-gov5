package apos

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func resetAllocState() {
	hardforkAllocOnce = sync.Once{}
	hardforkAllocMap = nil
	hardforkAllocDir = ""
}

func TestLoadHardForkAllocations_ValidFile(t *testing.T) {
	resetAllocState()
	dir := t.TempDir()
	data := `[{"block":100,"address":"0x4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091","amount":"0x1000"}]`
	os.WriteFile(filepath.Join(dir, hardforkAllocFile), []byte(data), 0644)
	hardforkAllocDir = dir
	loadHardForkAllocations()

	if len(hardforkAllocMap) != 1 {
		t.Fatalf("expected 1 block entry, got %d", len(hardforkAllocMap))
	}
	allocs := hardforkAllocMap[100]
	if len(allocs) != 1 {
		t.Fatalf("expected 1 allocation at block 100, got %d", len(allocs))
	}
	if allocs[0].value.Uint64() != 0x1000 {
		t.Fatalf("expected amount 0x1000, got %d", allocs[0].value.Uint64())
	}
}

func TestLoadHardForkAllocations_MissingFile(t *testing.T) {
	resetAllocState()
	hardforkAllocDir = t.TempDir() // empty dir
	loadHardForkAllocations()

	if len(hardforkAllocMap) != 0 {
		t.Fatalf("expected 0 entries for missing file, got %d", len(hardforkAllocMap))
	}
}

func TestLoadHardForkAllocations_InvalidAddress(t *testing.T) {
	resetAllocState()
	dir := t.TempDir()
	// Short address (not 40 hex chars)
	data := `[{"block":100,"address":"0x1234","amount":"0x1000"}]`
	os.WriteFile(filepath.Join(dir, hardforkAllocFile), []byte(data), 0644)
	hardforkAllocDir = dir
	loadHardForkAllocations()

	if len(hardforkAllocMap[100]) != 0 {
		t.Fatalf("expected 0 allocations for invalid address, got %d", len(hardforkAllocMap[100]))
	}
}

func TestLoadHardForkAllocations_InvalidHex(t *testing.T) {
	resetAllocState()
	dir := t.TempDir()
	data := `[{"block":100,"address":"0x4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091","amount":"not_hex"}]`
	os.WriteFile(filepath.Join(dir, hardforkAllocFile), []byte(data), 0644)
	hardforkAllocDir = dir
	loadHardForkAllocations()

	if len(hardforkAllocMap[100]) != 0 {
		t.Fatalf("expected 0 allocations for invalid hex, got %d", len(hardforkAllocMap[100]))
	}
}

func TestLoadHardForkAllocations_DuplicateWarning(t *testing.T) {
	resetAllocState()
	dir := t.TempDir()
	data := `[
		{"block":100,"address":"0x4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091","amount":"0x100"},
		{"block":100,"address":"0x4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091","amount":"0x200"}
	]`
	os.WriteFile(filepath.Join(dir, hardforkAllocFile), []byte(data), 0644)
	hardforkAllocDir = dir
	loadHardForkAllocations()

	// Both entries are kept (with warning logged).
	allocs := hardforkAllocMap[100]
	if len(allocs) != 2 {
		t.Fatalf("expected 2 allocations (dup allowed with warning), got %d", len(allocs))
	}
}

func TestValidateAddress(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"0x4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091", true},
		{"4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091", true},
		{"0x1234", false},                                           // too short
		{"0xZZZZc44eeb74fecf4ad37b95a6d81bcae0f3f091", false},      // invalid hex
		{"0x4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091aa", false},    // too long
		{"", false},
	}
	for _, tt := range tests {
		if got := validateAddress(tt.addr); got != tt.ok {
			t.Errorf("validateAddress(%q) = %v, want %v", tt.addr, got, tt.ok)
		}
	}
}
