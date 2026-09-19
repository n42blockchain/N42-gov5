// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// derivens.go — derive the exact storage node records (ns) from a finished
// archive's leaf history, without replaying the chain.
//
// The record of a level D-1 node at block b is the hashes of its 16 child
// units (level D subtrees) after b. A unit's keys are one contiguous range of
// the storage leaf history, so its hash after every block that touched it
// follows from that range alone: replay the rows in block order through an
// incremental trie (unittrie.go). Sixteen sibling units replayed together
// give the node's per-block records; nodes are independent of each other, so
// the whole derivation is an embarrassingly parallel scan of `s`.
//
// Every contract is checked against the archive itself: at sampled blocks the
// root assembled from the derived hashes must equal the storage-root history
// (sr), which the build verified against the block headers.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/trie"
)

// deriveContract is one contract to put on the exact ladder.
type deriveContract struct {
	dom   []byte
	depth int
}

// deriveRow is one leaf-history row of the node being replayed.
type deriveRow struct {
	block uint32
	slot  [32]byte
	val   []byte // empty = deleted
}

// deriveSample is a block at which the contract's derived root is compared
// with the storage-root history.
type deriveSample struct {
	block uint64
	want  types.Hash // zero with exists=false: no storage at that block
	has   bool
}

// deriveNodeAt is one level D-1 node as of a sample block.
type deriveNodeAt struct {
	nKids int
	hash  types.Hash // valid when nKids >= 2
}

type deriveStats struct {
	rows, records, fulls, hashes uint64
}

func parseDeriveContracts(path string) ([]deriveContract, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []deriveContract
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		txt := strings.TrimSpace(sc.Text())
		if txt == "" || txt[0] == '#' {
			continue
		}
		p := strings.Fields(txt)
		if len(p) < 2 {
			return nil, fmt.Errorf("%s:%d: want <addrHash> <depth>", path, line)
		}
		dom, err := hex.DecodeString(p[0])
		if err != nil || len(dom) != stoDomainLen {
			return nil, fmt.Errorf("%s:%d: bad addrHash", path, line)
		}
		depth, err := strconv.Atoi(p[1])
		if err != nil || depth < 1 || depth > maxExactDepth {
			return nil, fmt.Errorf("%s:%d: depth must be 1..%d", path, line, maxExactDepth)
		}
		if seen[string(dom)] {
			return nil, fmt.Errorf("%s:%d: contract listed twice", path, line)
		}
		seen[string(dom)] = true
		out = append(out, deriveContract{dom, depth})
	}
	return out, sc.Err()
}

// nodeRows collects the leaf-history rows below (domain, path) — the same
// range, with the same odd-nibble filter, that asOfLeaves folds.
func nodeRows(set *leafSegSet, domain, path []byte) ([]deriveRow, error) {
	prefix := append([]byte{}, domain...)
	for i := 0; i+1 < len(path); i += 2 {
		prefix = append(prefix, path[i]<<4|path[i+1])
	}
	odd := len(path)%2 == 1
	var rows []deriveRow
	c := set.Cursor()
	k, v, err := c.Seek(prefix)
	for ; k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, prefix) {
			break
		}
		if len(k) != stoDomainLen+32+blkLen {
			continue
		}
		if odd {
			nib := k[len(prefix)] >> 4
			if nib < path[len(path)-1] {
				continue
			}
			if nib > path[len(path)-1] {
				break
			}
		}
		r := deriveRow{block: binary.BigEndian.Uint32(k[stoDomainLen+32:]), val: append([]byte(nil), v...)}
		copy(r.slot[:], k[stoDomainLen:])
		rows = append(rows, r)
	}
	return rows, err
}

