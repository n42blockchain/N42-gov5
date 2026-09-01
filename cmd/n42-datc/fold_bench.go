// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// fold-bench — measure the cost of folding ONE depth-1 account subtree from
// leaf history as of a historical height N. This is the core operation of the
// proposed fast-proof scheme: given the 16 depth-1 subtree hashes at N (a dense
// per-block index, built separately), a proof only needs to fold the single
// ON-PATH depth-1 subtree (1/16 of the account trie) instead of reconstructing
// the whole tree. This tool validates that per-subtree fold cost before we
// invest in building the dense index.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func runFoldBench(args []string) {
	fs := flag.NewFlagSet("fold-bench", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (from build)")
	at := fs.Uint64("at", 0, "historical block height to fold as-of")
	nib := fs.Int("nib", 0, "depth-1 nibble to fold (0..15); -1 = all 16 (= whole-tree cost)")
	foldDepth := fs.Int("fold-depth", 4, "fold depth (verify-side flag; unused for the raw subtree read)")
	build := fs.Bool("build", true, "also build+hash the subtree MPT (proof-path cost)")
	fastEOA := fs.Bool("fast-eoa", false, "skip storage-root reconstruction for empty-code accounts (EOAs)")
	mapGB := fs.Int("map.gb", 1024, "MDBX map size GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}

	logger := log.New()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(logger).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	metaV, err := tx.GetOne(tDatcMeta, []byte("head"))
	if err != nil || len(metaV) < 8 {
		die("DATC meta missing: %v", err)
	}
	head := binary.BigEndian.Uint64(metaV)
	if *at == 0 || *at >= head {
		die("--at must be in (0, head=%d)", head)
	}
	schedV, _ := tx.GetOne(tDatcMeta, []byte("sched"))
	var sched epochSchedule
	for d := 0; d <= maxChgDepth && (d+1)*8 <= len(schedV); d++ {
		sched.e[d] = binary.BigEndian.Uint64(schedV[d*8:])
	}
	fmt.Printf("fold-bench: head=%d at=%d sched.e=%v\n", head, *at, sched.e)

	q := &querier{tx: tx, sched: sched, foldDepth: *foldDepth, fastEOA: *fastEOA}
	cache := newFrameLRU()
	openSeg := func(tab int) *leafSegSet {
		s, ok, e := openLeafSegSet(*out, tab, cache)
		if e != nil || !ok {
			return nil
		}
		return s
	}
	q.segA, q.segS = openSeg(segTabLeafA), openSeg(segTabLeafS)
	q.segCA, q.segCS = openSeg(segTabChgA), openSeg(segTabChgS)

	nibs := []int{}
	if *nib >= 0 {
		nibs = []int{*nib}
	} else {
		for i := 0; i < 16; i++ {
			nibs = append(nibs, i)
		}
	}

	var totLeaves int
	var totRead, totBuild time.Duration
	for _, nb := range nibs {
		t0 := time.Now()
		leaves, ferr := q.subtreeLeaves(nil, []byte{byte(nb)}, *at)
		dRead := time.Since(t0)
		if ferr != nil {
			fmt.Printf("  nib=%x subtreeLeaves ERROR: %v\n", nb, ferr)
			continue
		}
		var dBuild time.Duration
		if *build {
			t1 := time.Now()
			var path [][]byte
			_ = mptNodeRLP(leaves, 0, nil, &path) // build + hash the whole subtree
			dBuild = time.Since(t1)
		}
		totLeaves += len(leaves)
		totRead += dRead
		totBuild += dBuild
		fmt.Printf("  nib=%x leaves=%d  read(asOfLeaves)=%v  build+hash=%v  subtotal=%v\n",
			nb, len(leaves), dRead.Round(time.Millisecond), dBuild.Round(time.Millisecond), (dRead + dBuild).Round(time.Millisecond))
	}
	fmt.Printf("=== %d nibble(s): leaves=%d  read=%v  build=%v  TOTAL=%v\n",
		len(nibs), totLeaves, totRead.Round(time.Millisecond), totBuild.Round(time.Millisecond), (totRead + totBuild).Round(time.Millisecond))
	fmt.Printf("(one on-path depth-1 subtree = the per-proof fold cost; ×16 ≈ whole-tree/current cost)\n")
}
