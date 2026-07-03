// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Concurrent per-window state root: the additive parallel counterpart of
// flushTrieRootSerial. It fans the single CalcTrieRoot into 16 top-account-nibble
// shards (trie.CalcTrieRootShard, cutoff=1), each on its OWN RoTx reading
// committed-DB ⊕ the 4-table StateOverlay (this batch's uncommitted writes), then
// folds the 16 subtrie hashes with trie.CombineNibbleSubtries. The combined root
// AND the collected TrieOf* node updates are byte-identical to the serial loader
// (the shard algorithm is proven by lib/trie's shard tests; the end-to-end
// equivalence is pinned by trie_root_concurrent_test.go). Enabled via
// SetConcurrentRoot; cworkers<=1 keeps the serial path untouched.
package commitment

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
)

// kvPair is one collected TrieOf* node update (k=path key, v=node bytes; v==nil
// is a delete). Package-level so the per-shard reuse buffers on TrieRootComputer
// can hold them across windows.
type kvPair struct{ k, v []byte }

// byteArena is a chunked bump allocator for the per-shard collector's key/value
// copies. Sub-slices returned by alloc/copyOf stay valid as more is allocated:
// the active chunk is only ever resliced within its capacity, and when it fills a
// different chunk becomes active (the full one is left untouched). reset rewinds
// to the first chunk for the next window but RETAINS every allocated slab (a
// chunk's len is zeroed only when it becomes active again), so once the arena has
// grown to a window's working set, steady-state windows allocate nothing.
type byteArena struct {
	chunks [][]byte // retained across resets; ci indexes the active chunk
	ci     int
}

const arenaChunkSize = 1 << 20

func (a *byteArena) newChunk(n int) []byte {
	sz := arenaChunkSize
	if n > sz {
		sz = n
	}
	return make([]byte, 0, sz)
}

func (a *byteArena) reset() {
	a.ci = 0
	if len(a.chunks) > 0 {
		a.chunks[0] = a.chunks[0][:0]
	}
}

// alloc returns a length-n slice whose backing array won't move while other
// arena slices are live (cap == len, so callers can't append into it). NOTE: the
// bytes are NOT zeroed — a reused chunk holds the previous window's data — so the
// caller MUST fully overwrite all n bytes (copyOf and the MarshalTrieNode/ key
// builders do).
func (a *byteArena) alloc(n int) []byte {
	if a.ci == len(a.chunks) { // first use / arena empty
		a.chunks = append(a.chunks, a.newChunk(n))
	}
	cur := a.chunks[a.ci]
	if cap(cur)-len(cur) < n {
		a.ci++
		switch {
		case a.ci == len(a.chunks):
			a.chunks = append(a.chunks, a.newChunk(n))
		case cap(a.chunks[a.ci]) < n: // retained slab too small for this alloc
			a.chunks[a.ci] = a.newChunk(n)
		default:
			a.chunks[a.ci] = a.chunks[a.ci][:0] // reuse retained slab
		}
		cur = a.chunks[a.ci]
	}
	start := len(cur)
	cur = cur[:start+n]
	a.chunks[a.ci] = cur
	return cur[start : start+n : start+n]
}

func (a *byteArena) copyOf(b []byte) []byte {
	s := a.alloc(len(b))
	copy(s, b)
	return s
}

