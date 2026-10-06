// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// birthparts.go — birth partitions: folds at early heights.
//
// A fold walks every key under a prefix that the archive EVER saw, and skips
// the ones born after the queried height one by one. For a trie that grew a
// thousandfold, a fold at an early height is sized by the final trie: the
// account proof at block 60,000 cost seconds, a USDT slot proof 40,000 blocks
// after the contract's birth cost minutes, while the state they prove was a
// few thousand keys.
//
// A trie's life is cut into growth stages at the blocks where its number of
// keys ever born crosses a threshold (for a contract: the rungs of its exact
// ladder, so the fold unit shrinks as the trie grows). The keys born in stage
// i, with their history up to the LAST boundary, are copied into partition i.
// A fold at a height in stage g merges partitions 0..g and never sees a later
// key; from the last boundary on, the main history is read as before. The
// copies are small: they end where the trie was still 1/16 of its final size.
package datc

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const accStagesFile = "a.stages"

// loadAccStages reads the account trie's stage boundaries (ascending blocks);
// nil when the archive has no account birth partitions.
func loadAccStages(out string) ([]uint64, error) {
	raw, err := os.ReadFile(filepath.Join(out, leafSegDir, accStagesFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return parseStages(strings.TrimSpace(string(raw)))
}

func parseStages(s string) ([]uint64, error) {
	var out []uint64
	for _, p := range strings.Split(s, ",") {
		v, err := strconv.ParseUint(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("stage boundary %q: %v", p, err)
		}
		if len(out) > 0 && v <= out[len(out)-1] {
			return nil, fmt.Errorf("stage boundaries must ascend")
		}
		out = append(out, v)
	}
	if len(out) == 0 || len(out) > maxBirthParts {
		return nil, fmt.Errorf("need 1..%d stage boundaries", maxBirthParts)
	}
	return out, nil
}

// stageOf is the growth stage a block falls in: the number of boundaries at or
// before it. Build and reader must agree on this function.
func stageOf(bounds []uint64, block uint64) int {
	return sort.Search(len(bounds), func(i int) bool { return bounds[i] > block })
}

// birthPartsAt returns the partitions a fold at height n reads instead of the
// main leaf history, or nil when n is past the trie's last stage boundary (or
// the trie has no partitions).
func (q *querier) birthPartsAt(domain []byte, n uint64) []*leafSegSet {
	var bounds []uint64
	sets := &q.segAP
	if domain != nil {
		if q.exact == nil {
			return nil
		}
		for _, r := range q.exact.m[string(domain)] {
			bounds = append(bounds, r.from)
		}
		sets = &q.segSP
	} else {
		bounds = q.accStages
	}
	if len(bounds) == 0 || len(bounds) > maxBirthParts {
		return nil
	}
	g := stageOf(bounds, n)
	if g == len(bounds) {
		return nil
	}
	var parts []*leafSegSet
	for i := 0; i <= g; i++ {
		if sets[i] != nil {
			parts = append(parts, sets[i])
		}
	}
	if parts == nil {
		// Boundaries without any partition segment: an archive whose rungs
		// were written without partitions. The main history is always right.
		return nil
	}
	return parts
}

// wholeFoldWorkers is how many goroutines share the fold of a whole storage
// trie. A contract below the record threshold has no records at all, and its
// fold is the one cost of its proofs; the 16 first-nibble subtrees are
// independent scans, so splitting them is pure wall-clock gain.
const wholeFoldWorkers = 4

// asOfLeavesWhole is asOfLeaves(domain, nil, n) over the main history, its 16
// first-nibble ranges scanned concurrently.
func (q *querier) asOfLeavesWhole(domain []byte, n uint64) ([]foldLeaf, error) {
	var (
		wg     sync.WaitGroup
		parts  [16][]foldLeaf
		errs   [16]error
		reads  [16]int
		nibble = make(chan byte, 16)
	)
	for nib := byte(0); nib < 16; nib++ {
		nibble <- nib
	}
	close(nibble)
	for w := 0; w < wholeFoldWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			set := q.segS.clone(16)
			defer set.Close()
			sub := &querier{} // storage leaves need nothing of the querier but its read counter
			for nib := range nibble {
				before := sub.leafReads
				parts[nib], errs[nib] = sub.asOfLeavesFrom(set.Cursor(), false, domain, []byte{nib}, n)
				reads[nib] = sub.leafReads - before
			}
		}()
	}
	wg.Wait()
	var out []foldLeaf
	for nib := range parts {
		if errs[nib] != nil {
			return nil, errs[nib]
		}
		q.leafReads += reads[nib]
		for _, lf := range parts[nib] {
			// Remainders come back relative to the one-nibble path.
			lf.remainder = append([]byte{byte(nib)}, lf.remainder...)
			out = append(out, lf)
		}
	}
	return out, nil
}

// mergeFoldLeaves merges two key-sorted, disjoint leaf lists.
func mergeFoldLeaves(a, b []foldLeaf) []foldLeaf {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make([]foldLeaf, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if bytes.Compare(a[i].remainder, b[j].remainder) <= 0 {
			out = append(out, a[i])
			i++
		} else {
			out = append(out, b[j])
			j++
		}
	}
	out = append(out, a[i:]...)
	return append(out, b[j:]...)
}

