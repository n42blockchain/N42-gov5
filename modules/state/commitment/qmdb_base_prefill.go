// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The base-value prefill of a pipelined execution (P15).
//
// The lag executor runs child N+1 ahead on parent N's post-state, layered
// over the live tree (internal/lag_pipeline_exec.go). The child's reads of
// the tree take its reader lock per lookup, and N's ComputeRoot holds that
// lock exclusively for its whole ApplyOps: every base read the child makes
// while N is rooted waits for the root (round S110: the block-start system
// call's sysStartMs, median 22 ms, was that wait).
//
// A base value read from the tree in state N-1 or in state N is the same
// for every key N did not change: N's ComputeRoot applies exactly the ops of
// its dirty set, and N's post-state holds that same dirty set (its accounts
// and storage maps ARE the maps the root is handed), so the child's
// post-state layer answers every key N changed -- written, deleted, wiped --
// before the base is consulted. A BasePrefill holds tree values read ahead,
// while the tree is in either state, and a LookupSource with it attached
// answers them without the lock. Which state the tree holds is
// AppliedState: a ComputeRoot labelled for its block (LabelNextRoot, bound
// to the post-state of the dirty set it applies), and a mutation count that
// every other change of the tree's values advances, so a prefill can tell
// that nothing but its parent's own root ran while it read.

package commitment

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state"
)

// QMDBAppliedState is what a QMDBRootComputer's tree holds: Gen counts the
// mutations of its values; Labelled says the last one was the ComputeRoot
// of block Number (Hash, on ParentHash), so the tree is that block's
// post-state as this node computed it.
type QMDBAppliedState struct {
	Gen              uint64
	Labelled         bool
	Number           uint64
	Hash, ParentHash types.Hash
}

type rootLabel struct {
	post             *state.PostState
	number           uint64
	hash, parentHash types.Hash
}

// LabelNextRoot labels the next ComputeRoot as block number (hash, on
// parentHash) when that ComputeRoot applies post's dirty set -- the same
// account map, not an equal one; any other ComputeRoot drops the label
// unused. The importer calls it from the root's post-state hook, right
// before the root runs.
func (r *QMDBRootComputer) LabelNextRoot(post *state.PostState, number uint64, hash, parentHash types.Hash) {
	if post == nil {
		return
	}
	r.nextLabel.Store(&rootLabel{post: post, number: number, hash: hash, parentHash: parentHash})
}

// AppliedState returns what the tree holds now, without the reader lock.
func (r *QMDBRootComputer) AppliedState() QMDBAppliedState {
	if s := r.applied.Load(); s != nil {
		return *s
	}
	return QMDBAppliedState{}
}

// noteMutationLocked records a change of the tree's values (readers held
// exclusively): the ComputeRoot of label's block, or (nil) anything else.
func (r *QMDBRootComputer) noteMutationLocked(label *rootLabel) {
	var gen uint64
	if prev := r.applied.Load(); prev != nil {
		gen = prev.Gen
	}
	s := &QMDBAppliedState{Gen: gen + 1}
	if label != nil {
		s.Labelled, s.Number, s.Hash, s.ParentHash = true, label.number, label.hash, label.parentHash
	}
	r.applied.Store(s)
}

// prefillValue is one key's tree value; found=false records an absent key.
type prefillValue struct {
	v     []byte
	found bool
}

// BasePrefill is an immutable set of tree values read ahead, sharded by key
// hash. Its counters count the lookups it answered and those it did not.
type BasePrefill struct {
	shards []map[qmdb.Hash]prefillValue
	n      int

	hits   atomic.Uint64
	_      [56]byte // keep the counters on separate cache lines
	misses atomic.Uint64
}

func (p *BasePrefill) shard(k qmdb.Hash) map[qmdb.Hash]prefillValue {
	return p.shards[int(k[0])%len(p.shards)]
}

// get returns k's prefilled value; ok=false when k was not prefilled.
func (p *BasePrefill) get(k qmdb.Hash) (v []byte, found, ok bool) {
	pv, ok := p.shard(k)[k]
	if !ok {
		p.misses.Add(1)
		return nil, false, false
	}
	p.hits.Add(1)
	return pv.v, pv.found, true
}

// Len is the number of keys prefilled.
func (p *BasePrefill) Len() int {
	if p == nil {
		return 0
	}
	return p.n
}

// Stats returns the lookups answered from the prefill and those that were
// not (since it was built).
func (p *BasePrefill) Stats() (hits, misses uint64) {
	if p == nil {
		return 0, 0
	}
	return p.hits.Load(), p.misses.Load()
}

// FillBasePrefill reads every key of keys (one slice per producer;
// duplicates are read once) through the tree on workers goroutines, each
// with its own read transaction on db and the per-call reader lock, an
// evicted entry retried through a fresh transaction
// (NewLookupSourceRefreshing). Values are copied: they outlive the
// transactions. A stop that reports true makes the workers stop at the next
// key; the prefill then holds the keys read so far. The caller brackets the
// call with AppliedState to know which state the values come from.
func (r *QMDBRootComputer) FillBasePrefill(db kv.RoDB, keys [][]qmdb.Hash, workers int, stop func() bool) (*BasePrefill, error) {
	if workers < 1 {
		workers = 1
	}
	p := &BasePrefill{shards: make([]map[qmdb.Hash]prefillValue, workers)}
	total := 0
	for _, ks := range keys {
		total += len(ks)
	}
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		m := make(map[qmdb.Hash]prefillValue, total/workers+16)
		p.shards[w] = m
		wg.Add(1)
		go func(w int, m map[qmdb.Hash]prefillValue) {
			defer wg.Done()
			tx, err := db.BeginRo(context.Background())
			if err != nil {
				errs[w] = err
				return
			}
			defer tx.Rollback()
			src := NewLookupSourceRefreshing(r, tx, db)
			n := 0
			for _, ks := range keys {
				for _, k := range ks {
					if int(k[0])%workers != w {
						continue
					}
					if _, dup := m[k]; dup {
						continue
					}
					if n&255 == 0 && stop != nil && stop() {
						return
					}
					n++
					v, found := src.Get(k)
					var c []byte
					if found && len(v) > 0 {
						c = make([]byte, len(v))
						copy(c, v)
					}
					m[k] = prefillValue{v: c, found: found}
				}
			}
		}(w, m)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	for _, m := range p.shards {
		p.n += len(m)
	}
	return p, nil
}

// SetPrefill makes s answer the keys pf holds from it, without the tree or
// its lock; the rest read through as before. Only for a source of a reader
// whose base is any tree state pf's values are valid for (the caller's
// bracket, see the file comment). Returns s.
func (s *LookupSource) SetPrefill(pf *BasePrefill) *LookupSource {
	s.prefill = pf
	return s
}
