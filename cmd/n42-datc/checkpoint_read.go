// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Checkpoint reader + checkpoint-accelerated subtree fold. A checkpoint file
// (<out>/ckpt/<table>.<block>.ckpt) holds the key-sorted live-key-set at that
// block. asOfLeavesCkpt bounds the fold to the keys that EXIST at N: it unions
// the keys under the fold prefix from the two checkpoints bracketing N (the
// key was either live at the lower checkpoint, or created before the upper
// one) and reads each key's floor value ≤ N — deletions/absent floor to empty
// and drop out. For early N the state (and thus the checkpoint slice under a
// prefix) is tiny, so this replaces the ~100M-key future-scan with a few-K read.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// asOfLeavesCkpt folds the subtree under (domain, path) at N using the live-key
// checkpoints instead of scanning the whole prefix. Candidate keys = the keys
// under the fold prefix from the checkpoints bracketing N (below ≤ N < above);
// every key live at N is either live at `below` or was created before `above`,
// so the union covers them. Each candidate's floor value ≤ N is read (empty ⇒
// deleted/absent, dropped). Returns (leaves, true) when checkpoints can answer;
// (nil, false) when they cannot (no checkpoint ≤ N, or N past the last one) so
// the caller falls back to the scan.
func (q *querier) asOfLeavesCkpt(domain, path []byte, n uint64) ([]foldLeaf, bool, error) {
	tab := segTabLeafA
	if domain != nil {
		tab = segTabLeafS
	}
	if q.ckpt == nil || !q.ckpt.available(tab) || n > q.ckpt.maxBlock(tab) {
		return nil, false, nil // N past the last checkpoint: fall back to scan
	}
	// Smallest checkpoint ≥ N: its ever-appeared key-set is a COMPLETE candidate
	// set for "keys existing at N" (every such key was created ≤ N ≤ above).
	_, above, _, haveAbove := q.ckpt.bracket(tab, n)
	if !haveAbove {
		if n == q.ckpt.maxBlock(tab) {
			above, haveAbove = n, true // N exactly at the last checkpoint
		} else {
			return nil, false, nil
		}
	}
	s, err := q.ckpt.load(tab, above)
	if err != nil {
		return nil, false, err
	}

	fullNibbles := path
	bytePrefix := make([]byte, 0, len(domain)+len(fullNibbles)/2)
	bytePrefix = append(bytePrefix, domain...)
	for i := 0; i+1 < len(fullNibbles); i += 2 {
		bytePrefix = append(bytePrefix, fullNibbles[i]<<4|fullNibbles[i+1])
	}
	oddNib := len(fullNibbles)%2 == 1
	var oddVal byte
	if oddNib {
		oddVal = fullNibbles[len(fullNibbles)-1]
	}

	lo, hi := s.rangeUnder(bytePrefix)
	var out []foldLeaf
	for i := lo; i < hi; i++ {
		key := s.key(i)
		if oddNib && key[len(bytePrefix)]>>4 != oddVal {
			continue // wrong odd-nibble branch
		}
		val, _, err := q.leafFloor(domain != nil, key, n)
		if err != nil {
			return nil, false, err
		}
		q.leafReads++
		q.distinctKeys++
		if err := q.emitFoldLeaf(domain, fullNibbles, key, val, n, &out); err != nil {
			return nil, false, err
		}
	}
	return out, true, nil
}

// ckptSet is one loaded checkpoint: keyLen-strided, sorted keys.
type ckptSet struct {
	block  uint64
	keyLen int
	keys   []byte // concatenated fixed-length keys, sorted
	n      int
}

func (s *ckptSet) key(i int) []byte { return s.keys[i*s.keyLen : (i+1)*s.keyLen] }

// rangeUnder returns [lo,hi) index range of keys with the byte prefix.
func (s *ckptSet) rangeUnder(prefix []byte) (int, int) {
	lo := sort.Search(s.n, func(i int) bool { return bytes.Compare(s.key(i), prefix) >= 0 })
	hi := lo
	for hi < s.n && bytes.HasPrefix(s.key(hi), prefix) {
		hi++
	}
	return lo, hi
}

// ckptStore lazily loads and caches checkpoint sets per (table, block).
type ckptStore struct {
	dir     string
	blocks  [2][]uint64          // [table] sorted checkpoint blocks present
	loaded  map[uint64]*ckptSet  // key: table<<40 | block
	keyLens [2]int
}

func openCkptStore(outDir string) *ckptStore {
	st := &ckptStore{dir: filepath.Join(outDir, ckptDir), loaded: map[uint64]*ckptSet{}}
	st.keyLens = [2]int{32, 72}
	for tab := 0; tab < 2; tab++ {
		matches, _ := filepath.Glob(filepath.Join(st.dir, strconv.Itoa(tab)+".*.ckpt"))
		for _, m := range matches {
			// base = "<tab>.<block>.ckpt"
			parts := strings.Split(strings.TrimSuffix(filepath.Base(m), ".ckpt"), ".")
			if len(parts) != 2 || parts[0] != strconv.Itoa(tab) {
				continue
			}
			b, err := strconv.ParseUint(parts[1], 10, 64)
			if err != nil {
				continue
			}
			st.blocks[tab] = append(st.blocks[tab], b)
		}
		sort.Slice(st.blocks[tab], func(i, j int) bool { return st.blocks[tab][i] < st.blocks[tab][j] })
	}
	return st
}

// available reports whether any checkpoints exist for the table.
func (st *ckptStore) available(tab int) bool { return len(st.blocks[tab]) > 0 }

// maxBlock is the highest checkpoint block for the table (0 if none).
func (st *ckptStore) maxBlock(tab int) uint64 {
	bs := st.blocks[tab]
	if len(bs) == 0 {
		return 0
	}
	return bs[len(bs)-1]
}

// bracket returns the checkpoint blocks below (≤n) and above (>n) n. below=^0
// when none is ≤ n.
func (st *ckptStore) bracket(tab int, n uint64) (below, above uint64, haveBelow, haveAbove bool) {
	below = ^uint64(0)
	for _, b := range st.blocks[tab] {
		if b <= n {
			below, haveBelow = b, true
		} else {
			above, haveAbove = b, true
			break
		}
	}
	return
}

func (st *ckptStore) load(tab int, block uint64) (*ckptSet, error) {
	key := uint64(tab)<<40 | block
	if s, ok := st.loaded[key]; ok {
		return s, nil
	}
	path := filepath.Join(st.dir, strconv.Itoa(tab)+"."+strconv.FormatUint(block, 10)+".ckpt")
	comp, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	raw, err := zr.DecodeAll(comp, nil)
	zr.Close()
	if err != nil {
		return nil, err
	}
	kl := st.keyLens[tab]
	s := &ckptSet{block: block, keyLen: kl, keys: raw, n: len(raw) / kl}
	st.loaded[key] = s
	return s, nil
}