// deriveNode replays one level D-1 node and emits its per-block records.
// samples (ascending) receive the node's state as of each sample block.
func deriveNode(rows []deriveRow, domain, path []byte, samples []deriveSample,
	emit func(k, v []byte) error, st *deriveStats) ([]deriveNodeAt, error) {

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].block < rows[j].block })
	depth := len(path) + 1 // the units' level
	var (
		tries    [16]unitTrie
		cur      [16]types.Hash
		have     uint16
		diffs    int
		anchored bool
		at       = make([]deriveNodeAt, len(samples))
		si       int
		nodeBuf  = make([]byte, 0, 6+16*32)
		keyBase  = exactRecordKey(domain, path)
	)
	snapshot := func() deriveNodeAt {
		var slots [16]*types.Hash
		n := 0
		for i := range cur {
			if have&(1<<i) != 0 {
				slots[i] = &cur[i]
				n++
			}
		}
		if n < 2 {
			return deriveNodeAt{nKids: n}
		}
		return deriveNodeAt{nKids: n, hash: branch17Hash(slots)}
	}
	for lo := 0; lo < len(rows); {
		blk := rows[lo].block
		for si < len(samples) && samples[si].block < uint64(blk) {
			at[si] = snapshot()
			si++
		}
		var touched uint16
		hi := lo
		for ; hi < len(rows) && rows[hi].block == blk; hi++ {
			r := &rows[hi]
			nibs := nibblesOf(r.slot[:])
			child := nibs[depth-1]
			var item []byte
			if len(r.val) > 0 {
				item = rlpStr(rlpStr(r.val))
			}
			if tries[child].update(nibs[depth:], item) {
				touched |= 1 << child
			}
		}
		st.rows += uint64(hi - lo)
		lo = hi

		var changed uint16
		for i := 0; i < 16; i++ {
			bit := uint16(1) << i
			if touched&bit == 0 {
				continue
			}
			h, ok := tries[i].rootHash()
			switch {
			case !ok && have&bit != 0:
				have &^= bit
				changed |= bit
			case ok && (have&bit == 0 || h != cur[i]):
				have |= bit
				cur[i] = h
				changed |= bit
			}
		}
		if changed == 0 {
			continue
		}
		k := binary.BigEndian.AppendUint32(append([]byte{}, keyBase...), blk)
		var v []byte
		if have == 0 {
			anchored = false // tombstone; the next record starts a new chain
		} else {
			hashes := nodeBuf[:0]
			for i := 0; i < 16; i++ {
				if have&(1<<i) != 0 {
					hashes = append(hashes, cur[i][:]...)
				}
			}
			node := trie.MarshalTrieNode(have, 0, have, hashes, nil, make([]byte, 0, 6+len(hashes)))
			if !anchored || diffs >= fullEvery-1 {
				v = append([]byte{nodeRecFull}, node...)
				anchored, diffs = true, 0
				st.fulls++
			} else {
				v = encodeNodeDiff(node, changed)
				diffs++
			}
		}
		st.records++
		for c := changed & have; c != 0; c &= c - 1 {
			st.hashes++
		}
		if err := emit(k, v); err != nil {
			return nil, err
		}
	}
	for ; si < len(samples); si++ {
		at[si] = snapshot()
	}
	return at, nil
}

// deriveSamples picks up to n blocks at which the contract's storage root is
// known exactly: rows of the per-block storage-root history.
func deriveSamples(sr *leafSegSet, domain []byte, n int, rng *rand.Rand) ([]deriveSample, error) {
	var picked []deriveSample
	seen := 0
	c := sr.Cursor()
	k, v, err := c.Seek(domain)
	var last deriveSample
	for ; k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) != stoDomainLen+blkLen || !bytes.Equal(k[:stoDomainLen], domain) {
			break
		}
		s := deriveSample{block: uint64(binary.BigEndian.Uint32(k[stoDomainLen:]))}
		if len(v) == 32 {
			copy(s.want[:], v)
			s.has = true
		}
		last = s
		seen++
		if len(picked) < n {
			picked = append(picked, s)
		} else if j := rng.Intn(seen); j < n {
			picked[j] = s
		}
	}
	if seen > 0 {
		picked = append(picked, last) // always check the final state
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].block < picked[j].block })
	out := picked[:0]
	for i, s := range picked {
		if i == 0 || s.block != picked[i-1].block {
			out = append(out, s)
		}
	}
	return out, err
}

