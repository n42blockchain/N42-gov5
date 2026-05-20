// n42-zstd-dict-spike measures how much additional compression a
// trained zstd dictionary buys for an N42 freezer table that's
// already batch-compressed with SpeedBestCompression.
//
// Method:
//
//  1. Open <src>/<table>.cdat via the existing FreezerTable reader.
//  2. Sample N items uniformly across the table.
//  3. Train dict on the first half, hold out the second half.
//  4. For each held-out item, compress:
//     - Standalone zstd best, no dict          (baseline)
//     - Standalone zstd best, with trained dict
//  5. Report per-item bytes for both and the saving ratio.
//
// Real impact on the *table* depends on batch size: a batched cdat
// already amortises shared patterns across the batch, so the marginal
// dict win shrinks. We report standalone-item numbers (worst case for
// the baseline / best case for dict) so the upper bound is visible.
package main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func main() {
	src := flag.String("src", `D:\n42-eth1\chain\freezer`, "freezer directory")
	table := flag.String("table", "headerc", "table prefix (e.g. headerc, bodyc, receipts)")
	ext := flag.String("ext", "c", "compressed ext (c) or raw (r)")
	samples := flag.Int("samples", 5000, "items to sample (half trains, half tests)")
	dictSizeKB := flag.Int("dict-kb", 64, "target dictionary size in KB (zstd default 110 KB)")
	itemMin := flag.Int("item-min", -1, "skip items below this id (default: tail half of table to dodge prunes)")
	seed := flag.Int64("seed", 0, "PRNG seed (0=time)")
	flag.Parse()

	if *seed == 0 {
		*seed = time.Now().UnixNano()
	}
	rng := rand.New(rand.NewSource(*seed))

	t0 := time.Now()
	tbl, err := freezer.NewFreezerTableCompressedReadOnly(*src, *table, *ext)
	if err != nil {
		fatal("open %s/%s.%s.cdat: %v", *src, *table, *ext, err)
	}
	defer tbl.Close()
	total := tbl.Items()
	if total == 0 {
		fatal("empty table")
	}
	if uint64(*samples)*2 > total {
		fatal("samples*2 > items (%d > %d)", *samples*2, total)
	}
	// Default to the tail half: in N42-eth1 the head of headerc/bodyc is
	// pruned, so retrieving low ids fails. User can override via flag.
	minID := uint64(*itemMin)
	if *itemMin < 0 {
		minID = total / 2
	}
	if minID+uint64(*samples)*2 > total {
		fatal("not enough items above min: items=%d min=%d need=%d", total, minID, *samples*2)
	}
	fmt.Printf("table %s: %d items, sampling from [%d, %d)\n", *table, total, minID, total)

	// Uniform sample of 2N item ids in [minID, total).
	pool := make([]uint64, *samples*2)
	picked := make(map[uint64]struct{}, len(pool))
	span := int64(total - minID)
	for i := 0; i < len(pool); i++ {
		for {
			id := minID + uint64(rng.Int63n(span))
			if _, dup := picked[id]; !dup {
				picked[id] = struct{}{}
				pool[i] = id
				break
			}
		}
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i] < pool[j] })

	// Read all samples up front. Track raw sizes.
	rawItems := make([][]byte, 0, len(pool))
	var totalRaw int64
	var skippedPrune int
	readT0 := time.Now()
	for _, id := range pool {
		data, err := tbl.Retrieve(id)
		if err != nil {
			// Skip pruned ids gracefully so a partially-pruned table
			// doesn't kill the whole run.
			if err.Error() == "freezer: data pruned" {
				skippedPrune++
				continue
			}
			fatal("retrieve %d: %v", id, err)
		}
		buf := make([]byte, len(data))
		copy(buf, data)
		rawItems = append(rawItems, buf)
		totalRaw += int64(len(data))
	}
	if skippedPrune > 0 {
		fmt.Printf("skipped %d pruned ids; using %d live items\n", skippedPrune, len(rawItems))
	}
	if len(rawItems) < 200 {
		fatal("too few live items (%d); raise --samples or set --item-min higher", len(rawItems))
	}
	fmt.Printf("read %d items, %.2f MB raw, %.1fs (avg %.0f B/item)\n",
		len(rawItems), float64(totalRaw)/1e6,
		time.Since(readT0).Seconds(), float64(totalRaw)/float64(len(rawItems)))

	// Train dict on first half.
	half := len(rawItems) / 2
	trainSet := rawItems[:half]
	holdSet := rawItems[half:]

	// klauspost/compress BuildDict expects a pre-assembled `History`
	// (the dictionary CONTENT). It only computes the metadata tables;
	// it does not extract frequent patterns like zstd CLI's --train.
	// Simple workable approximation: concatenate samples up to the
	// target dict size and use that as History. Good enough to measure
	// "sample-based context vs no context" for the dict win.
	trainT0 := time.Now()
	targetBytes := *dictSizeKB * 1024
	var history []byte
	for _, s := range trainSet {
		if len(history)+len(s) > targetBytes {
			break
		}
		history = append(history, s...)
	}
	if len(history) < 8 {
		fatal("training set too small to assemble history (%d bytes)", len(history))
	}
	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:         1,
		Contents:   trainSet,
		History:    history,
		Level:      zstd.SpeedBestCompression,
		CompatV155: true,
	})
	if err != nil {
		fatal("BuildDict: %v", err)
	}
	fmt.Printf("dict: history=%d bytes from %d samples, output=%d bytes (%.1fs)\n",
		len(history), len(trainSet), len(dict), time.Since(trainT0).Seconds())

	// Compress hold-out set: no dict baseline.
	encNoDict, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithEncoderConcurrency(1))
	if err != nil {
		fatal("encoder no-dict: %v", err)
	}
	defer encNoDict.Close()

	encWithDict, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithEncoderDict(dict),
		zstd.WithEncoderConcurrency(1))
	if err != nil {
		fatal("encoder with-dict: %v", err)
	}
	defer encWithDict.Close()

	var (
		holdRaw         int64
		holdNoDict      int64
		holdWithDict    int64
		nNoDictBigger   int
		nWithDictBigger int
	)
	for _, item := range holdSet {
		holdRaw += int64(len(item))
		noDict := encNoDict.EncodeAll(item, nil)
		withDict := encWithDict.EncodeAll(item, nil)
		holdNoDict += int64(len(noDict))
		holdWithDict += int64(len(withDict))
		if len(noDict) > len(item) {
			nNoDictBigger++
		}
		if len(withDict) > len(item) {
			nWithDictBigger++
		}
	}

	fmt.Println()
	fmt.Printf("=== held-out set: %d items, %.2f MB raw ===\n", len(holdSet), float64(holdRaw)/1e6)
	fmt.Printf("  baseline (no dict)         %12.2f MB  %.3f bytes/byte  (%d items grew)\n",
		float64(holdNoDict)/1e6, float64(holdNoDict)/float64(holdRaw), nNoDictBigger)
	fmt.Printf("  with trained dict          %12.2f MB  %.3f bytes/byte  (%d items grew)\n",
		float64(holdWithDict)/1e6, float64(holdWithDict)/float64(holdRaw), nWithDictBigger)
	if holdNoDict > 0 {
		saving := 100 * (float64(holdNoDict-holdWithDict)) / float64(holdNoDict)
		fmt.Printf("  dict net savings vs baseline       %.1f%%\n", saving)
	}
	fmt.Printf("  dict overhead amortised per held item: %.1f bytes\n",
		float64(len(dict))/float64(len(holdSet)))

	fmt.Printf("\ntotal elapsed: %s\n", time.Since(t0).Truncate(time.Second))
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
