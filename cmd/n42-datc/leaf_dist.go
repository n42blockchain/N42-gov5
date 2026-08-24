// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// n42-datc leaf-dist — histograms the per-key version count in the leaf
// history (leafseg). The temporal point-query cost (as-of floor) is driven by
// this distribution: keys with 1 version are ~free, keys with millions of
// versions are what make a fold read tens of millions of entries. This tells
// us whether a plain (key,block) binary search suffices or whether the hot
// tail needs an Elias-Fano / inverted-index predecessor.

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"encoding/binary"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func runLeafDist(args []string) {
	fs := flag.NewFlagSet("leaf-dist", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (leafseg/)")
	table := fs.Int("table", segTabLeafS, "0=leafA(account,keylen32) 1=leafS(storage,keylen72)")
	keyLen := fs.Int("keylen", 0, "key length before the 8-byte block suffix (0=auto: 32 for leafA, 72 for leafS)")
	limit := fs.Int64("limit", 200_000_000, "max leaf entries to scan")
	mapGB := fs.Int("map.gb", 512, "MDBX map GB")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}
	if *keyLen == 0 {
		if *table == segTabLeafA {
			*keyLen = 32
		} else {
			*keyLen = 72
		}
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(*out).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		die("open: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		die("begin: %v", err)
	}
	defer tx.Rollback()

	cache := newFrameLRU()
	seg, ok, err := openLeafSegSet(*out, *table, cache)
	if err != nil || !ok {
		die("open leafseg table %d: %v (ok=%v) — finalize-leaves first?", *table, err, ok)
	}
	c := seg.Cursor()

	// Log2-ish buckets for version counts.
	edges := []int64{1, 2, 5, 17, 65, 257, 1025, 4097, 16385, 65537, 262145, 1048577, 4194305}
	labels := []string{"1", "2-4", "5-16", "17-64", "65-256", "257-1K", "1K-4K", "4K-16K",
		"16K-64K", "64K-256K", "256K-1M", "1M-4M", ">4M"}
	hist := make([]int64, len(labels))
	bucketOf := func(n int64) int {
		i := sort.Search(len(edges), func(i int) bool { return edges[i] > n }) - 1
		if i < 0 {
			i = 0
		}
		return i
	}

	var scanned, distinctKeys, maxVer int64
	var totalVersionsInLongTail int64 // versions belonging to keys with >1K versions
	var maxKey []byte
	var curKey []byte
	var curCount int64

	// State-size measurement (checkpoint sizing): count keys whose FIRST version
	// is ≤ each threshold block = live keys at that block (upper bound; deletions
	// reduce it). Drives the checkpoint-materialization storage estimate.
	thresholds := []uint64{100000, 500000, 1000000, 2000000, 4000000, 8000000, 12000000, 15220000}
	liveAt := make([]int64, len(thresholds))
	var curFirstBlk uint64 = ^uint64(0)

	flush := func() {
		if curKey == nil {
			return
		}
		distinctKeys++
		hist[bucketOf(curCount)]++
		if curCount > maxVer {
			maxVer = curCount
			maxKey = append(maxKey[:0], curKey...)
		}
		if curCount > 1024 {
			totalVersionsInLongTail += curCount
		}
		for i, th := range thresholds {
			if curFirstBlk <= th {
				liveAt[i]++
			}
		}
	}

	k, _, err := c.Seek(nil)
	for k != nil && err == nil && scanned < *limit {
		scanned++
		if len(k) < *keyLen {
			k, _, err = c.Next()
			continue
		}
		key := k[:*keyLen]
		if curKey == nil || !bytes.Equal(key, curKey) {
			flush()
			curKey = append(curKey[:0], key...)
			curCount = 0
			// First entry of a key = its min block (leaves sorted by key,block).
			if len(k) >= *keyLen+8 {
				curFirstBlk = binary.BigEndian.Uint64(k[*keyLen : *keyLen+8])
			} else {
				curFirstBlk = 0
			}
		}
		curCount++
		k, _, err = c.Next()
	}
	flush()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scan error (partial results): %v\n", err)
	}

	fmt.Printf("leaf-dist table=%d keylen=%d: scanned %d entries, %d distinct keys\n", *table, *keyLen, scanned, distinctKeys)
	fmt.Printf("%-10s %14s %8s\n", "versions", "keys", "%keys")
	for i, lb := range labels {
		if hist[i] == 0 {
			continue
		}
		fmt.Printf("%-10s %14d %7.3f%%\n", lb, hist[i], 100*float64(hist[i])/float64(distinctKeys))
	}
	fmt.Printf("max versions on a single key: %d  (key=%x)\n", maxVer, maxKey)
	fmt.Printf("entries in long tail (keys with >1K versions): %d  (%.1f%% of scanned)\n",
		totalVersionsInLongTail, 100*float64(totalVersionsInLongTail)/float64(scanned))
	fmt.Printf("\nstate size (live keys, first-version ≤ block) — checkpoint sizing:\n")
	for i, th := range thresholds {
		fmt.Printf("  block ≤%-9d  live_keys=%-12d  (%.1f%% of all sampled keys)\n",
			th, liveAt[i], 100*float64(liveAt[i])/float64(distinctKeys))
	}
}
