package vm

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestPrecompilesInitialized(t *testing.T) {
	if !PrecompilesInitialized() {
		t.Error("expected precompile address slices to be initialized")
	}
}

func TestPrecompileCount(t *testing.T) {
	counts := PrecompileCount()
	for _, fork := range []string{"Homestead", "Byzantium", "Istanbul", "Berlin"} {
		if counts[fork] <= 0 {
			t.Errorf("expected positive count for %s, got %d", fork, counts[fork])
		}
	}
}

func TestGetPrecompiledAddressesAndIsPrecompiled(t *testing.T) {
	rules := &params.Rules{IsHomestead: true}
	addrs := GetPrecompiledAddresses(rules)
	if len(addrs) == 0 {
		t.Fatal("expected non-empty precompiled addresses")
	}
	if !IsPrecompiled(addrs[0], rules) {
		t.Errorf("expected %v to be precompiled", addrs[0])
	}
	notPrecompiled := types.Address{0xDE, 0xAD, 0xBE, 0xEF}
	if IsPrecompiled(notPrecompiled, rules) {
		t.Error("expected arbitrary address to not be precompiled")
	}
}

func TestGetPrecompiledContract(t *testing.T) {
	rules := &params.Rules{IsHomestead: true}
	addrs := GetPrecompiledAddresses(rules)
	if len(addrs) == 0 {
		t.Fatal("expected non-empty precompiled addresses")
	}
	contract := GetPrecompiledContract(addrs[0], rules)
	if contract == nil {
		t.Errorf("expected contract for address %v", addrs[0])
	}
	missing := GetPrecompiledContract(types.Address{0xFF}, rules)
	if missing != nil {
		t.Error("expected nil contract for unknown address")
	}
}
