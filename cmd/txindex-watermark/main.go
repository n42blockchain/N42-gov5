// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// txindex-watermark reads or sets the tx-lookup tail-tier start watermark
// ("txindex-tail-start" in the ChainConfig table) of an n42 chaindata.
//
// Why this exists: the indexer sets the watermark to head+1 the first time
// it runs, deliberately refusing to back-scan history. That is the right
// default for a live node, but it is wrong for a SEED artifact: a seed is
// supposed to ship an index that already covers its chain, so every node
// reseeded from it can answer tx-hash lookups for the whole range instead
// of only for blocks produced after the reseed.
//
// The weekly seed build therefore sets the watermark to the first block the
// seed's index does not yet cover, starts one node against the seed once
// (the indexer seals the backlog into segments), and ships the resulting
// txindex directory inside the seed.
//
// Numbering note: replay-v2 is append-only, so blocks already present in
// the base keep their heights forever and their index entries stay valid
// across generations. Only the week being folded in is renumbered (gap
// fill), which is exactly the range the weekly run has to index.
//
//	txindex-watermark --db E:/qs-era-out/chaindata            # read
//	txindex-watermark --db E:/qs-era-out/chaindata --set 14266473
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

var watermarkKey = []byte("txindex-tail-start")

func main() {
	dbPath := flag.String("db", "", "chaindata directory (required)")
	set := flag.Int64("set", -1, "block to set as the tail-tier start (omit to read)")
	clear := flag.Bool("clear", false, "delete the watermark (next start reverts to head+1)")
	flag.Parse()

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "usage: txindex-watermark --db <chaindata> [--set N | --clear]")
		os.Exit(2)
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	ctx := context.Background()

	readonly := *set < 0 && !*clear
	b := mdbx.NewMDBX(log.New()).Path(*dbPath).Label(kv.ChainDB).
		MapSize(4 * datasize.TB).Accede()
	if readonly {
		b = b.Readonly()
	}
	db, err := b.Open(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	show := func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.ChainConfig, watermarkKey)
		if err != nil {
			return err
		}
		if len(v) != 8 {
			fmt.Println("watermark: (unset — the indexer will start at head+1)")
			return nil
		}
		fmt.Printf("watermark: %d\n", binary.BigEndian.Uint64(v))
		return nil
	}

	if readonly {
		if err := db.View(ctx, show); err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		return
	}

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if *clear {
			if err := tx.Delete(modules.ChainConfig, watermarkKey); err != nil {
				return err
			}
			fmt.Println("watermark cleared")
			return nil
		}
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(*set))
		if err := tx.Put(modules.ChainConfig, watermarkKey, buf[:]); err != nil {
			return err
		}
		fmt.Printf("watermark set to %d\n", *set)
		return nil
	}); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
}
