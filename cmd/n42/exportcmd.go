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
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/holiman/uint256"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/account"
	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
	"github.com/n42blockchain/N42/internal/node/backup"
)

var (
	exportCommand = &cli.Command{
		Name:        "export",
		Usage:       "Export N42 data",
		ArgsUsage:   "",
		Description: ``,
		Subcommands: []*cli.Command{
			{
				Name:      "txs",
				Usage:     "Export All N42 Transactions",
				ArgsUsage: "",
				Action:    exportTransactions,
				Flags: []cli.Flag{
					DataDirFlag,
				},
				Description: ``,
			},
			{
				Name:      "balance",
				Usage:     "Export All N42 account balance",
				ArgsUsage: "",
				Action:    exportBalance,
				Flags: []cli.Flag{
					DataDirFlag,
				},
				Description: ``,
			},
			{
				Name:      "dbState",
				Usage:     "Export All MDBX Buckets disk space",
				ArgsUsage: "",
				Action:    exportDBState,
				Flags: []cli.Flag{
					DataDirFlag,
				},
				Description: ``,
			},
			{
				Name:      "dbCopy",
				Usage:     "copy data from '--chaindata' to '--chaindata.to'",
				ArgsUsage: "",
				Action:    dbCopy,
				Flags: []cli.Flag{
					FromDataDirFlag,
					ToDataDirFlag,
				},
				Description: ``,
			},
		},
	}
)

func exportTransactions(ctx *cli.Context) error {
	stack, err := node.NewNode(ctx, &DefaultConfig)
	if err != nil {
		return err
	}

	blockChain := stack.BlockChain()
	defer stack.Close()

	currentBlock := blockChain.CurrentBlock()

	for i := uint64(0); i < currentBlock.Number64().Uint64(); i++ {

		block, err := blockChain.GetBlockByNumber(uint256.NewInt(i + 1))
		if err != nil {
			return fmt.Errorf("cannot get block at height %d: %w", i+1, err)
		}
		for _, transaction := range block.Transactions() {
			if transaction.To() == nil {
				continue
			}
			fmt.Printf("%d,%s,%s,%d,%s,%.2f\n",
				block.Number64().Uint64(),
				time.Unix(int64(block.Time()), 0).Format(time.RFC3339),
				transaction.From().Hex(),
				transaction.Nonce(),
				transaction.To().Hex(),
				new(big.Float).Quo(new(big.Float).SetInt(transaction.Value().ToBig()), new(big.Float).SetInt(big.NewInt(params.N))),
			)
		}
	}

	return nil
}

func exportBalance(ctx *cli.Context) error {
	stack, err := node.NewNode(ctx, &DefaultConfig)
	if err != nil {
		return err
	}
	db := stack.Database()
	defer stack.Close()

	roTX, err := db.BeginRo(ctx.Context)
	if err != nil {
		return err
	}
	defer roTX.Rollback()
	srcC, err := roTX.Cursor("Account")
	if err != nil {
		return err
	}

	for k, v, err := srcC.First(); k != nil; k, v, err = srcC.Next() {
		if err != nil {
			return err
		}
		var acc account.StateAccount
		if err = acc.DecodeForStorage(v); err != nil {
			return err
		}

		fmt.Printf("%x, %.2f\n",
			k,
			new(big.Float).Quo(new(big.Float).SetInt(acc.Balance.ToBig()), new(big.Float).SetInt(big.NewInt(params.N))),
		)
	}

	return nil
}

func exportDBState(ctx *cli.Context) error {
	stack, err := node.NewNode(ctx, &DefaultConfig)
	if err != nil {
		return err
	}
	db := stack.Database()
	defer stack.Close()

	var tsize uint64

	roTX, err := db.BeginRo(ctx.Context)
	if err != nil {
		return err
	}
	defer roTX.Rollback()

	migrator, ok := roTX.(kv.BucketMigrator)
	if !ok {
		return fmt.Errorf("cannot open db as BucketMigrator")
	}
	Buckets, err := migrator.ListBuckets()
	if err != nil {
		return fmt.Errorf("failed to list buckets: %w", err)
	}
	for _, Bucket := range Buckets {
		size, _ := roTX.BucketSize(Bucket)
		tsize += size
		cursor, err := roTX.Cursor(Bucket)
		if err != nil {
			log.Warn("Failed to create cursor", "bucket", Bucket, "err", err)
			continue
		}
		count, _ := cursor.Count()
		cursor.Close()
		if count != 0 {
			fmt.Printf("%30v count %10d size: %s \r\n", Bucket, count, common.StorageSize(size))
		}
	}
	fmt.Printf("total %s \n", common.StorageSize(tsize))
	return nil
}

func dbCopy(ctx *cli.Context) error {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	fromChaindata := ctx.String(FromDataDirFlag.Name)
	toChaindata := ctx.String(ToDataDirFlag.Name)

	if err := validateDirectory(fromChaindata, "fromChaindata"); err != nil {
		return err
	}
	if err := validateDirectory(toChaindata, "toChaindata"); err != nil {
		return err
	}

	from, to := backup.OpenPair(fromChaindata, toChaindata, kv.ChainDB, 0)
	defer from.Close()
	defer to.Close()
	err := backup.Kv2kv(ctx.Context, from, to, nil, backup.ReadAheadThreads)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	return nil
}

func validateDirectory(path, name string) error {
	f, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s error: %w", name, err)
	}
	if !f.IsDir() {
		return fmt.Errorf("%s is not a directory: %s", name, path)
	}
	return nil
}
