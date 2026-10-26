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
	"bytes"
	"os"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/log"
)

// Store invariant checks (N42_MVS_CHECK=1, default off): round S139's
// leader lost one recipient's credit in a block every other node executed
// right. The checks run once per block after the executor's run and log
// what they find (level error, "mvs check:"), never panic:
//
//  1. duplicate keys: no location has two entries, every entry sits in the
//     shard its hash picks and carries its key's hash;
//  2. lost writes: every write of a transaction's final write set has its
//     version (transaction, incarnation, kind, value) in the entry the
//     store's lookup returns for the key;
//  4. entry ownership: the entry a write was stored in is the one the
//     store's lookup returns for its key (checked at the write).
//
// Check 3, the apply walk against the store, lives with the walk
// (internal/parallel_processor.go).
var mvsCheck = os.Getenv("N42_MVS_CHECK") == "1"

// MVSCheckEnabled reports whether N42_MVS_CHECK is on.
func MVSCheckEnabled() bool { return mvsCheck }

// SetMVSCheckForTest switches the checks and returns the restore.
func SetMVSCheckForTest(on bool) func() {
	prev := mvsCheck
	mvsCheck = on
	return func() { mvsCheck = prev }
}

// PresizeEnabled reports whether N42_MVS_PRESIZE is on (the reports carry it).
func PresizeEnabled() bool { return presizeMVS }

// SetPresizeForTest switches N42_MVS_PRESIZE and returns the restore.
func SetPresizeForTest(on bool) func() {
	prev := presizeMVS
	presizeMVS = on
	return func() { presizeMVS = prev }
}

// MVSCheckMaxReports caps the error lines one check of one block logs.
const MVSCheckMaxReports = 16

// mvsCheckState is the executor's check state, allocated only when the
// checks are on.
type mvsCheckState struct {
	foreign atomic.Int64 // check 4 failures
	mu      sync.Mutex
	samples []foreignWrite
}

type foreignWrite struct {
	key     LocationKey
	txIndex int
	delta   bool
	got     *mvEntry // entry the write went to
	want    *mvEntry // entry lookup returns
}

// checkWriteEntry is check 4 for a write of txIndex at key stored in ent.
func (e *Executor) checkWriteEntry(key *LocationKey, ent *mvEntry, txIndex int, delta bool) {
	want := e.mvs.lookup(key)
	if want == ent && ent != nil && ent.key == *key {
		return
	}
	cs := e.check
	if cs == nil {
		return
	}
	if cs.foreign.Add(1) <= MVSCheckMaxReports {
		cs.mu.Lock()
		cs.samples = append(cs.samples, foreignWrite{key: *key, txIndex: txIndex, delta: delta, got: ent, want: want})
		cs.mu.Unlock()
	}
}

// MVSCheckReport is what CheckStore found.
type MVSCheckReport struct {
	Entries    int // entries scanned
	Duplicates int // keys with more than one entry
	Misplaced  int // entries in the wrong shard or with a stale hash
	CountDiffs int // shards whose entry count disagrees with the table
	Writes     int // final writes checked
	Lost       int // writes without their version in the looked-up entry
	Foreign    int // writes stored in an entry lookup does not return
	Skipped    string
}

// Issues is the number of violations found.
func (r MVSCheckReport) Issues() int {
	return r.Duplicates + r.Misplaced + r.CountDiffs + r.Lost + r.Foreign
}

