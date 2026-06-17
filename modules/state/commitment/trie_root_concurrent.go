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
	"context"
	"fmt"
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
// the current chunk is only ever resliced within its capacity, and when it fills
// a fresh chunk is started (the old one is retained by chunks). reset rewinds for
// the next window while keeping the current chunk's backing array for reuse, so a
// steady-state window allocates nothing.
type byteArena struct {
	chunks [][]byte
	cur    []byte
}

const arenaChunkSize = 1 << 20

func (a *byteArena) reset() {
	a.chunks = a.chunks[:0]
	if a.cur != nil {
		a.cur = a.cur[:0]
	}
}

// alloc returns a zeroed slice of length n whose backing array won't move while
// other arena slices are live (cap == len, so callers can't append into it).
func (a *byteArena) alloc(n int) []byte {
	if cap(a.cur)-len(a.cur) < n {
		sz := arenaChunkSize
		if n > sz {
			sz = n
		}
		if len(a.cur) > 0 {
			a.chunks = append(a.chunks, a.cur)
		}
		a.cur = make([]byte, 0, sz)
	}
	start := len(a.cur)
	a.cur = a.cur[:start+n]
	return a.cur[start : start+n : start+n]
}

func (a *byteArena) copyOf(b []byte) []byte {
	s := a.alloc(len(b))
	copy(s, b)
	return s
}

func (t *TrieRootComputer) flushTrieRootConcurrent(rl *trie.RetainList) (types.Hash, error) {
	type shardOut struct {
		hash    []byte // depth-1 subtrie hash; nil for an empty nibble
		accUpd  []kvPair
		storUpd []kvPair
		err     error
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
