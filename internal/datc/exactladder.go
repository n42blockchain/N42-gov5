// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// exactladder.go — the exact-only storage ladder (reader side).
//
// A proof needs, for every node on its path, the 16 child hashes at height N.
// An epoch record only helps where nothing changed inside the window; where
// the trie is dense every child changed and the reader recurses into all of
// them, and where it is sparse an epoch record is written per change anyway.
// So this ladder has no epoch levels: a listed contract of depth D records
// ONE level, D-1, per block (derive-ns writes it), and
//
//	level D-1   the floor record IS the node at N: no window, no recursion
//	levels < D-1 are assembled from their 16 children (16^k records)
//	level D     is folded from the leaf history — once per proof
//
// A contract that is not listed has depth 0: its trie is folded whole, which
// is what a contract that small costs anyway. The list lives next to the
// segments (leafseg/ns.ladders) rather than in MDBX, so an archive view can
// carry it without writing to the archive it was derived from.
package datc

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/n42blockchain/N42/common/types"
)

const (
	exactLaddersFile = "ns.ladders"
	exactLaddersHead = "# datc exact storage ladders v1: <addrHash> <fromBlock> <depth>"
	// maxExactDepth: with one recorded level the levels above it cost 16^k
	// record reads, so depth 3 (273 reads) is as deep as one level carries.
	maxExactDepth = 3
)

// exactRung is the depth in force from one block on.
type exactRung struct {
	from  uint64
	depth int
}

// exactLadders maps a contract (addrHash) to its rungs, ascending by block.
type exactLadders struct {
	m map[string][]exactRung
}

func (l *exactLadders) depthAt(domain []byte, n uint64) int {
	d := 0
	for _, r := range l.m[string(domain)] {
		if r.from > n {
			break
		}
		d = r.depth
	}
	return d
}

var exactLaddersCache = struct {
	mu sync.Mutex
	m  map[string]*exactLadders // path|size|mtime → parsed (immutable)
}{m: make(map[string]*exactLadders)}