// partitionRows copies one key's rows into its birth partition: rows holds
// the key's complete history, ascending by block. Keys born in the last stage
// (or later) stay in the main history only.
func partitionRows(bounds []uint64, baseTable int, rows []kvPair, blockOf func(k []byte) uint64, emit func(table int, k, v []byte) error) error {
	if len(rows) == 0 {
		return nil
	}
	stage := stageOf(bounds, blockOf(rows[0].k))
	if stage >= len(bounds) {
		return nil
	}
	last := bounds[len(bounds)-1]
	for _, r := range rows {
		if blockOf(r.k) >= last {
			break
		}
		if err := emit(baseTable+stage, r.k, r.v); err != nil {
			return err
		}
	}
	return nil
}

// runDeriveAccParts writes the account trie's birth partitions.
func runDeriveAccParts(args []string) {
	fs := flag.NewFlagSet("derive-acc-parts", flag.ExitOnError)
	out := fs.String("out", "", "archive to read (account leaf history)")
	dst := fs.String("dst", "", "archive dir the a0..a2 segments and a.stages are written into (default --out)")
	stages := fs.String("stages", "1200000,2400000,4400000", "stage boundaries (blocks): where the account trie's keys-ever-born crossed ~131k, ~2M and ~33M on mainnet")
	workers := fs.Int("workers", 24, "buckets scanned at once")
	_ = fs.Parse(args)
	if *out == "" {
		die("--out required")
	}
	if *dst == "" {
		*dst = *out
	}
	bounds, err := parseStages(*stages)
	if err != nil {
		die("--stages: %v", err)
	}
	if err := deriveAccParts(*out, *dst, bounds, *workers); err != nil {
		die("derive-acc-parts: %v", err)
	}
}

func deriveAccParts(out, dst string, bounds []uint64, workers int) error {
	if old, err := loadAccStages(dst); err != nil {
		return err
	} else if old != nil {
		return fmt.Errorf("%s already has account birth partitions", dst)
	}
	names, err := filepath.Glob(filepath.Join(out, leafSegDir, "a.*.seg"))
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("no account leaf segments under %s", out)
	}
	if err := os.MkdirAll(filepath.Join(dst, leafSegDir), 0o755); err != nil {
		return err
	}
	blockOf := func(k []byte) uint64 {
		return uint64(k[32])<<24 | uint64(k[33])<<16 | uint64(k[34])<<8 | uint64(k[35])
	}
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		copied   uint64
		start    = time.Now()
		ch       = make(chan string)
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// One spill writer per worker: a source bucket maps to the same
			// bucket of each partition, so no two workers share a spill file.
			spill, err := newLeafSpillWriter(dst)
			if err != nil {
				fail(err)
				for range ch {
				}
				return
			}
			var n uint64
			emit := func(table int, k, v []byte) error { n++; return spill.add(table, k, v) }
			for name := range ch {
				f, err := os.Open(name)
				if err != nil {
					fail(err)
					continue
				}
				sf, err := loadLeafSegFile(f)
				if err != nil {
					f.Close()
					fail(fmt.Errorf("%s: %w", name, err))
					continue
				}
				it := &oldSegIter{sf: sf, zr: zr2()}
				var rows []kvPair
				for {
					ok, err := it.ensure()
					if err != nil {
						fail(err)
						break
					}
					var k []byte
					if ok {
						k = it.key()
						if len(k) != 32+blkLen {
							it.next()
							continue
						}
					}
					if !ok || (len(rows) > 0 && !bytes.Equal(rows[0].k[:32], k[:32])) {
						if err := partitionRows(bounds, segTabLeafA0, rows, blockOf, emit); err != nil {
							fail(err)
						}
						rows = rows[:0]
					}
					if !ok {
						break
					}
					// Only the history before the last boundary is ever read from
					// a partition; do not hold a hot key's later versions.
					if len(rows) == 0 || blockOf(k) < bounds[len(bounds)-1] {
						rec := it.rec()
						kl, m := binary.Uvarint(rec)
						vOff := m + int(kl)
						vl, m2 := binary.Uvarint(rec[vOff:])
						rows = append(rows, kvPair{k: append([]byte(nil), k...), v: append([]byte(nil), rec[vOff+m2:vOff+m2+int(vl)]...)})
					}
					it.next()
				}
				it.close()
			}
			if err := spill.close(); err != nil {
				fail(err)
			}
			mu.Lock()
			copied += n
			mu.Unlock()
		}()
	}
	for _, n := range names {
		ch <- n
	}
	close(ch)
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if err := finalizeLeafSegments(dst); err != nil {
		return err
	}
	var parts []string
	for _, b := range bounds {
		parts = append(parts, strconv.FormatUint(b, 10))
	}
	if err := os.WriteFile(filepath.Join(dst, leafSegDir, accStagesFile), []byte(strings.Join(parts, ",")+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("account birth partitions: %d rows copied, boundaries %v, %s\n", copied, bounds, time.Since(start).Truncate(time.Second))
	return nil
}
