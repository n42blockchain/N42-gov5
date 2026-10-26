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

package parallel

import (
	"encoding/binary"
	"runtime"
	"sync"
	"testing"

	"github.com/holiman/uint256"
)

// presizeKey is the i-th test location: account keys for low i, storage
// keys above, so every shard sees both.
func presizeKey(i int) LocationKey {
	var k LocationKey
	binary.BigEndian.PutUint32(k.Address[16:], uint32(i))
	if i%3 == 2 {
		k.Field = FieldStorage
		binary.BigEndian.PutUint32(k.Slot[28:], uint32(i))
	}
	return k
}

// TestMVSPresizeConcurrentInserts stresses a presized store (slab entries,
// first-insert growth to minSlots, later doubling past it) with concurrent
// inserters and lock-free readers over overlapping keys, then checks the
// store against the serial result: one entry per key, every write present,
// deltas summed. Run under -race to check the entries' publication.
func TestMVSPresizeConcurrentInserts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		sizeTx int // SizeFor's numTxs; 0 leaves the doubling
		keys   int
	}{
		{"doubling", 0, 40000},
		{"presized", 40000, 40000},         // 512 slots per shard, ~156 locations: fits
		{"presized-overflow", 8000, 60000}, // 128 slots, ~234 locations: grows past minSlots
	} {
		t.Run(tc.name, func(t *testing.T) {
			for round := 0; round < 4; round++ {
				m := NewMVS()
				m.va = takeVersionArena()
				if tc.sizeTx > 0 {
					m.SizeFor(tc.sizeTx)
					if m.minSlots == 0 {
						t.Fatalf("SizeFor(%d) left minSlots 0", tc.sizeTx)
					}
				}
				workers := max(4, runtime.GOMAXPROCS(0))
				var wg sync.WaitGroup
				stop := make(chan struct{})
				// Lock-free readers race the inserts.
				var rwg sync.WaitGroup
				for r := 0; r < 2; r++ {
					rwg.Add(1)
					go func(r int) {
						defer rwg.Done()
						for i := r; ; i = (i + 7) % tc.keys {
							select {
							case <-stop:
								return
							default:
							}
							k := presizeKey(i)
							if e := m.lookup(&k); e != nil && e.key != k {
								t.Errorf("lookup(%d) returned entry of another key", i)
								return
							}
							m.Read(k, 1<<30)
						}
					}(r)
				}
				// Worker w writes every key as transaction w (full writes on
				// even workers, deltas on odd ones for account keys).
				for w := 0; w < workers; w++ {
					wg.Add(1)
					go func(w int) {
						defer wg.Done()
						for j := 0; j < tc.keys; j++ {
							i := (j*31 + w*977) % tc.keys
							k := presizeKey(i)
							if w%2 == 1 && k.Field == FieldBalance {
								m.WriteDelta(k, w, 0, uint256.NewInt(uint64(i+1)))
							} else {
								m.Write(k, w, 0, []byte{byte(w), byte(i)})
							}
						}
					}(w)
				}
				wg.Wait()
				close(stop)
				rwg.Wait()

				seen := make(map[LocationKey]*mvEntry, tc.keys)
				for s := range m.shards {
					sh := &m.shards[s]
					n := 0
					sh.tab.Load().each(func(e *mvEntry) {
						n++
						if prev, dup := seen[e.key]; dup {
							t.Fatalf("key %v has two entries %p %p", e.key, prev, e)
						}
						seen[e.key] = e
						if locationHash(&e.key) != e.hash {
							t.Fatalf("entry %v carries a stale hash", e.key)
						}
					})
					if n != sh.n {
						t.Fatalf("shard %d: %d entries in table, n=%d", s, n, sh.n)
					}
				}
				if len(seen) != tc.keys {
					t.Fatalf("store has %d keys, want %d", len(seen), tc.keys)
				}
				for i := 0; i < tc.keys; i++ {
					k := presizeKey(i)
					e := seen[k]
					if e == nil || m.lookup(&k) != e {
						t.Fatalf("key %d: lookup disagrees with the table", i)
					}
					if len(e.versions) != workers {
						t.Fatalf("key %d: %d versions, want %d", i, len(e.versions), workers)
					}
					for v := range e.versions {
						if e.versions[v].txIndex != v {
							t.Fatalf("key %d: version %d is tx %d", i, v, e.versions[v].txIndex)
						}
					}
					// The last full write and the deltas after it.
					var want uint256.Int
					last := -1
					for w := 0; w < workers; w++ {
						if w%2 == 1 && k.Field == FieldBalance {
							want.Add(&want, uint256.NewInt(uint64(i+1)))
						} else {
							last = w
							want.Clear()
						}
					}
					var d uint256.Int
					full, ftx, _, found, hasDelta := m.readAccountInto(&k, workers, &d)
					if !found || ftx != last || full[0] != byte(last) || full[1] != byte(i) {
						t.Fatalf("key %d: full write tx %d (found %v), want %d", i, ftx, found, last)
					}
					if hasDelta != !want.IsZero() || (hasDelta && d.Cmp(&want) != 0) {
						t.Fatalf("key %d: delta %v (has %v), want %v", i, &d, hasDelta, &want)
					}
				}
				m.release()
			}
		})
	}
}
