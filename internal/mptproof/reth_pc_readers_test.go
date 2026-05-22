package mptproof

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/commitment"
)

// TestRethBackedReader_USDCAccount verifies the AccountReader path
// against production reth data: addrHash → reth HashedAccounts →
// DecodeRethAccount → *commitment.Update with proper Flags + fields.
func TestRethBackedReader_USDCAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	src, err := NewRethHashedLeafSource(productionRethDB2k, 4096)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}
	defer src.Close()

	reader := NewRethBackedReader(src)

	// USDC address (plain, 20-byte).
	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	usdc, _ := hex.DecodeString(usdcHex)

	upd, err := reader.Account(usdc)
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if upd == nil {
		t.Fatalf("USDC absent")
	}

	t.Logf("USDC Update:")
	t.Logf("  Flags=0x%x (BalanceUpdate=0x%x NonceUpdate=0x%x CodeUpdate=0x%x StorageUpdate=0x%x)",
		upd.Flags, commitment.BalanceUpdate, commitment.NonceUpdate,
		commitment.CodeUpdate, commitment.StorageUpdate)
	t.Logf("  Nonce=%d", upd.Nonce)
	t.Logf("  Balance=%s", upd.Balance.String())
	t.Logf("  CodeHash=0x%x", upd.CodeHash[:])

	if upd.Flags&commitment.BalanceUpdate == 0 {
		t.Errorf("missing BalanceUpdate flag")
	}
	if upd.Flags&commitment.NonceUpdate == 0 {
		t.Errorf("missing NonceUpdate flag")
	}
	if upd.Flags&commitment.CodeUpdate == 0 {
		t.Errorf("missing CodeUpdate flag (USDC is a contract)")
	}
	if upd.Nonce != 1 {
		t.Errorf("nonce: got %d want 1", upd.Nonce)
	}
	if !upd.Balance.IsZero() {
		t.Errorf("balance: got %s want 0", upd.Balance.String())
	}
}

// TestRethBackedReader_USDCStorageSlot0 verifies the StorageReader
// path against production reth data: addrHash||slotHash → reth
// HashedStorages → *commitment.Update.
func TestRethBackedReader_USDCStorageSlot0(t *testing.T) {
	if testing.Short() {
		t.Skip("--short")
	}
	if _, err := os.Stat(filepath.Join(productionRethDB2k, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB2k)
	}

	src, err := NewRethHashedLeafSource(productionRethDB2k, 4096)
	if err != nil {
		t.Fatalf("NewRethHashedLeafSource: %v", err)
	}
	defer src.Close()

	reader := NewRethBackedReader(src)

	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	usdc, _ := hex.DecodeString(usdcHex)

	// Slot 0 = USDC totalSupply (plain, 32-byte all-zero slot id).
	var slot [32]byte

	composite := append(append([]byte{}, usdc...), slot[:]...)
	upd, err := reader.Storage(composite)
	if err != nil {
		t.Fatalf("Storage: %v", err)
	}
	if upd == nil {
		t.Fatalf("USDC slot 0 absent (expected: totalSupply > 0)")
	}
	if upd.Flags&commitment.StorageUpdate == 0 {
		t.Errorf("missing StorageUpdate flag")
	}
	if upd.StorageLen == 0 {
		t.Errorf("StorageLen is 0 (USDC totalSupply should be non-zero)")
	}
	t.Logf("USDC slot 0 (totalSupply) Update: StorageLen=%d value=0x%x",
		upd.StorageLen, upd.Storage[:upd.StorageLen])
}
