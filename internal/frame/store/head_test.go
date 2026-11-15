// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package store

import (
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func headOrder(s *Store) []types.Hash {
	var out []types.Hash
	s.HeadUnready(0, func(f HeadFrame) bool {
		out = append(out, f.H)
		return true
	})
	return out
}

// TestHeadIndex: the unready index reports, oldest first, the stored
// frames without senders that no written block holds; resolved frames
// leave it, and a frame pinned by a written block is listed but not
// reported until an abandoned fork releases it.
func TestHeadIndex(t *testing.T) {
	s := New(1 << 20)
	// Frames stored before the index is on are seeded in arrival order.
	s.Admit(hashOf(0), Entry{Raw: []byte{0}}, false)
	s.Admit(hashOf(1), Entry{Raw: []byte{1}}, false)
	s.SetSenders(hashOf(1), &Senders{})
	if got := headOrder(s); len(got) != 0 {
		t.Fatalf("index off reported %d frames", len(got))
	}
	s.EnableHeadIndex()
	for i := 2; i < 6; i++ {
		s.Admit(hashOf(i), Entry{Raw: []byte{byte(i)}}, false)
	}
	want := []types.Hash{hashOf(0), hashOf(2), hashOf(3), hashOf(4), hashOf(5)}
	if got := headOrder(s); !equalHashes(got, want) {
		t.Fatalf("head order %x, want %x", got, want)
	}
	// Senders cached: frame 2 leaves; frame 3 pinned by a written block is
	// not reported.
	s.SetSenders(hashOf(2), &Senders{})
	s.Include([]types.Hash{hashOf(3)}, 10)
	want = []types.Hash{hashOf(0), hashOf(4), hashOf(5)}
	if got := headOrder(s); !equalHashes(got, want) {
		t.Fatalf("head order %x, want %x", got, want)
	}
	// The fork is abandoned (another block commits at 10): frame 3 is
	// fillable again and reported in its arrival position.
	s.Commit(10, []types.Hash{hashOf(0)})
	want = []types.Hash{hashOf(3), hashOf(4), hashOf(5)}
	if got := headOrder(s); !equalHashes(got, want) {
		t.Fatalf("head order after release %x, want %x", got, want)
	}
	// The commit trimmed the resolved prefix (frames 0, 1 by never being
	// listed, 2): 3, 4, 5 remain.
	if n := s.HeadIndexLen(); n != 3 {
		t.Fatalf("index holds %d entries, want 3", n)
	}
	// The visit bound stops the walk.
	n := 0
	s.HeadUnready(1, func(HeadFrame) bool { n++; return true })
	if n != 1 {
		t.Fatalf("visited %d with a bound of 1", n)
	}
}

// TestHeadIndexTrimsCommitted: the committed frames of the keep margin are
// not walked; the index holds only frames that may still need senders, so
// a reader of the head visits them and nothing else (the LocalAger's whole
// store walk under the read lock, run 74).
func TestHeadIndexTrimsCommitted(t *testing.T) {
	s := NewWithKeep(1<<24, 64)
	s.EnableHeadIndex()
	const committed, fresh = 3000, 40
	var blk []types.Hash
	for i := 0; i < committed; i++ {
		s.Admit(hashOf(i), Entry{Raw: []byte{1}}, false)
		blk = append(blk, hashOf(i))
		if len(blk) == 100 {
			s.Commit(uint64(1+i/100), blk)
			blk = nil
		}
	}
	for i := committed; i < committed+fresh; i++ {
		s.Admit(hashOf(i), Entry{Raw: []byte{1}}, false)
	}
	if s.Len() != committed+fresh {
		t.Fatalf("store holds %d", s.Len())
	}
	if n := s.HeadIndexLen(); n != fresh {
		t.Fatalf("index holds %d entries, want the %d unready ones", n, fresh)
	}
	// AgedUnready through the index returns what the full walk returns.
	viaIndex := s.AgedUnready(1<<62, 1000, nil)
	s.mu.Lock()
	s.hqOn = false
	s.mu.Unlock()
	viaWalk := s.AgedUnready(1<<62, 1000, nil)
	if len(viaIndex) != fresh || len(viaWalk) != fresh {
		t.Fatalf("aged unready via index %d, via walk %d, want %d", len(viaIndex), len(viaWalk), fresh)
	}
	for i := range viaIndex {
		if viaIndex[i].H != viaWalk[i].H || viaIndex[i].At != viaWalk[i].At {
			t.Fatalf("position %d differs", i)
		}
	}
}

// TestFinishRecoverKeep: senders another source cached while a recovery
// ran stand; the recovery's result is discarded and readiness is counted
// once. FinishRecover (no keep) still replaces them.
func TestFinishRecoverKeep(t *testing.T) {
	s := New(1 << 20)
	s.Admit(hashOf(1), Entry{Raw: []byte{1}}, false)
	s.Admit(hashOf(2), Entry{Raw: []byte{2}}, false)
	attested := &Senders{Addrs: []types.Address{{1}}}
	local := &Senders{Addrs: []types.Address{{1}}}

	tk := s.BeginRecover(hashOf(1), true)
	if !tk.Own {
		t.Fatal("not owned")
	}
	s.SetSenders(hashOf(1), attested)
	if s.FinishRecoverKeep(tk, local) {
		t.Fatal("the late local recovery reported first")
	}
	if s.Senders(hashOf(1)) != attested {
		t.Fatal("attested senders replaced")
	}
	if info := s.RecoverInfoOf(hashOf(1)); info.Running || !info.Ready {
		t.Fatalf("state %+v", info)
	}
	// Uncontested: the recovery's senders are cached and reported first.
	tk = s.BeginRecover(hashOf(2), true)
	if !s.FinishRecoverKeep(tk, local) || s.Senders(hashOf(2)) != local {
		t.Fatal("local recovery not cached")
	}
	st := s.Stats()
	var att, loc uint64
	for i := range st.ReadyAttested {
		att += st.ReadyAttested[i]
		loc += st.ReadyLocal[i]
	}
	if att != 1 || loc != 1 || st.WithSenders != 2 {
		t.Fatalf("ready attested %d local %d with senders %d", att, loc, st.WithSenders)
	}
}

// TestChangeNotify: admission, cached senders, a released recovery, pins,
// commits and removals each notify.
func TestChangeNotify(t *testing.T) {
	s := New(1 << 20)
	var n atomic.Int64
	s.SetChangeNotify(func() { n.Add(1) })
	step := func(what string, fn func()) {
		t.Helper()
		before := n.Load()
		fn()
		if n.Load() == before {
			t.Fatalf("%s did not notify", what)
		}
	}
	step("admit", func() { s.Admit(hashOf(1), Entry{Raw: []byte{1}}, false) })
	step("admit", func() { s.Admit(hashOf(2), Entry{Raw: []byte{1}}, false) })
	step("senders", func() { s.SetSenders(hashOf(1), &Senders{}) })
	step("recovery", func() { s.FinishRecover(s.BeginRecover(hashOf(2), true), nil) })
	step("claim", func() { s.Claim([]types.Hash{hashOf(2)}) })
	step("include", func() { s.Include([]types.Hash{hashOf(2)}, 5) })
	step("commit", func() { s.Commit(5, []types.Hash{hashOf(2)}) })
	step("remove", func() { s.Remove([]types.Hash{hashOf(1)}) })
	s.SetChangeNotify(nil)
	before := n.Load()
	s.Admit(hashOf(3), Entry{Raw: []byte{1}}, false)
	if n.Load() != before {
		t.Fatal("notified after removal")
	}
}

func equalHashes(a, b []types.Hash) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
