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

func (t *TrieRootComputer) flushTrieRootConcurrent(rl *trie.RetainList) (types.Hash, error) {
	type kvPair struct{ k, v []byte }
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
			roTx, err := t.cdb.BeginRo(context.Background())
			if err != nil {
				outs[nib].err = err
				return
			}
			defer roTx.Rollback()
			rtx := WrapStateOverlay(roTx, t.coverlay)

			var accUpd, storUpd []kvPair
			accColl := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
				if len(keyHex) == 0 { // depth-0 root has no persisted node (serial skips it too)
					return nil
				}
				k := append([]byte{}, keyHex...)
				if hasState == 0 {
					accUpd = append(accUpd, kvPair{k, nil})
					return nil
				}
				buf := make([]byte, len(hashes)+len(rootHash)+6)
				v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
				accUpd = append(accUpd, kvPair{k, append([]byte{}, v...)})
				return nil
			}
			storColl := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
				k := append(append([]byte{}, accWithInc...), keyHex...)
				if len(k) == 0 {
					return nil
				}
				if hasState == 0 {
					storUpd = append(storUpd, kvPair{k, nil})
					return nil
				}
				buf := make([]byte, len(hashes)+len(rootHash)+6)
				v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
				storUpd = append(storUpd, kvPair{k, append([]byte{}, v...)})
				return nil
			}

			// Clone (full list), NOT NibbleSublist: RetainWithMarker's nextCreated
			// (next marked key, possibly in a higher nibble) must match what the
			// serial loader sees for byte-identical insert handling at nibble edges.
			loader := trie.NewFlatDBTrieLoader("croot", rl.Clone(), accColl, storColl, false)
			if t.storageRootHook != nil {
				loader.SetStorageRootHook(t.storageRootHook)
			}
			if t.denseNodeHook != nil {
				loader.SetDenseNodeHook(t.denseNodeHook)
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
		t.resetStorageRootHook()
		return t.flushTrieRootSerial(rl)
	}

	root, err := trie.CombineNibbleSubtries(subs)
	if err != nil {
		return types.Hash{}, err
	}
	// The shards never see the top branch; report it to the dense-node hook
	// from the combined subtree hashes (a serial recompute below re-reports).
	if t.denseNodeHook != nil {
		var mask uint16
		slots := make([]byte, 0, 16*33)
		for nib := 0; nib < 16; nib++ {
			if subs[nib] == nil {
				continue
			}
			mask |= 1 << nib
			slots = append(slots, 0xa0)
			slots = append(slots, subs[nib]...)
		}
		t.denseNodeHook(nil, []byte{}, mask, mask, slots)
	}

	// Gold-check the concurrent root against the caller-supplied expected (header)
	// root. A mismatch means the parallel read path (per-worker RoTx ⊕ overlay
	// merged cursors) diverged from what the serial loader sees — a data shape the
	// equivalence tests don't cover. N42_CROOT_DEBUG forces the same diagnostic on
	// every window regardless of the expected value.
	mismatch := t.expectRootSet && root != t.expectRoot
	if mismatch || os.Getenv("N42_CROOT_DEBUG") != "" {
		// Read-only serial root on the SAME overlay state (t.tx = committed ⊕
		// overlay). If it differs from the concurrent combine, the shards read the
		// overlay wrong; the per-nibble compare localizes which subtree, isolating
		// the merged-cursor / shard bug from a top-combine bug.
		sl := trie.NewFlatDBTrieLoader("croot-dbg", rl.Clone(), nil, nil, false)
		if sroot, serr := sl.CalcTrieRoot(t.tx, nil); serr == nil && sroot != root {
			fmt.Fprintf(os.Stderr, "CROOT root diverge: concurrent=%x serial-on-tx=%x\n", root[:8], sroot[:8])
			for nib := 0; nib < 16; nib++ {
				sd := trie.NewFlatDBTrieLoader("dbg", rl.Clone(), nil, nil, false)
				sh, _ := sd.CalcTrieRootShard(t.tx, byte(nib), nil)
				ch := outs[nib].hash
				same := (ch == nil && sh == trie.EmptyRoot) || (ch != nil && bytes.Equal(ch, sh[:]))
				if !same {
					fmt.Fprintf(os.Stderr, "  nibble %x DIVERGE: concurrent-shard=%x serial-shard=%x\n", nib, ch, sh[:8])
				}
			}
		}
	}

	// On a verified mismatch, fall back to the serial loader for this window. The
	// shards have only collected node updates in RAM (outs[*]); nothing has been
	// written to TrieOf* yet, so flushTrieRootSerial recomputes the root from the
	// SAME overlay leaf state (HashedAccounts/HashedStorage, written in Phase 1/2)
	// over the unchanged prior-window TrieOf*, and writes the CORRECT nodes itself.
	// The build self-heals instead of dying, and the operator keeps the speedup on
	// every window that matches.
	if mismatch {
		fmt.Fprintf(os.Stderr,
			"[croot] concurrent root %x != expected %x — recomputing this window with the serial loader\n",
			root[:8], t.expectRoot[:8])
		t.resetStorageRootHook()
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

// resetStorageRootHook tells the hook consumer to discard roots delivered by
// the (about to be superseded) shard pass.
func (t *TrieRootComputer) resetStorageRootHook() {
	if t.storageRootHook != nil {
		t.storageRootHook(nil, nil)
	}
	if t.denseNodeHook != nil {
		t.denseNodeHook(nil, nil, 0, 0, nil)
	}
}