// loadExactLadders reads <out>/leafseg/ns.ladders; nil when the archive has
// none (it then reads storage through the epoch records, as before).
func loadExactLadders(out string) (*exactLadders, error) {
	path := filepath.Join(out, leafSegDir, exactLaddersFile)
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	key := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	exactLaddersCache.mu.Lock()
	defer exactLaddersCache.mu.Unlock()
	if l := exactLaddersCache.m[key]; l != nil {
		return l, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	l := &exactLadders{m: make(map[string][]exactRung)}
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		txt := strings.TrimSpace(sc.Text())
		if txt == "" || txt[0] == '#' {
			continue
		}
		p := strings.Fields(txt)
		if len(p) != 3 {
			return nil, fmt.Errorf("%s:%d: want <addrHash> <fromBlock> <depth>", path, line)
		}
		dom, err := hex.DecodeString(p[0])
		if err != nil || len(dom) != stoDomainLen {
			return nil, fmt.Errorf("%s:%d: bad addrHash", path, line)
		}
		from, err1 := strconv.ParseUint(p[1], 10, 64)
		depth, err2 := strconv.Atoi(p[2])
		if err1 != nil || err2 != nil || depth < 0 || depth > maxExactDepth {
			return nil, fmt.Errorf("%s:%d: bad fromBlock/depth", path, line)
		}
		l.m[string(dom)] = append(l.m[string(dom)], exactRung{from, depth})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	for _, r := range l.m {
		// Rungs at the same block (a contract that outgrew two depths within
		// one bin): the deeper one must win, so order by depth too.
		sort.Slice(r, func(i, j int) bool {
			if r[i].from != r[j].from {
				return r[i].from < r[j].from
			}
			return r[i].depth < r[j].depth
		})
	}
	exactLaddersCache.m[key] = l
	return l, nil
}

// writeExactLadders writes the list atomically, sorted by contract.
func writeExactLadders(out string, m map[string][]exactRung) error {
	doms := make([]string, 0, len(m))
	for d := range m {
		doms = append(doms, d)
	}
	sort.Strings(doms)
	path := filepath.Join(out, leafSegDir, exactLaddersFile)
	f, err := os.Create(path + ".tmp")
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	fmt.Fprintln(w, exactLaddersHead)
	for _, d := range doms {
		for _, r := range m[d] {
			fmt.Fprintf(w, "%x %d %d\n", d, r.from, r.depth)
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// exactRecordKey is the ns record key without its block suffix:
// pathLen(1) | addrHash | path nibbles, the layout of every node record.
func exactRecordKey(domain, path []byte) []byte {
	k := make([]byte, 0, 1+len(domain)+len(path)+4)
	k = append(k, byte(len(domain)+len(path)))
	k = append(k, domain...)
	return append(k, path...)
}

// exactMemo remembers the node hashes of one (contract, height): the levels
// above the recorded one are assembled from the same records at every step
// down the proof path.
type exactMemo struct {
	domain string
	n      uint64
	m      map[string]exactMemoEntry
}

type exactMemoEntry struct {
	slots  [16]*types.Hash
	nKids  int
	usable bool
}

// exactSlotsAt is branchSlotsAt on the exact-only ladder.
func (q *querier) exactSlotsAt(domain, path []byte, n uint64) (slots [16]*types.Hash, nKids int, usable bool, err error) {
	d := len(path)
	depth := q.exact.depthAt(domain, n)
	if d >= depth {
		q.noteFold(d, "belowFold")
		return slots, 0, false, nil
	}
	if q.exactMemo.domain != string(domain) || q.exactMemo.n != n || q.exactMemo.m == nil {
		q.exactMemo = exactMemo{domain: string(domain), n: n, m: make(map[string]exactMemoEntry)}
	}
	if e, ok := q.exactMemo.m[string(path)]; ok {
		return e.slots, e.nKids, e.usable, nil
	}
	defer func() {
		if err == nil {
			q.exactMemo.m[string(path)] = exactMemoEntry{slots, nKids, usable}
		}
	}()

	if d == depth-1 {
		if q.segNS == nil {
			return slots, 0, false, fmt.Errorf("contract %x is on the exact ladder but the archive has no ns segments", domain[:6])
		}
		st, _, ok, ferr := q.floorRecordFrom(q.segNS.Cursor(), exactRecordKey(domain, path), n+1)
		if ferr != nil {
			return slots, 0, false, ferr
		}
		if !ok {
			switch q.lastFloorReason {
			case "absent", "tombstone":
				return slots, 0, true, nil // no key below this node at N
			}
			return slots, 0, false, fmt.Errorf("exact record of %x path %x at %d: %s", domain[:6], path, n, q.lastFloorReason)
		}
		q.recs++
		for nib := 0; nib < 16; nib++ {
			if st.hasState&(1<<nib) != 0 {
				h := types.Hash(st.hash[nib])
				slots[nib] = &h
				nKids++
			}
		}
	} else {
		for nib := byte(0); nib < 16; nib++ {
			h, exists, herr := q.nodeHashAt(domain, append(append(make([]byte, 0, d+1), path...), nib), n)
			if herr != nil {
				return slots, 0, false, herr
			}
			if exists {
				hc := h
				slots[nib] = &hc
				nKids++
			}
		}
	}
	if nKids == 1 {
		// One child: the node here is a leaf or an extension, which only the
		// fold can shape — and a node with one non-empty child is tiny.
		q.noteFold(d, "collapsed")
		return slots, nKids, false, nil
	}
	return slots, nKids, true, nil
}

// accExactLadder enables the exact-only reading of the account trie for
// archives whose deepest account level is recorded per block (tests switch it
// off to compare with the epoch-record path).
var accExactLadder = true

// accExactSlotsAt is branchSlotsAt for the account trie when its deepest
// recorded level is per block. The epoch levels above it answer a dense trie
// no better than assembling them from their children — every child changed
// inside the window, so the reader recursed into all 16 anyway — and reading
// them needs the change index; this path needs neither.
//
//	root (accRoot = 1)   its per-block record, else assembled from level 1
//	levels in between    assembled from their 16 children
//	level accFold-1      its floor record IS the node at n
//	level accFold        folded
//
// Unlike a derived ns record, a builder record exists only while the node is
// a branch whose children are all hashed: absent, tombstone and MIXED all
// mean "whatever sits here, only the fold knows its shape".
func (q *querier) accExactSlotsAt(path []byte, n uint64) (slots [16]*types.Hash, nKids int, usable bool, err error) {
	d := len(path)
	if d >= q.accFold {
		q.noteFold(d, "belowFold")
		return slots, 0, false, nil
	}
	if q.accExactMemo.n != n || q.accExactMemo.m == nil {
		q.accExactMemo = exactMemo{n: n, m: make(map[string]exactMemoEntry)}
	}
	if e, ok := q.accExactMemo.m[string(path)]; ok {
		return e.slots, e.nKids, e.usable, nil
	}
	defer func() {
		if err == nil {
			q.accExactMemo.m[string(path)] = exactMemoEntry{slots, nKids, usable}
		}
	}()

	recorded := d == q.accFold-1 || (d == 0 && q.sched.accRoot == 1)
	if recorded {
		st, _, ok, ferr := q.floorRecord(nil, path, n)
		if ferr != nil {
			return slots, 0, false, ferr
		}
		if ok && st.hasState == st.hasHash && st.hasState != 0 {
			q.recs++
			for nib := 0; nib < 16; nib++ {
				if st.hasState&(1<<nib) != 0 {
					h := types.Hash(st.hash[nib])
					slots[nib] = &h
					nKids++
				}
			}
			return slots, nKids, nKids >= 2, nil
		}
		if d == q.accFold-1 {
			reason := "mixedRecord"
			if !ok {
				reason = "floor:" + q.lastFloorReason
				if q.absentPaths != nil && q.lastFloorReason == "absent" {
					q.absentPaths[string(path)]++
				}
			}
			q.noteFold(d, reason)
			return slots, 0, false, nil
		}
		// An unusable root record: assemble the root like any other level.
	}
	for nib := byte(0); nib < 16; nib++ {
		h, exists, herr := q.nodeHashAt(nil, append(append(make([]byte, 0, d+1), path...), nib), n)
		if herr != nil {
			return slots, 0, false, herr
		}
		if exists {
			hc := h
			slots[nib] = &hc
			nKids++
		}
	}
	if nKids == 1 {
		q.noteFold(d, "collapsed")
		return slots, nKids, false, nil
	}
	return slots, nKids, true, nil
}

// exactBlockOf is the block suffix of an ns record key.
func exactBlockOf(k []byte) uint64 { return uint64(binary.BigEndian.Uint32(k[len(k)-4:])) }
