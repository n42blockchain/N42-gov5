// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package store

import (
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func hashOf(i int) types.Hash {
	var h types.Hash
	h[0], h[1], h[2] = byte(i), byte(i>>8), byte(i>>16)
	return h
}

func TestPutGetHas(t *testing.T) {
	s := New(1 << 20)
	if !s.Put(hashOf(1), []byte("abc")) {
		t.Fatal("put failed")
	}
	if s.Put(hashOf(1), []byte("abc")) {
		t.Fatal("duplicate accepted")
	}
	b, ok := s.Get(hashOf(1))
	if !ok || string(b) != "abc" || !s.Has(hashOf(1)) || s.Has(hashOf(2)) {
		t.Fatal("lookup mismatch")
	}
	st := s.Stats()
	if st.Puts != 1 || st.Duplicates != 1 || st.Bytes != 3 || st.Frames != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// TestUnincludedSurvivesFullStore: a full store never evicts a frame no
// block holds; the next frame is refused as Busy and stores nothing.
func TestUnincludedSurvivesFullStore(t *testing.T) {
	s := New(1000)
	for i := 0; i < 10; i++ {
		if !s.Put(hashOf(i), make([]byte, 100)) {
			t.Fatalf("put %d", i)
		}
	}
	if r := s.Admit(hashOf(100), Entry{Raw: make([]byte, 1)}, false); r != Busy {
		t.Fatalf("admit at cap: %v", r)
	}
	for i := 0; i < 10; i++ {
		if !s.Has(hashOf(i)) {
			t.Fatalf("frame %d evicted", i)
		}
	}
	if s.Has(hashOf(100)) {
		t.Fatal("busy frame stored")
	}
	// A duplicate is still a duplicate at the cap.
	if r := s.Admit(hashOf(3), Entry{Raw: make([]byte, 100)}, false); r != Duplicate {
		t.Fatalf("duplicate at cap: %v", r)
	}
	// Pinning by a written block and committing are not enough: the keep
	// margin must pass first.
	s.Include([]types.Hash{hashOf(0), hashOf(1)}, 5)
	if s.Admit(hashOf(100), Entry{Raw: make([]byte, 100)}, false) != Busy {
		t.Fatal("pinned frame made room")
	}
	st := s.Stats()
	if st.Busy != 2 || st.Evicted != 0 || st.Bytes != 1000 || st.Pending != 2 {
		t.Fatalf("stats %+v", st)
	}
	if s.Put(hashOf(200), make([]byte, 1001)) {
		t.Fatal("frame larger than budget accepted")
	}
}

// TestCommittedEvictedAfterKeep: a committed frame stays until the
// committed head is KeepBlocks past its block, then goes and a resend is a
// duplicate.
func TestCommittedEvictedAfterKeep(t *testing.T) {
	s := NewWithKeep(1000, 4)
	for i := 0; i < 10; i++ {
		s.Put(hashOf(i), make([]byte, 100))
	}
	s.Include([]types.Hash{hashOf(0), hashOf(1)}, 10)
	s.Commit(10, []types.Hash{hashOf(0), hashOf(1)})
	for n := uint64(11); n < 14; n++ {
		s.Commit(n, nil)
		if !s.Has(hashOf(0)) || !s.Has(hashOf(1)) {
			t.Fatalf("committed frame evicted at head %d, inside the margin", n)
		}
	}
	if s.Admit(hashOf(100), Entry{Raw: make([]byte, 100)}, false) != Busy {
		t.Fatal("room inside the margin")
	}
	s.Commit(14, nil)
	if s.Has(hashOf(0)) || s.Has(hashOf(1)) {
		t.Fatal("committed frame kept past the margin")
	}
	for i := 2; i < 10; i++ {
		if !s.Has(hashOf(i)) {
			t.Fatalf("unincluded frame %d evicted", i)
		}
	}
	if s.Admit(hashOf(100), Entry{Raw: make([]byte, 100)}, false) != Stored {
		t.Fatal("no room after eviction")
	}
	if s.Admit(hashOf(0), Entry{Raw: make([]byte, 100)}, false) != Duplicate {
		t.Fatal("evicted committed frame re-admitted")
	}
	st := s.Stats()
	if st.Evicted != 2 || st.EvictedBytes != 200 || st.Committed != 0 || st.CommittedHead != 14 {
		t.Fatalf("stats %+v", st)
	}
}

// TestForkPinReleased: a frame held only by a written fork block is not
// fillable and not evicted; when the committed head passes that height
// without it, it is fillable again. A frame pinned by a higher pending
// block stays pinned.
func TestForkPinReleased(t *testing.T) {
	s := NewWithKeep(1<<20, 2)
	for i := 0; i < 4; i++ {
		s.PutEntry(hashOf(i), Entry{Raw: make([]byte, 10), Gas: uint64(i)})
	}
	s.Include([]types.Hash{hashOf(0)}, 7) // fork sibling at 7
	s.Include([]types.Hash{hashOf(1)}, 7) // sibling at 7 ...
	s.Include([]types.Hash{hashOf(1)}, 8) // ... and re-taken at 8, still pending
	s.Include([]types.Hash{hashOf(2)}, 7) // winner at 7
	fillable := func() []int {
		var out []int
		s.ScanFillable(func(h types.Hash, e Entry) bool { out = append(out, int(e.Gas)); return true })
		return out
	}
	if got := fillable(); len(got) != 1 || got[0] != 3 {
		t.Fatalf("fillable %v", got)
	}
	s.Commit(7, []types.Hash{hashOf(2)})
	if got := fillable(); len(got) != 2 || got[0] != 0 || got[1] != 3 {
		t.Fatalf("fillable after commit %v", got)
	}
	if !s.Included(hashOf(1)) || !s.Included(hashOf(2)) || s.Included(hashOf(0)) {
		t.Fatal("pins after commit")
	}
	// A late fork block below the committed head pins nothing.
	s.Include([]types.Hash{hashOf(3)}, 6)
	if s.Included(hashOf(3)) {
		t.Fatal("late fork pinned a frame")
	}
	// Many commits later the abandoned frame is still there.
	for n := uint64(8); n < 30; n++ {
		s.Commit(n, nil)
	}
	if !s.Has(hashOf(0)) || !s.Has(hashOf(3)) || s.Has(hashOf(2)) {
		t.Fatal("eviction touched an unincluded frame or kept a committed one")
	}
	st := s.Stats()
	if st.Released != 2 || st.Pending != 0 {
		t.Fatalf("stats %+v", st)
	}
}

// TestBypassAdmission: pulls and local frames are stored over the budget;
// they are evictable after commit like any other frame.
func TestBypassAdmission(t *testing.T) {
	s := NewWithKeep(100, 1)
	s.Put(hashOf(0), make([]byte, 100))
	if s.Admit(hashOf(1), Entry{Raw: make([]byte, 50)}, false) != Busy {
		t.Fatal("not busy")
	}
	if s.Admit(hashOf(1), Entry{Raw: make([]byte, 50)}, true) != Stored {
		t.Fatal("bypass refused")
	}
	if s.Admit(hashOf(2), Entry{Raw: make([]byte, 500)}, true) != Stored {
		t.Fatal("bypass of an oversized frame refused")
	}
	if st := s.Stats(); st.Bytes != 650 || st.Bypassed != 2 {
		t.Fatalf("stats %+v", st)
	}
	s.Include([]types.Hash{hashOf(1), hashOf(2)}, 3)
	s.Commit(3, []types.Hash{hashOf(1), hashOf(2)})
	s.Commit(4, nil)
	if s.Has(hashOf(1)) || s.Has(hashOf(2)) || s.Stats().Bytes != 100 {
		t.Fatalf("pulled frames not evicted: %+v", s.Stats())
	}
	if s.Admit(hashOf(3), Entry{Raw: make([]byte, 1)}, false) != Busy {
		t.Fatal("unincluded frame evicted for room")
	}
}

func TestRingCompacts(t *testing.T) {
	s := NewWithKeep(1<<30, 0)
	for i := 0; i < 100000; i++ {
		h := hashOf(i)
		s.Put(h, make([]byte, 10))
		s.Include([]types.Hash{h}, uint64(i+1))
		s.Commit(uint64(i+1), []types.Hash{h})
	}
	if s.Len() != 0 || len(s.ring)-s.head != 0 || len(s.ring) > 4096 || len(s.cq) > 4096 {
		t.Fatalf("len %d ring %d head %d cq %d", s.Len(), len(s.ring), s.head, len(s.cq))
	}
}

func TestEnvBudget(t *testing.T) {
	t.Setenv(EnvBudget, "12345")
	t.Setenv(EnvKeepBlocks, "7")
	if s := NewFromEnv(); s.Stats().Budget != 12345 || s.Stats().KeepBlocks != 7 {
		t.Fatalf("stats %+v", s.Stats())
	}
	t.Setenv(EnvBudget, "")
	t.Setenv(EnvKeepBlocks, "")
	if s := NewFromEnv(); s.Stats().Budget != DefaultBudget || s.Stats().KeepBlocks != DefaultKeepBlocks {
		t.Fatal("defaults")
	}
}

func TestConcurrent(t *testing.T) {
	s := New(50_000)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				h := hashOf(w*100000 + i)
				s.Put(h, make([]byte, 64))
				s.Get(h)
				s.Has(hashOf(i))
			}
		}(w)
	}
	wg.Wait()
	if st := s.Stats(); st.Bytes > 50_000 {
		t.Fatalf("over budget: %+v", st)
	}
}

