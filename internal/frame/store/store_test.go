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

func TestEvictsOldestFirst(t *testing.T) {
	s := New(1000)
	for i := 0; i < 10; i++ {
		s.Put(hashOf(i), make([]byte, 100))
	}
	if s.Len() != 10 {
		t.Fatalf("len %d", s.Len())
	}
	// Two more 150-byte frames need 300 bytes: frames 0..2 go.
	s.Put(hashOf(100), make([]byte, 150))
	s.Put(hashOf(101), make([]byte, 150))
	for i := 0; i < 3; i++ {
		if s.Has(hashOf(i)) {
			t.Fatalf("frame %d not evicted", i)
		}
	}
	for i := 3; i < 10; i++ {
		if !s.Has(hashOf(i)) {
			t.Fatalf("frame %d evicted early", i)
		}
	}
	st := s.Stats()
	if st.Evicted != 3 || st.EvictedBytes != 300 || st.Bytes != 1000 {
		t.Fatalf("stats %+v", st)
	}
	if s.Put(hashOf(200), make([]byte, 1001)) {
		t.Fatal("frame larger than budget accepted")
	}
}

func TestRingCompacts(t *testing.T) {
	s := New(100)
	for i := 0; i < 100000; i++ {
		s.Put(hashOf(i), make([]byte, 10))
	}
	if s.Len() != 10 || len(s.ring)-s.head != 10 || len(s.ring) > 4096 {
		t.Fatalf("len %d ring %d head %d", s.Len(), len(s.ring), s.head)
	}
}

func TestEnvBudget(t *testing.T) {
	t.Setenv(EnvBudget, "12345")
	if s := NewFromEnv(); s.Stats().Budget != 12345 {
		t.Fatalf("budget %d", s.Stats().Budget)
	}
	t.Setenv(EnvBudget, "")
	if s := NewFromEnv(); s.Stats().Budget != DefaultBudget {
		t.Fatal("default budget")
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
	order = order[:0]
	s.Scan(func(h types.Hash, e Entry) bool { order = append(order, int(e.Gas)); return true })
	if len(order) != 8 || order[0] != 1 || order[2] != 4 {
		t.Fatalf("scan after remove %v", order)
	}
}
