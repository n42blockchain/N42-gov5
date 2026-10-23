// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// shardedMapIndex is the in-RAM index split into 256 Go maps by the key
// hash's first byte. Lookups cost the same as mapIndex (one byte selects the
// map); what the split buys is that disjoint first-byte ranges are disjoint
// maps, so the sharded apply (shard_apply.go) can write the index from
// several goroutines at once: its ops are sorted by key hash, and every
// worker owns a contiguous run of whole first-byte shards. Selected by
// NewIndexFor when N42_QMDB_SHARD_APPLY is on; the contents are exactly the
// live mapping a mapIndex would hold.

package qmdb

// indexShards is the shard count of shardedMapIndex (one per first byte).
const indexShards = 256

type shardedMapIndex struct {
	m [indexShards]map[Hash]uint64
}

var _ IterableIndex = (*shardedMapIndex)(nil)

// newShardedMapIndex returns an empty index pre-sized for about n keys.
func newShardedMapIndex(n int) *shardedMapIndex {
	x := &shardedMapIndex{}
	per := 0
	if n > 0 {
		per = n/indexShards + n/(indexShards*16) + 1
	}
	for i := range x.m {
		x.m[i] = make(map[Hash]uint64, per)
	}
	return x
}

func (x *shardedMapIndex) Get(k Hash) (uint64, bool) {
	s, ok := x.m[k[0]][k]
	if ok {
		idxGetHit.Add(1)
	} else {
		idxGetMiss.Add(1)
	}
	return s, ok
}

func (x *shardedMapIndex) Put(k Hash, s uint64) { idxPuts.Add(1); x.m[k[0]][k] = s }
func (x *shardedMapIndex) Delete(k Hash)        { idxDeletes.Add(1); delete(x.m[k[0]], k) }

func (x *shardedMapIndex) Len() int {
	n := 0
	for i := range x.m {
		n += len(x.m[i])
	}
	return n
}

func (x *shardedMapIndex) Range(fn func(Hash, uint64) bool) {
	for i := range x.m {
		for k, s := range x.m[i] {
			if !fn(k, s) {
				return
			}
		}
	}
}

// get, put and del are the uncounted forms the sharded apply's workers use;
// it adds their totals to the package counters once per batch.
func (x *shardedMapIndex) get(k Hash) (uint64, bool) { s, ok := x.m[k[0]][k]; return s, ok }
func (x *shardedMapIndex) put(k Hash, s uint64)      { x.m[k[0]][k] = s }
func (x *shardedMapIndex) del(k Hash)                { delete(x.m[k[0]], k) }
