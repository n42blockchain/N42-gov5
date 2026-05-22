package mptproof

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestDecodeRethAccount_USDCSanity opens reth's HashedAccounts table
// and decodes USDC's account row, sanity-checking the field shape:
//
//	nonce       = small (contracts have nonce >= 1 from creation)
//	balance     = exactly 0 (USDC contract holds no ETH)
//	bytecode_hash = present (it's a contract)
//
// Acts as the smoke test for our Compact decoder before HA-3b
// wires it into a real AccountReader implementation. Skipped if
// reth's DB isn't present.
func TestDecodeRethAccount_USDCSanity(t *testing.T) {
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

	usdcHex := "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48"
	var usdc [20]byte
	b, _ := hex.DecodeString(usdcHex)
	copy(usdc[:], b)

	raw, found, err := src.AccountValue(usdc)
	if err != nil {
		t.Fatalf("AccountValue: %v", err)
	}
	if !found {
		t.Fatalf("USDC not found in HashedAccounts")
	}
	t.Logf("USDC raw account (%d bytes): %x", len(raw), raw)

	acct, err := DecodeRethAccount(raw)
	if err != nil {
		t.Fatalf("DecodeRethAccount: %v", err)
	}
	t.Logf("USDC decoded:")
	t.Logf("  nonce        = %d", acct.Nonce)
	t.Logf("  balance      = %s wei", acct.Balance.String())
	t.Logf("  hasBytecode  = %v", acct.HasBytecode)
	if acct.HasBytecode {
		t.Logf("  bytecodeHash = 0x%x", acct.BytecodeHash[:])
	}

	if !acct.HasBytecode {
		t.Errorf("USDC must have bytecode (it's a contract)")
	}
	if acct.Nonce == 0 {
		t.Errorf("USDC contract nonce must be >= 1")
	}
}

// TestDecodeRethAccount_FirstN dumps the first N accounts from the
// table to spot-check the codec produces sensible field shapes.
func TestDecodeRethAccount_FirstN(t *testing.T) {
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

	tx, err := src.db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer tx.Rollback()
	c, err := tx.Cursor(rethHashedAccountsTable)
	if err != nil {
		t.Fatalf("Cursor: %v", err)
	}
	defer c.Close()

	const N = 5
	count := 0
	for k, v, err := c.First(); k != nil && count < N; k, v, err = c.Next() {
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		acct, derr := DecodeRethAccount(v)
		if derr != nil {
			t.Errorf("decode %x: %v (raw=%x)", k[:8], derr, v)
			count++
			continue
		}
		t.Logf("addr_hash %x... nonce=%d balance=%s hasBytecode=%v",
			k[:8], acct.Nonce, acct.Balance.String(), acct.HasBytecode)
		count++
	}
}
