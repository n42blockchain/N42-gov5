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
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/urfave/cli/v2"

	common "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/node"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

var dbCommand = &cli.Command{
	Name:  "db",
	Usage: "Database inspection and debugging tools",
	Subcommands: []*cli.Command{
		{
			Name:      "stats",
			Usage:     "Show table sizes and record counts",
			ArgsUsage: "",
			Action:    dbStats,
			Flags: []cli.Flag{
				DataDirFlag,
			},
			Description: `Display all database tables with record counts and disk usage.`,
		},
		{
			Name:      "list",
			Usage:     "List all table names",
			ArgsUsage: "",
			Action:    dbList,
			Flags: []cli.Flag{
				DataDirFlag,
			},
			Description: `List all table names in the database.`,
		},
		{
			Name:      "get",
			Usage:     "Get value by table and hex key",
			ArgsUsage: "<table> <hex-key>",
			Action:    dbGet,
			Flags: []cli.Flag{
				DataDirFlag,
			},
			Description: `Read a specific key-value pair from the database.
The key must be provided as a hex string (with or without 0x prefix).`,
		},
		{
			Name:      "inspect",
			Usage:     "Browse table entries",
			ArgsUsage: "<table> [limit]",
			Action:    dbInspect,
			Flags: []cli.Flag{
				DataDirFlag,
				&cli.IntFlag{
					Name:  "limit",
					Usage: "Maximum entries to display",
					Value: 10,
				},
				&cli.StringFlag{
					Name:  "start",
					Usage: "Start key (hex) for seeking",
					Value: "",
				},
			},
			Description: `Browse entries in a database table.
Shows hex-encoded key-value pairs. Use --limit to control output size.
Use --start to seek to a specific position.`,
		},
	},
}

// dbStats shows table sizes and record counts (same as existing exportDBState but standalone).
func dbStats(ctx *cli.Context) error {
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

	migrator, ok := roTX.(kv.BucketMigrator)
	if !ok {
		return fmt.Errorf("cannot open db as BucketMigrator")
	}
	buckets, err := migrator.ListBuckets()
	if err != nil {
		return fmt.Errorf("failed to list buckets: %w", err)
	}

	var totalSize uint64
	fmt.Printf("%-35s %12s %12s\n", "TABLE", "COUNT", "SIZE")
	fmt.Printf("%s\n", strings.Repeat("-", 62))

	for _, bucket := range buckets {
		size, _ := roTX.BucketSize(bucket)
		totalSize += size
		cursor, err := roTX.Cursor(bucket)
		if err != nil {
			log.Warn("Failed to create cursor", "bucket", bucket, "err", err)
			continue
		}
		count, _ := cursor.Count()
		cursor.Close()
		if count != 0 {
			fmt.Printf("%-35s %12d %12s\n", bucket, count, common.StorageSize(size))
		}
	}
	fmt.Printf("%s\n", strings.Repeat("-", 62))
	fmt.Printf("%-35s %12s %12s\n", "TOTAL", "", common.StorageSize(totalSize))

	return nil
}

// dbList lists all table names.
func dbList(ctx *cli.Context) error {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

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

	migrator, ok := roTX.(kv.BucketMigrator)
	if !ok {
		return fmt.Errorf("cannot open db as BucketMigrator")
	}
	buckets, err := migrator.ListBuckets()
	if err != nil {
		return fmt.Errorf("failed to list buckets: %w", err)
	}

	fmt.Printf("Database tables (%d):\n", len(buckets))
	for i, bucket := range buckets {
		fmt.Printf("  %3d. %s\n", i+1, bucket)
	}
	return nil
}

// dbGet reads a specific key from a table.
func dbGet(ctx *cli.Context) error {
	if ctx.NArg() < 2 {
		return fmt.Errorf("usage: n42 db get <table> <hex-key>")
	}

	table := ctx.Args().Get(0)
	keyHex := ctx.Args().Get(1)
	keyHex = strings.TrimPrefix(keyHex, "0x")

	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return fmt.Errorf("invalid hex key: %w", err)
	}

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

	val, err := roTX.GetOne(table, key)
	if err != nil {
		return fmt.Errorf("get error: %w", err)
	}

	if val == nil {
		fmt.Printf("Key not found in table %q\n", table)
		return nil
	}

	fmt.Printf("Table: %s\n", table)
	fmt.Printf("Key:   %x (%d bytes)\n", key, len(key))
	fmt.Printf("Value: %x (%d bytes)\n", val, len(val))
	return nil
}

// dbInspect browses entries in a table.
func dbInspect(ctx *cli.Context) error {
	if ctx.NArg() < 1 {
		return fmt.Errorf("usage: n42 db inspect <table> [--limit N] [--start hex-key]")
	}

	table := ctx.Args().Get(0)
	limit := ctx.Int("limit")
	startHex := ctx.String("start")

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

	cursor, err := roTX.Cursor(table)
	if err != nil {
		return fmt.Errorf("cannot open cursor for %q: %w", table, err)
	}
	defer cursor.Close()

	var k, v []byte
	if startHex != "" {
		startHex = strings.TrimPrefix(startHex, "0x")
		startKey, err := hex.DecodeString(startHex)
		if err != nil {
			return fmt.Errorf("invalid start key: %w", err)
		}
		k, v, err = cursor.Seek(startKey)
	} else {
		k, v, err = cursor.First()
	}
	if err != nil {
		return fmt.Errorf("cursor error: %w", err)
	}

	fmt.Printf("Table: %s (showing up to %d entries)\n", table, limit)
	fmt.Printf("%s\n", strings.Repeat("-", 80))

	count := 0
	for k != nil && count < limit {
		keyStr := hex.EncodeToString(k)
		valStr := hex.EncodeToString(v)

		// Truncate long values for display
		if len(valStr) > 128 {
			valStr = valStr[:128] + fmt.Sprintf("... (%d bytes total)", len(v))
		}

		fmt.Printf("[%d] key(%d): %s\n    val(%d): %s\n",
			count+1, len(k), keyStr, len(v), valStr)

		k, v, err = cursor.Next()
		if err != nil {
			return fmt.Errorf("cursor error: %w", err)
		}
		count++
	}

	if count == 0 {
		fmt.Println("(empty table)")
	} else {
		fmt.Printf("%s\nShowed %d entries\n", strings.Repeat("-", 80), count)
	}
	return nil
}
