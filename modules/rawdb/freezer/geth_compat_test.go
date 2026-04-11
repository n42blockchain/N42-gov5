// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// geth_compat_test.go verifies that the Geth-compatible freezer can read
// real Geth ancient chain data from e:\geth\geth\chaindata\ancient\chain\.
//
// These tests are skipped if the Geth data directory is not present.

package freezer

import (
	"bytes"
	"os"
	"testing"
)

const gethAncientPath = `e:\geth\geth\chaindata\ancient\chain`

func skipIfNoGeth(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(gethAncientPath); err != nil {
		t.Skipf("Geth ancient data not available at %s", gethAncientPath)
	}
}

func TestGethReadHeaders(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := NewFreezerTable(gethAncientPath, "headers", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	items := tbl.Items()
	t.Logf("Headers table: %d items", items)
	if items == 0 {
		t.Fatal("expected non-zero items in headers table")
	}

	// Read genesis header (block 0).
	data, err := tbl.Retrieve(0)
	if err != nil {
		t.Fatalf("retrieve header 0: %v", err)
	}
	t.Logf("Genesis header: %d bytes", len(data))
	if len(data) < 50 {
		t.Fatalf("genesis header too small: %d bytes", len(data))
	}

	// Read header 1.
	data1, err := tbl.Retrieve(1)
	if err != nil {
		t.Fatalf("retrieve header 1: %v", err)
	}
	t.Logf("Header 1: %d bytes", len(data1))

	// Read a later header.
	data1M, err := tbl.Retrieve(1_000_000)
	if err != nil {
		t.Fatalf("retrieve header 1M: %v", err)
	}
	t.Logf("Header 1M: %d bytes", len(data1M))
}

func TestGethReadHashes(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := NewFreezerTable(gethAncientPath, "hashes", "r")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	items := tbl.Items()
	t.Logf("Hashes table: %d items", items)

	// Read genesis hash (block 0).
	hash0, err := tbl.Retrieve(0)
	if err != nil {
		t.Fatalf("retrieve hash 0: %v", err)
	}
	if len(hash0) != 32 {
		t.Fatalf("expected 32-byte hash, got %d bytes", len(hash0))
	}
	t.Logf("Genesis hash: %x", hash0)

	// Ethereum mainnet genesis hash.
	expectedGenesis := []byte{
		0xd4, 0xe5, 0x67, 0x40, 0xf8, 0x76, 0xae, 0xf8,
		0xc0, 0x10, 0xb8, 0x6a, 0x40, 0xd5, 0xf5, 0x67,
		0x45, 0xa1, 0x18, 0xd0, 0x90, 0x6a, 0x34, 0xe6,
		0x9a, 0xec, 0x8c, 0x0d, 0xb1, 0xcb, 0x8f, 0xa3,
	}
	if !bytes.Equal(hash0, expectedGenesis) {
		t.Fatalf("genesis hash mismatch:\n  got:  %x\n  want: %x", hash0, expectedGenesis)
	}
	t.Log("Genesis hash matches Ethereum mainnet!")
}

func TestGethReadDiffs(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := NewFreezerTable(gethAncientPath, "diffs", "r")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	items := tbl.Items()
	t.Logf("Diffs table: %d items", items)

	// Read genesis TD (should be 0x400000000 = 17179869184 for mainnet).
	td0, err := tbl.Retrieve(0)
	if err != nil {
		t.Fatalf("retrieve td 0: %v", err)
	}
	t.Logf("Genesis TD: %x (%d bytes)", td0, len(td0))
}

func TestGethReadBodies(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := NewFreezerTable(gethAncientPath, "bodies", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	items := tbl.Items()
	t.Logf("Bodies table: %d items", items)

	// Genesis body should be empty (no transactions).
	body0, err := tbl.Retrieve(0)
	if err != nil {
		t.Fatalf("retrieve body 0: %v", err)
	}
	t.Logf("Genesis body: %d bytes, first bytes: %x", len(body0), body0[:min(16, len(body0))])

	// Block 46147 — first ever Ethereum transaction.
	body46147, err := tbl.Retrieve(46147)
	if err != nil {
		t.Fatalf("retrieve body 46147: %v", err)
	}
	t.Logf("Block 46147 body: %d bytes (first Ethereum tx)", len(body46147))
	if len(body46147) < 50 {
		t.Fatal("block 46147 should have a transaction")
	}
}

func TestGethReadReceipts(t *testing.T) {
	skipIfNoGeth(t)

	tbl, err := NewFreezerTable(gethAncientPath, "receipts", "c")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	items := tbl.Items()
	t.Logf("Receipts table: %d items", items)

	// Genesis receipts should be empty.
	rcpt0, err := tbl.Retrieve(0)
	if err != nil {
		t.Fatalf("retrieve receipt 0: %v", err)
	}
	t.Logf("Genesis receipts: %d bytes", len(rcpt0))
}

func TestGethTableCounts(t *testing.T) {
	skipIfNoGeth(t)

	// Don't use New() on real Geth data — it would truncate tables.
	// Instead, open each table individually and report counts.
	for _, spec := range []struct{ name, ext string }{
		{"headers", "c"}, {"bodies", "c"}, {"receipts", "c"},
		{"hashes", "r"}, {"diffs", "r"},
	} {
		tbl, err := NewFreezerTable(gethAncientPath, spec.name, spec.ext)
		if err != nil {
			t.Fatalf("open %s: %v", spec.name, err)
		}
		t.Logf("%-10s: %d items", spec.name, tbl.Items())
		tbl.Close()
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
