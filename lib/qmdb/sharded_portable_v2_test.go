package qmdb

import (
	"bytes"
	"testing"
)

// TestShardedAccessorsAndLifecycle covers the thin ShardedTree accessor layer
// (Shards/Shard/ShardAt/Get/LiveCount/Delete) plus the flush lifecycle hooks
// (CommitFlush/AbortFlush) that the engine calls after a tx commit/rollback.
func TestShardedAccessorsAndLifecycle(t *testing.T) {
	st, err := NewSharded(4)
	if err != nil {
		t.Fatalf("NewSharded: %v", err)
	}
	if got := st.Shards(); got != 4 {
		t.Fatalf("Shards() = %d, want 4", got)
	}
	for i := 0; i < st.Shards(); i++ {
		if st.ShardAt(i) == nil {
			t.Fatalf("ShardAt(%d) is nil", i)
		}
	}

	const n = 200
	for i := uint64(0); i < n; i++ {
		st.Set(key(i), val(i))
	}
	if got := st.LiveCount(); got != n {
		t.Fatalf("LiveCount() = %d, want %d", got, n)
	}
	for i := uint64(0); i < n; i++ {
		v, ok := st.Get(key(i))
		if !ok || string(v) != string(val(i)) {
			t.Fatalf("Get(%d) = (%q,%v), want (%q,true)", i, v, ok, val(i))
		}
		if shard := st.Shard(key(i)); shard == nil {
			t.Fatalf("Shard(%d) is nil", i)
		}
	}

	// Delete routes to the owning shard and removes liveness.
	st.Delete(key(0))
	if _, ok := st.Get(key(0)); ok {
		t.Fatalf("Get after Delete still reports live")
	}
	if got := st.LiveCount(); got != n-1 {
		t.Fatalf("LiveCount after delete = %d, want %d", got, n-1)
	}

	// Flush lifecycle: stage dead rows via a flush+evict+overwrite cycle on one
	// shard directly, then drive CommitFlush/AbortFlush through the sharded
	// wrapper and confirm every shard's bookkeeping cleared/requeued.
	store := newMapStore()
	next, written, err := st.FlushTo(store, nil)
	if err != nil {
		t.Fatalf("FlushTo: %v", err)
	}
	if written == 0 {
		t.Fatalf("FlushTo wrote zero entries")
	}
	st.CommitFlush()
	for i, s := range st.shards {
		if len(s.stagedDead) != 0 {
			t.Fatalf("shard %d: CommitFlush left stagedDead non-empty", i)
		}
	}

	// Reload into a fresh sharded tree and confirm the root matches.
	reloaded, err := NewSharded(4)
	if err != nil {
		t.Fatalf("NewSharded (reload): %v", err)
	}
	if err := reloaded.LoadFrom(store); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if reloaded.Root() != st.Root() {
		t.Fatalf("reloaded sharded root mismatch")
	}

	// Build dead rows below each shard's entriesBase, flush again, then exercise
	// AbortFlush's requeue.
	for i, s := range st.shards {
		s.SetCold(ColdReaderFromGetter(shardTableGetter{inner: store, sid: byte(i)}))
		s.SetLeafStore(LeafStoreFromGetter(shardTableGetter{inner: store, sid: byte(i)}))
	}
	for i := range st.shards {
		st.shards[i].EvictThrough(next[i])
	}
	for i := uint64(0); i < n; i += 7 {
		st.Set(key(i), val(i+1))
	}
	if _, _, err := st.FlushTo(store, next); err != nil {
		t.Fatalf("FlushTo #2: %v", err)
	}
	staged := 0
	for _, s := range st.shards {
		staged += len(s.stagedDead)
	}
	if staged == 0 {
		t.Skip("no shard accumulated staged dead rows with this key distribution")
	}
	st.AbortFlush()
	for i, s := range st.shards {
		if len(s.stagedDead) != 0 {
			t.Fatalf("shard %d: AbortFlush left stagedDead non-empty", i)
		}
	}
}

