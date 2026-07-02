// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// chg-at — dump the account changes at one block whose keccak(addr) falls under
// a target hashed-nibble prefix, flagging DELETIONS (empty NewValue). Used to
// find whether a non-boundary height's divergence is caused by an
// account-removal/self-destruct that asOfLeaves mishandles.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"

	"github.com/n42blockchain/N42/internal/ethel"
)

func runChgAt(args []string) {
	fs := flag.NewFlagSet("chg-at", flag.ExitOnError)
	csDir := fs.String("changesets", `D:/N42-eth1177/chain/freezer`, "acctcs/storcs freezer dir")
	at := fs.Uint64("at", 0, "block height")
	prefixHex := fs.String("prefix", "", "hashed-nibble prefix (hex, e.g. b50) to filter keccak(addr)")
	_ = fs.Parse(args)

	pref := []byte(*prefixHex)
	acctTbl := openCS(*csDir, "acctcs")
	defer acctTbl.Close()

	ab, err := acctTbl.Retrieve(*at)
	if err != nil {
		die("retrieve acctcs[%d]: %v", *at, err)
	}
	if len(ab) == 0 {
		fmt.Printf("chg-at %d: empty account changeset\n", *at)
		return
	}
	acc, err := ethel.DecodeAccountChanges(ab)
	if err != nil {
		die("decode: %v", err)
	}
	fmt.Printf("chg-at %d: %d account changes total; filter keccak(addr) prefix=%s\n", *at, len(acc), *prefixHex)
	matched, deletes := 0, 0
	for i := range acc {
		h := keccak(acc[i].Address[:])
		hx := hex.EncodeToString(h[:])
		if *prefixHex != "" && !hasHexNibblePrefix(hx, string(pref)) {
			continue
		}
		matched++
		del := len(acc[i].NewValue) == 0
		if del {
			deletes++
		}
		fmt.Printf("  addr=%x hk=%s oldLen=%d newLen=%d %s\n",
			acc[i].Address[:], hx[:12], len(acc[i].OldValue), len(acc[i].NewValue),
			map[bool]string{true: "<<< DELETE", false: ""}[del])
	}
	fmt.Printf("=> %d under prefix, %d DELETIONS\n", matched, deletes)
}

func hasHexNibblePrefix(full, prefix string) bool {
	if len(prefix) > len(full) {
		return false
	}
	return full[:len(prefix)] == prefix
}