// deriveRootAt assembles the contract root at one sample from its level D-1
// nodes (index = path as a base-16 number). ok=false when some node on the way
// up has a single child: its hash is an extension or leaf only a fold can
// shape, so the sample cannot be checked from the records alone.
func deriveRootAt(level []deriveNodeAt) (root types.Hash, exists, ok bool) {
	for len(level) > 1 {
		up := make([]deriveNodeAt, len(level)/16)
		for p := range up {
			var slots [16]*types.Hash
			n := 0
			for c := 0; c < 16; c++ {
				ch := &level[p*16+c]
				switch {
				case ch.nKids == 1:
					return root, false, false
				case ch.nKids >= 2:
					slots[c] = &ch.hash
					n++
				}
			}
			up[p] = deriveNodeAt{nKids: n}
			if n >= 2 {
				up[p].hash = branch17Hash(slots)
			}
		}
		level = up
	}
	switch {
	case level[0].nKids == 0:
		return root, false, true
	case level[0].nKids == 1:
		return root, false, false
	}
	return level[0].hash, true, true
}

func runDeriveNS(args []string) {
	fs := flag.NewFlagSet("derive-ns", flag.ExitOnError)
	out := fs.String("out", "", "archive to read (storage leaf history + storage-root history)")
	dst := fs.String("dst", "", "archive dir the ns segments and ns.ladders are written into (default --out; use a reframe --view to leave the source untouched)")
	list := fs.String("contracts", "", "file of '<addrHash> <depth>' lines: the contracts to put on the exact ladder")
	workers := fs.Int("workers", 16, "level D-1 nodes replayed at once (each holds its rows and 16 unit tries in memory)")
	nSamples := fs.Int("check-samples", 64, "blocks per contract at which the derived root is compared with the storage-root history")
	_ = fs.Parse(args)
	if *out == "" || *list == "" {
		die("--out and --contracts required")
	}
	if *dst == "" {
		*dst = *out
	}
	contracts, err := parseDeriveContracts(*list)
	if err != nil {
		die("contracts: %v", err)
	}
	// Every worker reads the same storage segments: load their indexes once
	// and keep them for the run.
	if _, err := preloadSegFiles(*out, 32); err != nil {
		die("preload: %v", err)
	}
	if err := deriveNS(*out, *dst, contracts, *workers, *nSamples); err != nil {
		die("derive-ns: %v", err)
	}
}