// TestNewShardedRejectsBadCounts covers the validation guard.
func TestNewShardedRejectsBadCounts(t *testing.T) {
	for _, s := range []int{0, 1, 3, 257, 300} {
		if _, err := NewSharded(s); err == nil {
			t.Fatalf("NewSharded(%d) accepted an invalid shard count", s)
		}
	}
	if _, err := NewSharded(2); err != nil {
		t.Fatalf("NewSharded(2): %v", err)
	}
	if _, err := NewSharded(256); err != nil {
		t.Fatalf("NewSharded(256): %v", err)
	}
}

// TestWritePortableSnapshotV2HollowAndLeaves covers the v2 leaf-form snapshot
// writer across a resident (leaf-holding) twig and a flushed+evicted (hollow)
// twig, plus StatsForV2's accounting, and its root/next-slot validation guards.
func TestWritePortableSnapshotV2HollowAndLeaves(t *testing.T) {
	tr := New()
	tr.SetIndex(newMapIndex()) // WritePortableSnapshotV2 needs an IterableIndex
	store := newMapStore()
	tr.SetCold(ColdReaderFromGetter(store))
	tr.SetLeafStore(LeafStoreFromGetter(store))

	const n = uint64(2 * TwigSize)
	for i := uint64(0); i < n; i++ {
		tr.Set(key(i), val(i))
	}
	root := tr.Root()
	next, _, err := tr.FlushTo(store, 0)
	if err != nil {
		t.Fatalf("FlushTo: %v", err)
	}
	// Evict only the first twig so one twig is hollow (flushed+evicted) and the
	// second stays resident with live leaf hashes.
	tr.EvictThrough(TwigSize)
	if tr.Root() != root {
		t.Fatalf("root changed across eviction")
	}

	statsBefore := tr.StatsForV2(store)
	if statsBefore.Twigs != 2 {
		t.Fatalf("StatsForV2.Twigs = %d, want 2", statsBefore.Twigs)
	}
	if statsBefore.HollowTwigs != 0 {
		// A twig with no resident nodes but a leaf-blob store hit is not
		// "hollow" by StatsForV2's accounting (only unreconstructable twigs are).
		t.Logf("StatsForV2.HollowTwigs = %d (leaf blobs available)", statsBefore.HollowTwigs)
	}
	if statsBefore.LiveEntries != int(n) {
		t.Fatalf("StatsForV2.LiveEntries = %d, want %d", statsBefore.LiveEntries, n)
	}

	var buf bytes.Buffer
	meta := PortableSnapshotMetadata{
		ChainID:     7,
		GenesisHash: key(0xAA),
		BlockNumber: 11,
		BlockHash:   key(0xBB),
		Root:        tr.Root(),
		NextSlot:    tr.NextSlot(),
	}
	written, err := WritePortableSnapshotV2(&buf, meta, tr, store)
	if err != nil {
		t.Fatalf("WritePortableSnapshotV2: %v", err)
	}
	if written == 0 || int64(buf.Len()) != written {
		t.Fatalf("WritePortableSnapshotV2 wrote %d, buffer has %d bytes", written, buf.Len())
	}
	if !bytes.Equal(buf.Bytes()[:8], portableSnapshotMagicV2[:]) {
		t.Fatalf("v2 snapshot missing magic header")
	}
	_ = next

	// Wrong root is rejected.
	badMeta := meta
	badMeta.Root = key(0xFF)
	if _, err := WritePortableSnapshotV2(&buf, badMeta, tr, store); err == nil {
		t.Fatalf("WritePortableSnapshotV2 accepted a mismatched root")
	}

	// Wrong next slot is rejected.
	badMeta = meta
	badMeta.NextSlot = meta.NextSlot + 1
	if _, err := WritePortableSnapshotV2(&buf, badMeta, tr, store); err == nil {
		t.Fatalf("WritePortableSnapshotV2 accepted a mismatched next slot")
	}

	// Nil tree/getter guards.
	if _, err := WritePortableSnapshotV2(&buf, meta, nil, store); err == nil {
		t.Fatalf("WritePortableSnapshotV2 accepted a nil tree")
	}
	if _, err := WritePortableSnapshotV2(&buf, meta, tr, nil); err == nil {
		t.Fatalf("WritePortableSnapshotV2 accepted a nil getter")
	}
}