func (t *TrieRootComputer) flushTrieRootConcurrent(rl *trie.RetainList) (types.Hash, error) {
	type shardAccRoot struct {
		nib  []byte // account hashed key, 64 nibbles (arena-backed)
		root types.Hash
	}
	type shardOut struct {
		hash     []byte // depth-1 subtrie hash; nil for an empty nibble
		accUpd   []kvPair
		storUpd  []kvPair
		accRoots []shardAccRoot // per-account storage roots (AccRootEmitter capture)
		err      error
	}
	var outs [16]shardOut
	var wg sync.WaitGroup
	for nib := 0; nib < 16; nib++ {
		wg.Add(1)
		go func(nib int) {
			defer wg.Done()
			// The trie loader panics on some malformed-state paths (e.g. a seek
			// error deep in GenStructStep). Recover so one shard's panic becomes a
			// propagable error instead of taking the whole build process down (and
			// skipping the other 15 shards' RoTx rollbacks).
			defer func() {
				if r := recover(); r != nil {
					outs[nib].err = fmt.Errorf("concurrent root shard %x panic: %v", nib, r)
				}
			}()
			roTx, err := t.cdb.BeginRo(context.Background())
			if err != nil {
				outs[nib].err = err
				return
			}
			defer roTx.Rollback()
			rtx := WrapStateOverlay(roTx, t.coverlay)

			// Reuse this shard's arena + slice backing across windows (index [nib]
			// is owned solely by this goroutine). copyOf/alloc carve stable
			// sub-slices, and MarshalTrieNode writes in place into the arena buf so
			// v is stored directly — no extra per-node copy.
			ar := &t.cArena[nib]
			ar.reset()
			accUpd := t.cAccUpd[nib][:0]
			storUpd := t.cStorUpd[nib][:0]
			accColl := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
				if len(keyHex) == 0 { // depth-0 root has no persisted node (serial skips it too)
					return nil
				}
				k := ar.copyOf(keyHex)
				if hasState == 0 {
					accUpd = append(accUpd, kvPair{k, nil})
					return nil
				}
				buf := ar.alloc(len(hashes) + len(rootHash) + 6)
				v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
				accUpd = append(accUpd, kvPair{k, v})
				return nil
			}
			storColl := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
				if len(accWithInc)+len(keyHex) == 0 {
					return nil
				}
				k := ar.alloc(len(accWithInc) + len(keyHex))
				copy(k, accWithInc)
				copy(k[len(accWithInc):], keyHex)
				if hasState == 0 {
					storUpd = append(storUpd, kvPair{k, nil})
					return nil
				}
				buf := ar.alloc(len(hashes) + len(rootHash) + 6)
				v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
				storUpd = append(storUpd, kvPair{k, v})
				return nil
			}

			loader := trie.NewFlatDBTrieLoader("croot", rl.NibbleSublist(byte(nib)), accColl, storColl, false)
			if t.accRootEmitter != nil {
				// Capture per-account storage roots SHARD-LOCALLY (zero locking);
				// the caller replays them into the real emitter only after the
				// combined root is accepted (fallback paths re-fire it from the
				// serial loader instead, so nothing double-emits).
				loader.SetAccRootEmitter(func(accNib []byte, root types.Hash) {
					outs[nib].accRoots = append(outs[nib].accRoots,
						shardAccRoot{nib: ar.copyOf(accNib), root: root})
				})
			}
			h, err := loader.CalcTrieRootShard(rtx, byte(nib), nil)
			if err != nil {
				outs[nib].err = err
				return
			}
			if h != trie.EmptyRoot {
				hb := make([]byte, 32)
				copy(hb, h[:])
				outs[nib].hash = hb
			}
			// Keep the (possibly grown) slice backing for the next window's reuse.
			t.cAccUpd[nib] = accUpd
			t.cStorUpd[nib] = storUpd
			outs[nib].accUpd = accUpd
			outs[nib].storUpd = storUpd
		}(nib)
	}
	wg.Wait()

	var subs [16][]byte
	nonEmpty := 0
	for nib := 0; nib < 16; nib++ {
		if outs[nib].err != nil {
			return types.Hash{}, fmt.Errorf("concurrent root shard %x: %w", nib, outs[nib].err)
		}
		subs[nib] = outs[nib].hash
		if outs[nib].hash != nil {
			nonEmpty++
		}
	}

	// Top-extension guard: CombineNibbleSubtries assumes a clean 16-branch root.
	// With <2 populated top nibbles the synthesized top would be wrong, so fall
	// back to the serial loader (correct for any shape). The per-window gold-check
	// against the header remains the outer safety net regardless.
	if nonEmpty < 2 {
		return t.flushTrieRootSerial(rl)
	}

	root, err := trie.CombineNibbleSubtries(subs)
	if err != nil {
		return types.Hash{}, err
	}

	// Gold-check the concurrent root against the caller-supplied expected (header)
	// root. A mismatch means the parallel read path (per-worker RoTx ⊕ overlay
	// merged cursors) diverged from what the serial loader sees — a data shape the
	// equivalence tests don't cover. N42_CROOT_DEBUG forces the diagnostic on every
	// window regardless of the expected value.
	mismatch := t.expectRootSet && root != t.expectRoot
	if mismatch || os.Getenv("N42_CROOT_DEBUG") != "" {
		// Read-only serial root on the SAME overlay state (t.tx = committed ⊕
		// overlay). If it differs from the concurrent combine, the shards read the
		// overlay wrong; the per-nibble compare (serial shard on t.tx vs the
		// concurrent shard's hash) localizes which subtree, separating a
		// merged-cursor / shard READ bug from a top-combine bug.
		for nib := 0; nib < 16; nib++ {
			sd := trie.NewFlatDBTrieLoader("croot-dbg", rl.NibbleSublist(byte(nib)), nil, nil, false)
			sh, _ := sd.CalcTrieRootShard(t.tx, byte(nib), nil)
			ch := outs[nib].hash
			same := (ch == nil && sh == trie.EmptyRoot) || (ch != nil && bytes.Equal(ch, sh[:]))
			if !same {
				fmt.Fprintf(os.Stderr, "CROOT nibble %x DIVERGE: concurrent-shard=%x serial-shard-on-tx=%x\n", nib, ch, sh[:8])
			}
		}
	}

	// On a verified mismatch, fall back to the serial loader for this window. The
	// shards have only collected node updates in RAM (outs[*] arena); nothing has
	// been written to TrieOf* yet, so flushTrieRootSerial recomputes the root from
	// the SAME overlay leaf state (HashedAccounts/HashedStorage, written in
	// ComputeRoot Phase 1/2) over the unchanged prior-window TrieOf*, and writes the
	// CORRECT nodes itself. The build self-heals instead of dying, and the operator
	// keeps the concurrent speedup on every window that matches.
	if mismatch {
		fmt.Fprintf(os.Stderr,
			"[croot] concurrent root %x != expected %x — recomputing this window with the serial loader\n",
			root[:8], t.expectRoot[:8])
		rl.Rewind() // reset the lteIndex cursor consumed by the shards
		sroot, serr := t.flushTrieRootSerial(rl)
		if serr != nil {
			return types.Hash{}, serr
		}
		if sroot == t.expectRoot {
			fmt.Fprintf(os.Stderr, "[croot] serial fallback recovered the correct root %x (concurrent shards diverged)\n", sroot[:8])
		} else {
			fmt.Fprintf(os.Stderr,
				"[croot] serial loader ALSO yields %x != expected %x — genuine data/header mismatch, NOT a concurrent-root bug\n",
				sroot[:8], t.expectRoot[:8])
		}
		return sroot, nil
	}

	// Combined root accepted: replay the shards' per-account storage roots into
	// the real emitter (main goroutine — the builder's sink is single-threaded).
	// Shards are nibble-disjoint, so no account appears twice.
	if t.accRootEmitter != nil {
		for nib := 0; nib < 16; nib++ {
			for i := range outs[nib].accRoots {
				t.accRootEmitter(outs[nib].accRoots[i].nib, outs[nib].accRoots[i].root)
			}
		}
	}

	// Phase 4: flush merged node updates to TrieOf*. The shards' key spaces are
	// nibble-disjoint, so the union is a plain concatenation; the writes land on
	// the real RwTx (t.tx) exactly as the serial Phase 4 does.
	for nib := 0; nib < 16; nib++ {
		for _, p := range outs[nib].accUpd {
			if p.v == nil {
				if err := t.tx.Delete(modules.TrieOfAccounts, p.k); err != nil {
					return types.Hash{}, err
				}
			} else if err := t.tx.Put(modules.TrieOfAccounts, p.k, p.v); err != nil {
				return types.Hash{}, err
			}
		}
		for _, p := range outs[nib].storUpd {
			// TrieOfStorage is DupSort: delete-before-put so each path holds exactly
			// its current node version (same discipline as serial Phase 4).
			if err := t.tx.Delete(modules.TrieOfStorage, p.k); err != nil {
				return types.Hash{}, err
			}
			if p.v != nil {
				if err := t.tx.Put(modules.TrieOfStorage, p.k, p.v); err != nil {
					return types.Hash{}, err
				}
			}
		}
	}
	return root, nil
}
