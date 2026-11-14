// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// ethexec history and tx-lookup build/query subcommands.

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/cscompact"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func runStorHistBuild(c *cli.Context) error {
	db, err := openErigonDB(c.String("erigon-db"))
	if err != nil {
		return err
	}
	defer db.Close()

	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")
	if endBlock == 0 {
		endBlock, err = detectEndBlock(db, "StorageChangeSets", "StorageChangeSet")
		if err != nil {
			return err
		}
	}

	ctx, cancel := withShutdown()
	defer cancel()

	stoBuilder := cscompact.NewStorageHistoryBuilder(db, filepath.Join(c.String("datadir"), "chain"))
	return stoBuilder.BuildFromChangesets(ctx, startBlock, endBlock)
}

func runHistoryBuild(c *cli.Context) error {
	erigonDB := c.String("erigon-db")
	datadir := c.String("datadir")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")

	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(erigonDB).Label(kv.ChainDB).Readonly().Accede().
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open erigon mdbx: %w", err)
	}
	defer db.Close()
	log.Info("Erigon MDBX opened READ-ONLY", "path", erigonDB)

	if endBlock == 0 {
		tx, _ := db.BeginRo(context.Background())
		for _, tbl := range []string{
			"AccountChangeSets", "StorageChangeSets",
			"AccountChangeSet", "StorageChangeSet",
		} {
			cursor, err := tx.Cursor(tbl)
			if err != nil {
				continue
			}
			k, v, _ := cursor.Last()
			cursor.Close()
			if len(k) < 8 || len(v) > 64 {
				continue
			}
			bn := cscompact.DetectBlockNum(k)
			if bn > 0 && bn < 1<<32 && bn+1 > endBlock {
				endBlock = bn + 1
			}
		}
		tx.Rollback()
		if endBlock == 0 {
			return fmt.Errorf("cannot determine end block from changeset tables")
		}
		log.Info("Detected end block", "endBlock", endBlock)
	}

	ctx, cancel := withShutdown()
	defer cancel()

	histDir := filepath.Join(datadir, "chain", "freezer")

	fromCS := c.Bool("from-changesets")

	log.Info("=== Account History ===", "fromChangesets", fromCS)
	accBuilder := cscompact.NewAccountHistoryBuilder(db, histDir)
	if fromCS {
		if err := accBuilder.BuildFromChangesets(ctx, startBlock, endBlock); err != nil {
			return fmt.Errorf("account history: %w", err)
		}
	} else {
		if err := accBuilder.BuildRange(ctx, startBlock, endBlock); err != nil {
			return fmt.Errorf("account history: %w", err)
		}
	}

	log.Info("=== Storage History ===", "fromChangesets", fromCS)
	stoBuilder := cscompact.NewStorageHistoryBuilder(db, histDir)
	if fromCS {
		return stoBuilder.BuildFromChangesets(ctx, startBlock, endBlock)
	}
	return stoBuilder.BuildRange(ctx, startBlock, endBlock)
}