// deriveNS derives the ns records of `contracts` from the archive `out` into
// `dst` and lists them in dst's ns.ladders. Nothing becomes readable unless
// every contract's derived roots match the storage-root history.
func deriveNS(out, dst string, contracts []deriveContract, workers, nSamples int) error {
	ladders := map[string][]exactRung{}
	if old, err := loadExactLadders(dst); err != nil {
		return fmt.Errorf("ladders: %v", err)
	} else if old != nil {
		for d, r := range old.m {
			ladders[d] = r
		}
	}
	for _, c := range contracts {
		if _, dup := ladders[string(c.dom)]; dup {
			return fmt.Errorf("contract %x already has ns records in %s", c.dom[:6], dst)
		}
	}
	if err := os.MkdirAll(filepath.Join(dst, leafSegDir), 0o755); err != nil {
		return fmt.Errorf("dst: %v", err)
	}
	spill, err := newLeafSpillWriter(dst)
	if err != nil {
		return fmt.Errorf("spill: %v", err)
	}
	var spillMu sync.Mutex
	emit := func(k, v []byte) error {
		spillMu.Lock()
		defer spillMu.Unlock()
		return spill.add(segTabNodeS, k, v)
	}

	srSet, ok, err := openLeafSegSet(out, segTabStoRoot, newFrameLRUSize(64))
	if err != nil || !ok {
		return fmt.Errorf("open storage-root history: ok=%v err=%v", ok, err)
	}
	type job struct {
		ci   int
		path []byte
		idx  int
	}
	results := make([][][]deriveNodeAt, len(contracts)) // [contract][node][sample]
	samples := make([][]deriveSample, len(contracts))
	rng := rand.New(rand.NewSource(1))
	var jobs []job
	for ci, c := range contracts {
		if samples[ci], err = deriveSamples(srSet, c.dom, nSamples, rng); err != nil {
			return fmt.Errorf("samples of %x: %v", c.dom[:6], err)
		}
		nodes := 1
		for i := 1; i < c.depth; i++ {
			nodes *= 16
		}
		results[ci] = make([][]deriveNodeAt, nodes)
		for idx := 0; idx < nodes; idx++ {
			path := make([]byte, c.depth-1)
			for i, x := len(path)-1, idx; i >= 0; i, x = i-1, x/16 {
				path[i] = byte(x % 16)
			}
			jobs = append(jobs, job{ci, path, idx})
		}
	}
	srSet.Close()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		total    deriveStats
		done     int
		start    = time.Now()
		ch       = make(chan job)
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			set, ok, err := openLeafSegSet(out, segTabLeafS, newFrameLRUSize(32))
			if err != nil || !ok {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("open storage leaf history: ok=%v err=%v", ok, err)
				}
				mu.Unlock()
				for range ch {
				}
				return
			}
			defer set.Close()
			for j := range ch {
				c := contracts[j.ci]
				var st deriveStats
				rows, err := nodeRows(set, c.dom, j.path)
				var at []deriveNodeAt
				if err == nil {
					at, err = deriveNode(rows, c.dom, j.path, samples[j.ci], emit, &st)
				}
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = fmt.Errorf("contract %x node %x: %w", c.dom[:6], j.path, err)
				}
				results[j.ci][j.idx] = at
				total.rows += st.rows
				total.records += st.records
				total.fulls += st.fulls
				total.hashes += st.hashes
				done++
				if done%500 == 0 || done == len(jobs) {
					fmt.Printf("[derive-ns] %d/%d nodes  rows=%d records=%d  %s\n",
						done, len(jobs), total.rows, total.records, time.Since(start).Truncate(time.Second))
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		mu.Lock()
		failed := firstErr != nil
		mu.Unlock()
		if failed {
			break
		}
		ch <- j
	}
	close(ch)
	wg.Wait()
	if cerr := spill.close(); cerr != nil && firstErr == nil {
		firstErr = cerr
	}
	if firstErr != nil {
		return fmt.Errorf("derive-ns: %v (spill left in %s)", firstErr, filepath.Join(dst, leafSpillDir))
	}

	// Check every contract against the storage-root history before anything
	// becomes readable.
	var checked, skipped int
	for ci, c := range contracts {
		for si, s := range samples[ci] {
			level := make([]deriveNodeAt, len(results[ci]))
			for ni := range results[ci] {
				level[ni] = results[ci][ni][si]
			}
			root, exists, ok := deriveRootAt(level)
			if !ok {
				skipped++
				continue
			}
			if exists != s.has || (exists && root != s.want) {
				return fmt.Errorf("contract %x at block %d: derived root %x (exists=%v), storage-root history %x (exists=%v); spill left in %s",
					c.dom[:6], s.block, root[:8], exists, s.want[:8], s.has, filepath.Join(dst, leafSpillDir))
			}
			checked++
		}
	}
	fmt.Printf("[derive-ns] roots checked against sr: %d ok, %d not checkable from records (single-child node)\n", checked, skipped)

	if err := finalizeLeafSegments(dst); err != nil {
		return fmt.Errorf("finalize: %v", err)
	}
	for _, c := range contracts {
		ladders[string(c.dom)] = []exactRung{{from: 0, depth: c.depth}}
	}
	if err := writeExactLadders(dst, ladders); err != nil {
		return fmt.Errorf("ladders: %v", err)
	}
	fmt.Printf("derived %d contracts: rows=%d records=%d (full=%d) hashes=%d in %s\n",
		len(contracts), total.rows, total.records, total.fulls, total.hashes, time.Since(start).Truncate(time.Second))
	return nil
}
