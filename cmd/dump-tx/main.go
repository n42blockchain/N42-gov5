// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

// dump-tx prints a single tx from a Geth ancient block body — to/from,
// value, gas, gasPrice, and the input data prefix. Useful for matching a
// failing tx hash against its on-chain action when debugging gas mismatches.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	ancient := flag.String("ancient", `d:\geth\geth\chaindata\ancient\chain`, "geth ancient chain dir")
	blockNum := flag.Uint64("block", 11114732, "block number")
	idx := flag.Int("idx", 32, "tx index in block")
	flag.Parse()

	f, err := freezer.New(*ancient, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open freezer:", err)
		os.Exit(1)
	}
	defer f.Close()

	bodyData, err := f.Ancient(freezer.TableBodies, *blockNum)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read body:", err)
		os.Exit(1)
	}
	body, err := ethel.DecodeGethBody(bodyData)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode body:", err)
		os.Exit(1)
	}

	if *idx < 0 || *idx >= len(body.Transactions) {
		fmt.Fprintf(os.Stderr, "idx %d out of range [0,%d)\n", *idx, len(body.Transactions))
		os.Exit(1)
	}
	tx := body.Transactions[*idx]
	fmt.Printf("block %d tx %d\n", *blockNum, *idx)
	fmt.Printf("  hash:     %s\n", tx.Hash().Hex())
	fmt.Printf("  type:     %d\n", tx.Type())
	fmt.Printf("  nonce:    %d\n", tx.Nonce())
	fmt.Printf("  gas:      %d\n", tx.Gas())
	fmt.Printf("  gasPrice: %s\n", tx.GasPrice().String())
	fmt.Printf("  value:    %s wei\n", tx.Value().String())
	if to := tx.To(); to != nil {
		fmt.Printf("  to:       %s\n", to.Hex())
	} else {
		fmt.Printf("  to:       (contract creation)\n")
	}
	data := tx.Data()
	fmt.Printf("  data:     %d bytes\n", len(data))
	if len(data) >= 4 {
		fmt.Printf("  selector: 0x%s\n", hex.EncodeToString(data[:4]))
	}
	if len(data) > 0 {
		dump := data
		if len(dump) > 256 {
			dump = dump[:256]
		}
		fmt.Printf("  dataHex:  %s\n", hex.EncodeToString(dump))
	}
}
