// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

func init() {
	exportCommand.Subcommands = append(exportCommand.Subcommands, &cli.Command{
		Name:      "state",
		Usage:     "Export world state (all accounts) to JSON",
		ArgsUsage: "<filename>",
		Action:    exportState,
		Flags: []cli.Flag{
			DataDirFlag,
			&cli.BoolFlag{
				Name:  "include-storage",
				Usage: "Include contract storage slots",
				Value: false,
			},
			&cli.BoolFlag{
				Name:  "include-code",
				Usage: "Include contract bytecode",
				Value: false,
			},
		},
		Description: `Export all account states to a JSON file.
By default exports addresses, nonces, and balances.
Use --include-storage to include contract storage slots.
Use --include-code to include contract bytecodes.`,
	})
}

// DumpAccount represents a single account in the state dump.
type DumpAccount struct {
	Address string            `json:"address"`
	Nonce   uint64            `json:"nonce"`
	Balance string            `json:"balance"`
	Code    string            `json:"code,omitempty"`
	Storage map[string]string `json:"storage,omitempty"`
}

// StateDump represents the complete state dump.
type StateDump struct {
	Block    uint64         `json:"block"`
	Accounts []*DumpAccount `json:"accounts"`
}

func exportState(ctx *cli.Context) error {
	if ctx.NArg() < 1 {
		return fmt.Errorf("usage: n42 export state <filename> [--include-storage] [--include-code]")
	}

	includeStorage := ctx.Bool("include-storage")
	includeCode := ctx.Bool("include-code")
	filename := ctx.Args().Get(0)

	stack, err := node.NewNode(ctx, &DefaultConfig)
	if err != nil {
		return err
	}
	defer stack.Close()

	db := stack.Database()
	bc := stack.BlockChain()

	currentBlock := bc.CurrentBlock()
	blockNum := uint64(0)
	if currentBlock != nil {
		blockNum = currentBlock.Number64().Uint64()
	}

	roTX, err := db.BeginRo(ctx.Context)
	if err != nil {
		return err
	}
	defer roTX.Rollback()

	log.Info("Exporting state", "block", blockNum, "file", filename,
		"includeStorage", includeStorage, "includeCode", includeCode)
	start := time.Now()

	// Iterate Account table
	cursor, err := roTX.Cursor(modules.Account)
	if err != nil {
		return fmt.Errorf("cannot open Account cursor: %w", err)
	}
	defer cursor.Close()

	dump := &StateDump{
		Block:    blockNum,
		Accounts: make([]*DumpAccount, 0, 1024),
	}

	count := uint64(0)
	for k, v, err := cursor.First(); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			return fmt.Errorf("cursor error: %w", err)
		}

		var acc account.StateAccount
		if err := acc.DecodeForStorage(v); err != nil {
			log.Warn("Failed to decode account", "key", fmt.Sprintf("%x", k), "err", err)
			continue
		}

		da := &DumpAccount{
			Address: fmt.Sprintf("0x%x", k),
			Nonce:   acc.Nonce,
			Balance: acc.Balance.ToBig().String(),
		}

		if includeCode && acc.Incarnation > 0 {
			code, err := roTX.GetOne(modules.Code, acc.CodeHash[:])
			if err == nil && len(code) > 0 {
				da.Code = fmt.Sprintf("0x%x", code)
			}
		}

		if includeStorage {
			da.Storage = make(map[string]string)
			storageCursor, err := roTX.Cursor(modules.Storage)
			if err == nil {
				// Storage key: address(20) + incarnation(2) + storageKey(32)
				prefix := make([]byte, 22)
				copy(prefix, k)
				// Use actual incarnation from account
				prefix[20] = byte(acc.Incarnation >> 8)
				prefix[21] = byte(acc.Incarnation)

				for sk, sv, err := storageCursor.Seek(prefix); sk != nil; sk, sv, err = storageCursor.Next() {
					if err != nil {
						break
					}
					// Check prefix match (address + incarnation)
					if !bytes.HasPrefix(sk, prefix) {
						break
					}
					storageKey := sk[22:]
					da.Storage[fmt.Sprintf("0x%x", storageKey)] = fmt.Sprintf("0x%x", sv)
				}
				storageCursor.Close()
			}
		}

		dump.Accounts = append(dump.Accounts, da)
		count++

		if count%100000 == 0 {
			log.Info("Exporting accounts", "count", count,
				"elapsed", time.Since(start).Round(time.Second))
		}
	}

	// Write JSON output
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("cannot create file: %w", err)
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(dump); err != nil {
		return fmt.Errorf("JSON encode error: %w", err)
	}

	log.Info("State export complete",
		"accounts", count,
		"elapsed", time.Since(start).Round(time.Second),
		"file", filename)
	return nil
}