// runTxLookupQuery looks up a single tx hash, or self-verifies the segments:
// for each sampled block it decodes the real body from the geth ancient
// freezer, hashes first/middle/last tx, and checks Lookup returns exactly that
// block; plus a random-hash negative probe. Exits non-zero on any mismatch.
func runTxLookupQuery(c *cli.Context) error {
	dir := c.String("dir")
	svc, err := txlookup.NewService(dir)
	if err != nil {
		return fmt.Errorf("open segments: %w", err)
	}

	if txHex := c.String("tx"); txHex != "" {
		h := types.HexToHash(txHex)
		blk, err := svc.Lookup(nil, h)
		if err != nil {
			return err
		}
		if blk == nil {
			fmt.Printf("%s -> NOT FOUND\n", h.Hex())
		} else {
			fmt.Printf("%s -> block %d\n", h.Hex(), *blk)
		}
		return nil
	}

	ancientPath := c.String("ancient")
	if ancientPath == "" {
		return fmt.Errorf("--tx or --ancient required")
	}
	f, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer f.Close()

	// The segments are effectively no-LFP (recsplit only materializes the
	// existence fingerprint when Enums is on), so a bare Lookup phantom-hits
	// the newest segment for any foreign hash. Production wiring therefore
	// REQUIRES the verifier: confirm each candidate block really contains the
	// hash, else keep probing older segments. Mirror that here.
	svc.SetVerifier(func(blockNum uint64, txHash types.Hash) (uint64, bool) {
		bodyData, err := f.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			return 0, false
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			return 0, false
		}
		for i, tx := range body.Transactions {
			if tx.Hash() == txHash {
				return uint64(i), true
			}
		}
		return 0, false
	})

	var blocks []uint64
	for _, s := range strings.Split(c.String("blocks"), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return fmt.Errorf("bad block %q: %w", s, err)
		}
		blocks = append(blocks, n)
	}
	if len(blocks) == 0 {
		return fmt.Errorf("--blocks required with --ancient")
	}

	failed := 0
	checked := 0
	for _, blockNum := range blocks {
		bodyData, err := f.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			return fmt.Errorf("read body %d: %w", blockNum, err)
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			return fmt.Errorf("decode body %d: %w", blockNum, err)
		}
		if len(body.Transactions) == 0 {
			fmt.Printf("block %d: empty, skipped\n", blockNum)
			continue
		}
		samples := []int{0, len(body.Transactions) / 2, len(body.Transactions) - 1}
		for _, i := range samples {
			h := body.Transactions[i].Hash()
			got, err := svc.Lookup(nil, h)
			checked++
			switch {
			case err != nil:
				fmt.Printf("FAIL block %d tx[%d] %s: %v\n", blockNum, i, h.Hex(), err)
				failed++
			case got == nil:
				fmt.Printf("FAIL block %d tx[%d] %s: not found\n", blockNum, i, h.Hex())
				failed++
			case *got != blockNum:
				fmt.Printf("FAIL block %d tx[%d] %s: resolved to block %d\n", blockNum, i, h.Hex(), *got)
				failed++
			default:
				fmt.Printf("ok   block %d tx[%d] %s\n", blockNum, i, h.Hex())
			}
		}
	}

	// Negative probe: a hash that cannot exist must not resolve.
	var bogus types.Hash
	copy(bogus[:], []byte("txlookup-negative-probe-hash!!!!"))
	if got, err := svc.Lookup(nil, bogus); err != nil {
		fmt.Printf("FAIL negative probe: %v\n", err)
		failed++
	} else if got != nil {
		fmt.Printf("FAIL negative probe resolved to block %d (phantom)\n", *got)
		failed++
	} else {
		fmt.Printf("ok   negative probe not found\n")
	}
	checked++

	fmt.Printf("txlookup-query: %d checked, %d failed\n", checked, failed)
	if failed > 0 {
		return fmt.Errorf("%d verification failures", failed)
	}
	return nil
}

func runTxLookupBuild(c *cli.Context) error {
	ancientPath := c.String("ancient")
	erigonDB := c.String("erigon-db")
	datadir := c.String("datadir")
	startBlock := c.Uint64("start")
	endBlock := c.Uint64("end")

	outputDir := filepath.Join(datadir, "chain", "freezer")
	ctx, cancel := withShutdown()
	defer cancel()

	// Reth/Erigon MDBX path — read from TransactionHashNumbers + TransactionBlocks.
	if erigonDB != "" {
		logger := log2.New()
		db, err := mdbx.NewMDBX(logger).
			Path(erigonDB).Label(kv.ChainDB).Readonly().Accede().
			DBVerbosity(kv.DBVerbosityLvl(2)).
			Open(context.Background())
		if err != nil {
			return fmt.Errorf("open mdbx: %w", err)
		}
		defer db.Close()
		log.Info("MDBX opened READ-ONLY for txindex", "path", erigonDB)

		if endBlock == 0 {
			endBlock = detectRethEndBlock(db)
			if endBlock == 0 {
				return fmt.Errorf("cannot detect end block from MDBX")
			}
			log.Info("Detected end block", "endBlock", endBlock)
		}

		builder := txlookup.NewRethBuilder(db, outputDir)
		return builder.BuildRange(ctx, startBlock, endBlock)
	}

	// Geth ancient freezer path.
	if ancientPath == "" {
		return fmt.Errorf("--ancient or --erigon-db is required")
	}
	f, err := freezer.NewReadOnly(ancientPath)
	if err != nil {
		return fmt.Errorf("open input freezer: %w", err)
	}
	defer f.Close()
	log.Info("Input freezer opened", "frozen", f.Frozen())

	if endBlock == 0 {
		endBlock = f.Frozen()
	}

	builder := txlookup.NewSegmentBuilder(f, outputDir, ethelBodyTxHashes)
	return builder.BuildRange(ctx, startBlock, endBlock)
}
