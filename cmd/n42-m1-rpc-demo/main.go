// n42-m1-rpc-demo is a minimal prototype of the M1 "snapshot + hot-delta" client
// RPC capability (docs/ethel/minimal-client-rpc-capability.md). It opens the cold
// RecSplit state snapshot (internal/ethel/snapshotreader, produced by
// cmd/reth-snapshot-export) at a fixed height H0 and serves the current-state
// reads — eth_getBalance / eth_getTransactionCount / eth_getCode(Hash) /
// eth_getStorageAt — directly from it, with NO RebuildState and NO full trie.
//
// This is the cold half of M1. The hot half (warm MDBX overlay of blocks
// H0→tip, already wired into eth-el's engine_state_adapter via --bootstrap.mode
// snapshot) would shadow these reads for keys changed after H0; the snapshot is
// the authoritative base.
//
// Usage:
//
//	n42-m1-rpc-demo --dir D:/N42-snap/snapshot --acc accounts.0-25188781 --sto storage.0-25188781
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/internal/ethel/snapshotreader"
)

// Well-known mainnet addresses to exercise the reader (contract = code+storage+
// balance; these are stable, high-profile, and present at any recent height).
var demo = []struct {
	name string
	addr string
}{
	{"WETH (contract)", "C02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"},
	{"USDC proxy (contract)", "A0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"},
	{"Uniswap V2 router (contract)", "7a250d5630B4cF539739dF2C5dAcb4c659F2488D"},
}

func mustAddr(s string) [20]byte {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr", s)
		os.Exit(1)
	}
	var a [20]byte
	copy(a[:], b)
	return a
}

func main() {
	dir := flag.String("dir", `D:/N42-snap/snapshot`, "snapshot segment dir")
	acc := flag.String("acc", "accounts.0-25188781", "accounts segment prefix")
	sto := flag.String("sto", "storage.0-25188781", "storage segment prefix")
	flag.Parse()

	seg, err := snapshotreader.OpenSegment(*dir, *acc, *sto)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open snapshot:", err)
		os.Exit(1)
	}
	defer seg.Close()
	fmt.Printf("M1 snapshot opened: %s/%s (codedict %d entries)\n\n", *dir, *acc, seg.CodeDictLen())

	for _, d := range demo {
		addr := mustAddr(d.addr)
		raw, ok := seg.AccountValueRaw(addr)
		if !ok {
			fmt.Printf("%-28s 0x%s : ABSENT\n", d.name, d.addr)
			continue
		}
		a, derr := seg.DecodeAccount(raw)
		if derr != nil {
			fmt.Printf("%-28s decode error: %v\n", d.name, derr)
			continue
		}
		isContract := a.CodeHash.Hex() != "0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470"
		fmt.Printf("%-28s 0x%s\n", d.name, d.addr)
		fmt.Printf("    eth_getBalance        = %s wei\n", a.Balance.ToBig().String())
		fmt.Printf("    eth_getTransactionCount(nonce) = %d\n", a.Nonce)
		fmt.Printf("    eth_getCode(codeHash) = %s  contract=%v\n", a.CodeHash.Hex(), isContract)
		// eth_getStorageAt for a few low slots.
		for _, slotN := range []byte{0, 1, 2} {
			var slot [32]byte
			slot[31] = slotN
			v, sok := seg.StorageValue(addr, slot)
			if sok {
				fmt.Printf("    eth_getStorageAt(slot %d) = 0x%x\n", slotN, v)
			} else {
				fmt.Printf("    eth_getStorageAt(slot %d) = 0x0 (absent/zero)\n", slotN)
			}
		}
		fmt.Println()
	}
	fmt.Println("M1 capability demonstrated: getBalance / getTransactionCount / getCode(Hash) / getStorageAt served from the cold snapshot at H0, no trie, no RebuildState.")
}
