// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// deriveplan.go — which contracts go on the exact ladder, and how deep.
//
// A contract without records is folded whole, which costs its keys: about
// 1.2 us per key ever written, plus ~15 us for each key with a long version
// run (the fold seeks to its floor instead of walking it). Records cost one
// hash per write from then on, so a contract is listed only when its whole
// fold exceeds --threshold-ms; its depth is the shallowest one whose fold unit
// fits --unit-ms (depth does not change the record volume, only the unit and
// the 16^k records read above the recorded level).
//
// derive-plan scans the storage leaf history and writes the contracts file
// derive-ns takes. With --exclude-listed it lists only contracts that are not
// on the ladder yet — after a weekly update, the ones that grew past the
// threshold.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// contractCensus is what the policy needs to know about one contract.
type contractCensus struct {
	dom             [stoDomainLen]byte
	rows, keys, hot uint64
	first, last     uint32
}

// censusBucket scans one storage leaf segment. minRows drops the long tail of
// tiny contracts (28M of them on mainnet) that no threshold would list.
func censusBucket(path string, hotVersions int, minRows uint64) ([]contractCensus, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	sf, err := loadLeafSegFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	it := &oldSegIter{sf: sf, zr: zr2()}
	defer it.close()
	var (
		out     []contractCensus
		cur     contractCensus
		have    bool
		lastKey [32]byte
		keyRows int
	)
	endKey := func() {
		if keyRows > hotVersions {
			cur.hot++
		}
		keyRows = 0
	}
	flush := func() {
		if !have {
			return
		}
		endKey()
		if cur.rows >= minRows {
			out = append(out, cur)
		}
	}
	for {
		ok, err := it.ensure()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		k := it.key()
		it.next()
		if len(k) != stoDomainLen+32+blkLen {
			continue
		}
		blk := uint32(stoBlockOf(k))
		if !have || string(k[:stoDomainLen]) != string(cur.dom[:]) {
			flush()
			cur = contractCensus{first: blk, last: blk}
			copy(cur.dom[:], k)
			copy(lastKey[:], k[stoDomainLen:])
			have = true
			cur.keys = 1
		} else if string(k[stoDomainLen:stoDomainLen+32]) != string(lastKey[:]) {
			endKey()
			copy(lastKey[:], k[stoDomainLen:])
			cur.keys++
		}
		cur.rows++
		keyRows++
		if blk < cur.first {
			cur.first = blk
		}
		if blk > cur.last {
			cur.last = blk
		}
	}
	flush()
	return out, nil
}

func runDerivePlan(args []string) {
	fs := flag.NewFlagSet("derive-plan", flag.ExitOnError)
	out := fs.String("out", "", "archive (reads the storage leaf history)")
	list := fs.String("list", "", "write the '<addrHash> <depth>' lines for derive-ns here")
	census := fs.String("census", "", "also write every scanned contract here: <addrHash> <rows> <keys> <hotKeys> <firstBlock> <lastBlock> <foldMs>")
	threshold := fs.Float64("threshold-ms", 150, "list a contract when folding its whole trie costs more than this")
	unit := fs.Float64("unit-ms", 35, "depth: the shallowest one whose fold unit costs at most this")
	keyUs := fs.Float64("key-us", 1.2, "fold cost per key ever written (us)")
	hotUs := fs.Float64("hot-us", 15, "extra fold cost per key with more than --hot-versions versions (us)")
	hotVersions := fs.Int("hot-versions", 4, "a key is hot above this many versions (the fold's linear budget)")
	excludeListed := fs.Bool("exclude-listed", true, "leave out contracts that already are in the archive's ns.ladders")
	workers := fs.Int("workers", 24, "segments scanned at once")
	_ = fs.Parse(args)
	if *out == "" || *list == "" {
		die("--out and --list required")
	}
	names, err := filepath.Glob(filepath.Join(*out, leafSegDir, "s.*.seg"))
	if err != nil || len(names) == 0 {
		die("no storage leaf segments under %s (err=%v)", *out, err)
	}
	listed := map[string]bool{}
	if *excludeListed {
		l, err := loadExactLadders(*out)
		if err != nil {
			die("ladders: %v", err)
		}
		if l != nil {
			for d := range l.m {
				listed[d] = true
			}
		}
	}
	// A contract below this many rows cannot reach the threshold.
	minRows := uint64(*threshold * 1000 / (*keyUs + *hotUs) / 4)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		all      []contractCensus
		firstErr error
		start    = time.Now()
		ch       = make(chan string)
	)
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range ch {
				cs, err := censusBucket(name, *hotVersions, minRows)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				all = append(all, cs...)
				mu.Unlock()
			}
		}()
	}
	for _, n := range names {
		ch <- n
	}
	close(ch)
	wg.Wait()
	if firstErr != nil {
		die("census: %v", firstErr)
	}
	sort.Slice(all, func(i, j int) bool { return string(all[i].dom[:]) < string(all[j].dom[:]) })

	lf, err := os.Create(*list)
	if err != nil {
		die("list: %v", err)
	}
	defer lf.Close()
	lw := bufio.NewWriter(lf)
	defer lw.Flush()
	var cw *bufio.Writer
	if *census != "" {
		cf, err := os.Create(*census)
		if err != nil {
			die("census: %v", err)
		}
		defer cf.Close()
		cw = bufio.NewWriter(cf)
		defer cw.Flush()
	}
	var byDepth [maxExactDepth + 1]int
	var planned, skipped int
	var rows uint64
	for _, c := range all {
		ms := (float64(c.keys)**keyUs + float64(c.hot)**hotUs) / 1000
		if cw != nil {
			fmt.Fprintf(cw, "%x %d %d %d %d %d %.1f\n", c.dom, c.rows, c.keys, c.hot, c.first, c.last, ms)
		}
		if ms <= *threshold {
			continue
		}
		if listed[string(c.dom[:])] {
			skipped++
			continue
		}
		depth := 0
		for u := ms; u > *unit && depth < maxExactDepth; u /= 16 {
			depth++
		}
		fmt.Fprintf(lw, "%x %d\n", c.dom, depth)
		byDepth[depth]++
		planned++
		rows += c.rows
	}
	fmt.Printf("derive-plan: %d contracts scanned (>= %d rows), %d over %.0f ms already listed, %d to derive (%d rows; depth 1/2/3 = %d/%d/%d) in %s\n",
		len(all), minRows, skipped, *threshold, planned, rows, byDepth[1], byDepth[2], byDepth[3], time.Since(start).Truncate(time.Second))
}