// CheckStore runs checks 1, 2 and 4 on the executor's store after Run and
// logs every violation (block and presize identify the report).
func (e *Executor) CheckStore(block uint64) MVSCheckReport {
	var rep MVSCheckReport
	m := e.mvs
	if m == nil {
		rep.Skipped = "no store"
		return rep
	}
	ctx := []any{"block", block, "numTxs", e.numTxs, "presize", presizeMVS, "minSlots", m.minSlots}
	var logged atomic.Int32
	report := func(msg string, kv ...any) {
		if logged.Add(1) > MVSCheckMaxReports {
			return
		}
		log.Error(msg, append(append([]any{}, ctx...), kv...)...)
	}
	workers := min(runtime.GOMAXPROCS(0), 16)

	// Check 1, per shard: a key's entries can only meet in its own shard
	// when every entry is in the shard its hash picks (also checked).
	var entries, dups, misplaced, countDiffs atomic.Int64
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seen := make(map[LocationKey]int, 128)
			for {
				s := int(next.Add(1) - 1)
				if s >= mvsShards {
					return
				}
				clear(seen)
				sh := &m.shards[s]
				n := 0
				sh.tab.Load().each(func(en *mvEntry) {
					n++
					seen[en.key]++
					if h := locationHash(&en.key); h != en.hash || int(h>>56) != s {
						misplaced.Add(1)
						report("mvs check: misplaced entry", "check", 1, "key", en.key.String(), "shard", s, "hashShard", int(h>>56), "staleHash", h != en.hash)
					}
				})
				entries.Add(int64(n))
				if n != sh.n {
					countDiffs.Add(1)
					report("mvs check: shard count differs", "check", 1, "shard", s, "tableEntries", n, "count", sh.n)
				}
				for k, c := range seen {
					if c > 1 {
						dups.Add(1)
						report("mvs check: duplicate key", "check", 1, "key", k.String(), "shard", s, "count", c)
					}
				}
			}
		}()
	}
	wg.Wait()
	rep.Entries, rep.Duplicates, rep.Misplaced, rep.CountDiffs = int(entries.Load()), int(dups.Load()), int(misplaced.Load()), int(countDiffs.Load())

	// Check 4: what the writes recorded.
	if cs := e.check; cs != nil {
		rep.Foreign = int(cs.foreign.Load())
		cs.mu.Lock()
		for _, f := range cs.samples {
			report("mvs check: write stored in a foreign entry", "check", 4, "key", f.key.String(), "tx", f.txIndex, "delta", f.delta,
				"entryNil", f.got == nil, "lookupNil", f.want == nil, "entryKeyMatches", f.got != nil && f.got.key == f.key)
		}
		cs.mu.Unlock()
	}

	// Check 2: every final write has its version.
	if e.fellBack {
		rep.Skipped = "sequential fallback"
	} else {
		var writes, lost atomic.Int64
		const chunk = 2048
		next.Store(0)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var nw, nl int64
				for {
					lo := int(next.Add(chunk) - chunk)
					if lo >= e.numTxs {
						break
					}
					for i := lo; i < min(lo+chunk, e.numTxs); i++ {
						rw := e.rwSets[i]
						if rw == nil {
							continue
						}
						inc := e.incarnation[i]
						for wi := range rw.Writes {
							wd := &rw.Writes[wi]
							if laterWriteOf(rw.Writes[wi+1:], &wd.Key) {
								continue // the later write replaced this one
							}
							nw++
							if why := m.versionMissing(&wd.Key, i, inc, wd); why != "" {
								nl++
								report("mvs check: lost write", "check", 2, "key", wd.Key.String(), "tx", i, "inc", inc, "delta", wd.Delta != nil, "why", why)
							}
						}
					}
				}
				writes.Add(nw)
				lost.Add(nl)
			}()
		}
		wg.Wait()
		rep.Writes, rep.Lost = int(writes.Load()), int(lost.Load())
	}
	if rep.Issues() > 0 {
		log.Error("mvs check: block has violations", append(append([]any{}, ctx...), "entries", rep.Entries, "duplicates", rep.Duplicates,
			"misplaced", rep.Misplaced, "countDiffs", rep.CountDiffs, "writes", rep.Writes, "lost", rep.Lost, "foreign", rep.Foreign)...)
	}
	return rep
}

// laterWriteOf reports whether ws writes key.
func laterWriteOf(ws []WriteDescriptor, key *LocationKey) bool {
	for i := range ws {
		if ws[i].Key == *key {
			return true
		}
	}
	return false
}

// versionMissing (called after Run, the store quiescent) returns why the entry lookup finds for key lacks wd as
// txIndex's version at incarnation inc ("" when it has it).
func (m *MVS) versionMissing(key *LocationKey, txIndex int, inc uint32, wd *WriteDescriptor) string {
	en := m.lookup(key)
	if en == nil {
		return "no entry"
	}
	// No lock: the store is quiescent once Run returned (its workers
	// joined), and a reader count shared by 16 checking goroutines on a hot
	// recipient's entry was most of the check's time.
	idx := en.search(txIndex)
	if idx >= len(en.versions) || en.versions[idx].txIndex != txIndex {
		return "no version"
	}
	v := &en.versions[idx]
	switch {
	case v.incarnation != inc:
		return "incarnation"
	case v.isDelta != (wd.Delta != nil):
		return "kind"
	case v.isDelta && v.delta.Cmp(wd.Delta) != 0:
		return "delta value"
	case !v.isDelta && !bytes.Equal(v.value, wd.Value):
		return "value"
	}
	return ""
}

// AccountCheck is an account's final composition recomputed from its entry
// (found through the store's lookup, not the apply walk): the last full
// write before numTxs (Full, HasFull), the sum of the deltas after it and
// their count.
type AccountCheck struct {
	Found   bool
	HasFull bool
	Full    []byte
	Delta   uint256.Int
	NDeltas int
}

// CheckAccount recomputes key's final composition for check 3.
func (m *MVS) CheckAccount(key LocationKey, numTxs int) AccountCheck {
	var c AccountCheck
	en := m.lookup(&key)
	if en == nil {
		return c
	}
	c.Found = true
	// No lock: called after Run, on a quiescent store (versionMissing).
	for i := en.search(numTxs) - 1; i >= 0; i-- {
		v := &en.versions[i]
		if v.isDelta {
			c.Delta.Add(&c.Delta, &v.delta)
			c.NDeltas++
			continue
		}
		c.HasFull, c.Full = true, append([]byte(nil), v.value...)
		break
	}
	return c
}

// DropVersionForTest removes txIndex's version at key: a lost write.
func (m *MVS) DropVersionForTest(key LocationKey, txIndex int) {
	m.Delete(key, txIndex)
}

// String is the key for the check reports.
func (k LocationKey) String() string {
	b := make([]byte, 0, 2+40+3+66)
	b = append(b, k.Address.Hex()...)
	b = append(b, '/')
	b = append(b, byte('0'+k.Field/10), byte('0'+k.Field%10))
	if k.Field == FieldStorage {
		b = append(b, '/')
		b = append(b, k.Slot.Hex()...)
	}
	return string(b)
}

// Composed is the account value the apply walk should give a full-write
// account: the full write composed with the deltas after it.
func (c AccountCheck) Composed() []byte {
	if c.NDeltas == 0 {
		return c.Full
	}
	d := c.Delta
	return composeAccount(c.Full, &d)
}
