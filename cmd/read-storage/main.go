// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

// read-storage prints the current value of (address, slot) pairs in a
// Plain Account/Storage MDBX database. Used to spot-check whether
// committed state matches the canonical chain when debugging gas
// mismatches against geth.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func main() {
	dir := flag.String("dir", `d:\n42-eth037`, "datadir (MDBX path)")
	addrHex := flag.String("addr", "c02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", "20-byte address (hex)")
	slotHex := flag.String("slot", "fb19a963956c9cb662dd3ae48988c4b90766df71ea130109840abe0a1b23dba8", "32-byte slot (hex)")
	flag.Parse()

	addr, err := hex.DecodeString(*addrHex)
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr:", err)
		os.Exit(1)
	}
	slot, err := hex.DecodeString(*slotHex)
	if err != nil || len(slot) != 32 {
		fmt.Fprintln(os.Stderr, "bad slot:", err)
		os.Exit(1)
	}

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*dir).
		Label(kv.ChainDB).
		Readonly().
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open mdbx:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin ro:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	// Plain layout: addr(20) + slot(32) = 52
	composite := make([]byte, 52)
	copy(composite[:20], addr)
	copy(composite[20:], slot)

	v, err := tx.GetOne(modules.Storage, composite)
	if err != nil {
		fmt.Fprintln(os.Stderr, "get:", err)
		os.Exit(1)
	}
	if v == nil {
		fmt.Printf("addr=%s slot=%s -> NOT PRESENT (= 0)\n", *addrHex, *slotHex)
		return
	}
	fmt.Printf("addr=%s slot=%s -> %x (%d bytes)\n", *addrHex, *slotHex, v, len(v))
}