func TestScanRemoveTombstone(t *testing.T) {
	s := New(1 << 20)
	for i := 0; i < 10; i++ {
		s.PutEntry(hashOf(i), Entry{Raw: make([]byte, 10), Gas: uint64(i), Txs: 1})
	}
	var order []int
	s.Scan(func(h types.Hash, e Entry) bool {
		order = append(order, int(e.Gas))
		return len(order) < 5
	})
	if len(order) != 5 || order[0] != 0 || order[4] != 4 {
		t.Fatalf("scan order %v", order)
	}
	s.Remove([]types.Hash{hashOf(0), hashOf(3)})
	if s.Has(hashOf(0)) || s.Len() != 8 || s.Stats().Bytes != 80 || s.Stats().Removed != 2 {
		t.Fatalf("remove: %+v", s.Stats())
	}
	if s.Put(hashOf(3), make([]byte, 10)) {
		t.Fatal("tombstoned frame re-admitted")
	}
	// Remove leaves a frame a written block holds.
	s.Include([]types.Hash{hashOf(1)}, 1)
	s.Remove([]types.Hash{hashOf(1)})
	if !s.Has(hashOf(1)) {
		t.Fatal("Remove dropped an included frame")
	}
	order = order[:0]
	s.Scan(func(h types.Hash, e Entry) bool { order = append(order, int(e.Gas)); return true })
	if len(order) != 8 || order[0] != 1 || order[2] != 4 {
		t.Fatalf("scan after remove %v", order)
	}
}
