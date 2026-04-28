// dump-block-addr lists every tx in a block whose `to` matches a given
// address, or whose tx position is "interesting" (CREATE / value transfer
// to gas-token-style addresses). Useful to identify which tx in a block
// triggered a SELFDESTRUCT/CREATE2 cycle on a specific contract.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	ancient := flag.String("ancient", `d:\geth2m\geth\chaindata\ancient\chain`, "geth ancient chain dir")
	blockStr := flag.String("blocks", "", "comma-separated block numbers, e.g. 10941141,10941146,10941306")
	addrHex := flag.String("addr", "", "20-byte address (with or without 0x prefix); empty = list all txs")
	flag.Parse()

	if *blockStr == "" {
		fmt.Fprintln(os.Stderr, "usage: dump-block-addr --blocks 10941141,10941146,10941306 --addr 0x...beef")
		os.Exit(2)
	}
	var wantAddr *types.Address
	if *addrHex != "" {
		raw, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(*addrHex, "0x"), "0X"))
		if err != nil || len(raw) != 20 {
			fmt.Fprintln(os.Stderr, "bad addr (need 20-byte hex):", err)
			os.Exit(2)
		}
		var a types.Address
		copy(a[:], raw)
		wantAddr = &a
	}

	f, err := freezer.New(*ancient, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open freezer:", err)
		os.Exit(1)
	}
	defer f.Close()

	for _, bn := range strings.Split(*blockStr, ",") {
		var blockNum uint64
		fmt.Sscanf(strings.TrimSpace(bn), "%d", &blockNum)
		bodyData, err := f.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			fmt.Printf("block %d: read body: %v\n", blockNum, err)
			continue
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			fmt.Printf("block %d: decode body: %v\n", blockNum, err)
			continue
		}
		fmt.Printf("\n=== block %d (%d txs) ===\n", blockNum, len(body.Transactions))
		for i, tx := range body.Transactions {
			to := tx.To()
			match := wantAddr == nil
			if wantAddr != nil && to != nil && *to == *wantAddr {
				match = true
			}
			if !match {
				continue
			}
			toStr := "(contract creation)"
			if to != nil {
				toStr = to.Hex()
			}
			data := tx.Data()
			selector := ""
			if len(data) >= 4 {
				selector = "0x" + hex.EncodeToString(data[:4])
			}
			fmt.Printf("  tx[%3d] hash=%s type=%d gas=%d to=%s val=%s sel=%s dataLen=%d\n",
				i, tx.Hash().Hex(), tx.Type(), tx.Gas(), toStr, tx.Value().String(), selector, len(data))
		}
	}
}
