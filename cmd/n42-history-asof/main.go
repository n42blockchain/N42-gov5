// n42-history-asof: query account or storage state at a specific block
// height using the MPHF+fp history coldstore. Reth-style historical
// state query — no commitment-history persisted, value is decoded from
// the change history blob built by n42-history-build.
//
// Examples:
//
//	# USDC contract account state just before block 20,000,000
//	n42-history-asof --store D:\n42-history-full \
//	    --addr 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48 \
//	    --block 20000000
//
//	# USDC totalSupply slot at the start of block 25,000,000
//	n42-history-asof --store D:\n42-history-full \
//	    --addr 0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48 \
//	    --slot 0x000000000000000000000000000000000000000000000000000000000000000b \
//	    --block 25000000
//
//	# dump full per-block timeline for an address
//	n42-history-asof --store D:\n42-history-full \
//	    --addr 0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045 --history
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/n42blockchain/N42/internal/history"
	"github.com/n42blockchain/N42/internal/historicalstate"
)

func main() {
	store := flag.String("store", `D:\n42-history-full`, "history coldstore directory")
	addrHex := flag.String("addr", "", "20-byte address (hex, 0x optional)")
	slotHex := flag.String("slot", "", "32-byte storage slot (hex, 0x optional). Empty = account query")
	block := flag.Uint64("block", 0, "query block height (value at START of this block)")
	dumpHistory := flag.Bool("history", false, "print full timeline instead of single AsOf")
	flag.Parse()

	if *addrHex == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-history-asof --store DIR --addr 0xADDR [--slot 0xSLOT] [--block N | --history]")
		os.Exit(1)
	}

	addr, err := decodeFixed(*addrHex, 20)
	if err != nil {
		fatal("addr: %v", err)
	}
	var addr20 [20]byte
	copy(addr20[:], addr)

	r, err := historicalstate.Open(*store)
	if err != nil {
		fatal("open store: %v", err)
	}
	defer r.Close()

	st := r.Stats()
	fmt.Fprintf(os.Stderr, "store: account=%d keys (%d pages), storage=%d keys (%d pages)\n",
		st.AccountKeys, st.AccountPageCount, st.StorageKeys, st.StoragePageCount)

	if *slotHex != "" {
		slot, err := decodeFixed(*slotHex, 32)
		if err != nil {
			fatal("slot: %v", err)
		}
		var slot32 [32]byte
		copy(slot32[:], slot)
		if *dumpHistory {
			changes, ok, err := r.StorageHistory(addr20, slot32)
			if err != nil {
				fatal("storage history: %v", err)
			}
			if !ok {
				fmt.Println("absent (slot never modified)")
				return
			}
			printTimeline("storage", changes)
			return
		}
		val, found, err := r.StorageAsOf(addr20, slot32, *block)
		if err != nil {
			fatal("storage asof: %v", err)
		}
		if !found {
			fmt.Printf("absent at block %d (slot did not exist or was never modified before this block)\n", *block)
			return
		}
		fmt.Printf("value@block_start=%d:  0x%s  (%d bytes)\n", *block, hex.EncodeToString(val), len(val))
		return
	}

	if *dumpHistory {
		changes, ok, err := r.AccountHistory(addr20)
		if err != nil {
			fatal("account history: %v", err)
		}
		if !ok {
			fmt.Println("absent (account never modified)")
			return
		}
		printTimeline("account", changes)
		return
	}
	val, found, err := r.AccountAsOf(addr20, *block)
	if err != nil {
		fatal("account asof: %v", err)
	}
	if !found {
		fmt.Printf("absent at block %d (account did not exist before this block)\n", *block)
		return
	}
	fmt.Printf("account@block_start=%d:  0x%s  (%d bytes)\n", *block, hex.EncodeToString(val), len(val))
}

func decodeFixed(s string, want int) ([]byte, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, err
	}
	if len(b) != want {
		return nil, fmt.Errorf("expected %d bytes, got %d", want, len(b))
	}
	return b, nil
}

func printTimeline(label string, changes []history.Change) {
	fmt.Printf("%s timeline: %d entries\n", label, len(changes))
	for _, c := range changes {
		fmt.Printf("  block=%-10d  value=0x%s\n", c.Block, hex.EncodeToString(c.Value))
	}
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
