// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package datc

import (
	"bufio"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
)

// runSegCount scans the storage leaf-history segments and derives, per
// contract, the record shape a rebuild should use:
//
//   - depth from the contract's DISTINCT slot keys K, so a fold walks about
//     --fold-width keys: d = ceil(log16(K / width));
//   - which level, if any, to record per block. A proof recurses into every
//     child that changed inside each level's window, so the folds it ends up
//     doing multiply down the trie (16 x 16 x ... for a hot contract). A level
//     recorded per block is exact at every height and stops that recursion
//     off the proof path. Cutting costs min(16^level, writes/block) records per
//     block, so the policy takes the SHALLOWEST level whose residual folds fit
//     --fold-target; a contract that fits without any cut gets none.
//
// Both come from the history that is actually there, which is what "size by
// the reads a proof costs" means in practice.
func runSegCount(args []string) {
	fs := flag.NewFlagSet("segcount", flag.ExitOnError)
	out := fs.String("out", "", "DATC dir (reads leafseg/)")
	top := fs.Int("top", 40, "print this many heaviest contracts")
	mapPath := fs.String("map", "", "write the per-contract '<addrHash> <depth> <shift>' map here")
	width := fs.Uint64("fold-width", 512, "target keys per fold (B)")
	foldTarget := fs.Float64("fold-target", 300, "average folds per proof above which the deepest level goes per block")
	blocks := fs.Uint64("blocks", 0, "chain length the history covers (for the write rate); 0 = last block seen + 1")
	stoSchedStr := fs.String("sto-sched", "1024,1024,1024,1024,4096,4096", "storage ladder the rebuild will use")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}
	sto, err := parseSchedule(*stoSchedStr)
	if err != nil {
		die("--sto-sched: %v", err)
	}
	set, ok, err := openLeafSegSet(*out, leafTableS, newFrameLRU())
	if err != nil || !ok {
		die("open storage leaf segments: ok=%v err=%v", ok, err)
	}
	defer set.Close()

	depthFor := func(keys uint64) int {
		d := 0
		for w := *width; keys > w; w *= 16 {
			d++
		}
		return d
	}
	type ent struct {
		dom  [stoDomainLen]byte
		rows uint64
		keys uint64
	}
	var all []ent
	var cur ent
	var lastKey []byte
	var totRows, totKeys, lastBlock uint64
	flush := func() {
		if cur.keys > 0 {
			all = append(all, cur)
		}
	}
	c := set.Cursor()
	for k, _, e := c.Seek([]byte{0}); k != nil && e == nil; k, _, e = c.Next() {
		if len(k) != stoDomainLen+32+blkLen {
			continue
		}
		totRows++
		var dom [stoDomainLen]byte
		copy(dom[:], k[:stoDomainLen])
		if dom != cur.dom {
			flush()
			cur = ent{dom: dom}
			lastKey = nil
		}
		cur.rows++
		key := k[:stoDomainLen+32]
		if lastKey == nil || string(key) != string(lastKey) {
			cur.keys++
			totKeys++
			lastKey = append(lastKey[:0], key...)
		}
		if b := uint64(k[stoDomainLen+32])<<24 | uint64(k[stoDomainLen+33])<<16 | uint64(k[stoDomainLen+34])<<8 | uint64(k[stoDomainLen+35]); b > lastBlock {
			lastBlock = b
		}
	}
	flush()
	if *blocks == 0 {
		*blocks = lastBlock + 1
	}
	fmt.Printf("storage leaf history: rows=%d distinctKeys=%d contracts=%d blocks=%d\n", totRows, totKeys, len(all), *blocks)

	// Shift that takes level d to one block on this ladder.
	perBlockShift := func(d int) uint8 {
		s := uint8(0)
		for l := sto.e[d]; l > 1 && s < maxStoShift; l >>= 1 {
			s++
		}
		return s
	}
	// residualFolds estimates the folds one proof does when level cut (-1 =
	// none) is recorded per block: below the cut only the proof path
	// continues, and at each level its node recurses into the children that
	// changed inside that level's window — about w*s[d]/2 writes spread over
	// 16^d nodes, capped at 16 per node.
	residualFolds := func(w float64, d, cut int) float64 {
		v := 1.0
		for lv := cut + 1; lv < d; lv++ {
			c := w * float64(sto.e[lv]) / 2 / math.Pow(16, float64(lv))
			if c > 16 {
				c = 16
			}
			if c < 1 {
				c = 1
			}
			v *= c
		}
		return v
	}
	var depthHist [8]uint64
	var cutHist [8]uint64
	var shifted, extraPerBlock float64
	var mapW *bufio.Writer
	if *mapPath != "" {
		f, err := os.Create(*mapPath)
		if err != nil {
			die("create map: %v", err)
		}
		defer f.Close()
		mapW = bufio.NewWriterSize(f, 1<<20)
		defer mapW.Flush()
	}
	type heavy struct {
		ent
		d, cut int
		shift  uint8
		folds0 float64
		folds  float64
	}
	var hv []heavy
	for _, e := range all {
		d := depthFor(e.keys)
		if d < len(depthHist) {
			depthHist[d]++
		}
		w := float64(e.rows) / float64(*blocks)
		cut, shift := -1, uint8(0)
		folds0 := residualFolds(w, d, -1)
		folds := folds0
		if d >= 1 && folds0 > *foldTarget {
			for lv := 0; lv < d; lv++ {
				if f := residualFolds(w, d, lv); f <= *foldTarget {
					cut, shift, folds = lv, perBlockShift(lv), f
					break
				}
			}
			if cut < 0 { // nothing fits: cut as deep as possible
				cut, shift, folds = d-1, perBlockShift(d-1), residualFolds(w, d, d-1)
			}
			shifted++
			cost := math.Min(math.Pow(16, float64(cut)), w)
			extraPerBlock += cost
			if cut < len(cutHist) {
				cutHist[cut]++
			}
		}
		if mapW != nil && (d > 0 || shift > 0) {
			if shift > 0 {
				fmt.Fprintf(mapW, "%x %d %d %d\n", e.dom, d, cut, shift)
			} else {
				fmt.Fprintf(mapW, "%x %d\n", e.dom, d)
			}
		}
		if e.keys >= 100_000 {
			hv = append(hv, heavy{e, d, cut, shift, folds0, folds})
		}
	}
	fmt.Printf("depth policy (fold width %d): ", *width)
	for d, n := range depthHist {
		if n > 0 {
			fmt.Printf("d%d=%d ", d, n)
		}
	}
	fmt.Printf("\nper-block cuts (fold target %.0f): %.0f contracts; by level: ", *foldTarget, shifted)
	for lv, n := range cutHist {
		if n > 0 {
			fmt.Printf("L%d=%d ", lv, n)
		}
	}
	fmt.Printf("\n  extra records ≈ %.1f per block ≈ %.2fe9 over %d blocks\n", extraPerBlock, extraPerBlock*float64(*blocks)/1e9, *blocks)
	sort.Slice(hv, func(i, j int) bool { return hv[i].folds0 > hv[j].folds0 })
	fmt.Printf("top %d contracts by predicted folds per proof (addrHash, keys, rows, writes/block, depth, cut level, shift, folds before → after):\n", *top)
	for i := 0; i < len(hv) && i < *top; i++ {
		h := hv[i]
		fmt.Printf("  %x keys=%d rows=%d w=%.2f d=%d cut=%d shift=%d folds≈%.0f→%.0f\n", h.dom, h.keys, h.rows, float64(h.rows)/float64(*blocks), h.d, h.cut, h.shift, h.folds0, h.folds)
	}
}
