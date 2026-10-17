// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// rsBlock is one storage-only dirty set and the full key -> word view the
// tree holds after it (absent = deleted / never written).
type rsBlock struct {
	storage map[types.Address]map[types.Hash]*uint256.Int
	after   map[qmdb.Hash][]byte
}

var rsAddr = types.Address{0x42}

func rsSlot(i int) types.Hash {
	var h types.Hash
	h[30], h[31] = byte(i>>8), byte(i)
	return h
}

func rsKey(i int) qmdb.Hash { return qmdb.Hash(StorageKeyHash(rsAddr, rsSlot(i))) }

func rsWord(v uint64) []byte {
	var b [32]byte
	uint256.NewInt(v).WriteToSlice(b[:])
	return b[:]
}

// rsBlocks builds n blocks over `keys` slots: each touches about half of
// them (updates, deletes of live slots, re-inserts).
func rsBlocks(n, keys int, seed int64) []rsBlock {
	rng := rand.New(rand.NewSource(seed))
	cur := map[qmdb.Hash][]byte{}
	out := make([]rsBlock, 0, n)
	for b := 0; b < n; b++ {
		slots := map[types.Hash]*uint256.Int{}
		for i := 0; i < keys; i++ {
			if b > 0 && rng.Intn(2) == 0 {
				continue
			}
			k := rsKey(i)
			if _, live := cur[k]; live && rng.Intn(5) == 0 {
				slots[rsSlot(i)] = uint256.NewInt(0) // delete
				delete(cur, k)
				continue
			}
			v := uint64(b)<<32 | uint64(i) | 1
			slots[rsSlot(i)] = uint256.NewInt(v)
			cur[k] = rsWord(v)
		}
		after := make(map[qmdb.Hash][]byte, len(cur))
		for k, v := range cur {
			after[k] = v
		}
		out = append(out, rsBlock{storage: map[types.Address]map[types.Hash]*uint256.Int{rsAddr: slots}, after: after})
	}
	return out
}

func rsCompute(t *testing.T, rc *QMDBRootComputer, b rsBlock) types.Hash {
	t.Helper()
	root, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{}, b.storage)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// rsCheck asserts that a span's reads of every key equal view.
func rsCheck(rc *QMDBRootComputer, keys int, view map[qmdb.Hash][]byte, locked bool) (bad int) {
	for i := 0; i < keys; i++ {
		k := rsKey(i)
		var v []byte
		var found bool
		if locked {
			v, found, _ = rc.LookupLocked(k, nil)
		} else {
			v, found, _ = rc.Lookup(k, nil)
		}
		want, live := view[k]
		if found != live || (live && !bytes.Equal(v, want)) {
			bad++
		}
	}
	return bad
}

// lookupWithin runs fn on another goroutine and reports whether it returned
// within d (a reader the exclusive lock would block does not).
func lookupWithin(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		<-done
		return false
	}
}

// TestReadSnapshotReadsDuringRoot: under N42_QMDB_READ_SNAPSHOT a Lookup
// made while ComputeRoot(N) prepares returns N-1's values without waiting,
// one made while it folds returns N's, RootLocked waits for the fold, and
// after the root every value is N's.
func TestReadSnapshotReadsDuringRoot(t *testing.T) {
	defer SetReadSnapshot(SetReadSnapshot(true))
	const keys = 600
	blocks := rsBlocks(2, keys, 1)
	rc := NewQMDBRootComputer()
	rsCompute(t, rc, blocks[0])
	pre := rc.AppliedState().Gen

	ref := NewQMDBRootComputer()
	rsCompute(t, ref, blocks[0])
	wantRoot := rsCompute(t, ref, blocks[1])

	var prepBad, foldBad int
	var prepFast, foldFast bool
	var prepGen, foldGen uint64
	var rootDuringFold atomic.Value
	testHookNarrowPrepared = func() {
		prepFast = lookupWithin(2*time.Second, func() {
			prepGen = rc.AppliedState().Gen
			prepBad = rsCheck(rc, keys, blocks[0].after, false)
		})
	}
	testHookNarrowFolding = func() {
		foldFast = lookupWithin(2*time.Second, func() {
			foldGen = rc.AppliedState().Gen
			foldBad = rsCheck(rc, keys, blocks[1].after, false)
		})
		// A RootLocked reader started now must see the folded root.
		started := make(chan struct{})
		go func() {
			unlock := rc.LockReaders()
			close(started)
			rootDuringFold.Store(rc.RootLocked())
			unlock()
		}()
		<-started
	}
	defer func() { testHookNarrowPrepared, testHookNarrowFolding = nil, nil }()

	root := rsCompute(t, rc, blocks[1])
	testHookNarrowPrepared, testHookNarrowFolding = nil, nil
	if root != wantRoot {
		t.Fatalf("narrow root %x, reference %x", root, wantRoot)
	}
	if !prepFast || !foldFast {
		t.Fatalf("reads blocked during the root: prepare=%v fold=%v", prepFast, foldFast)
	}
	if prepBad != 0 || prepGen != pre {
		t.Fatalf("during prepare: %d reads not N-1's, applied gen %d want %d", prepBad, prepGen, pre)
	}
	if foldBad != 0 || foldGen != pre+1 {
		t.Fatalf("during fold: %d reads not N's, applied gen %d want %d", foldBad, foldGen, pre+1)
	}
	deadline := time.Now().Add(5 * time.Second)
	for rootDuringFold.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got, _ := rootDuringFold.Load().(types.Hash); got != wantRoot {
		t.Fatalf("RootLocked during the fold returned %x, want %x", got, wantRoot)
	}
	if bad := rsCheck(rc, keys, blocks[1].after, false); bad != 0 {
		t.Fatalf("after the root: %d reads not N's", bad)
	}
	if ph := LastComputeRootPhases(); ph == nil || ph.Exclusive <= 0 || ph.Exclusive > ph.Apply {
		t.Fatalf("phases %+v: exclusive section not recorded within the apply", ph)
	}
}

// TestReadSnapshotNoTornReads: readers hammer the tree across many roots,
// with -race in mind. Every span bracketed by one applied generation reads
// exactly that generation's values for every key -- never a mix of N-1 and
// N -- and both modes produce the same roots and undo records.
func TestReadSnapshotNoTornReads(t *testing.T) {
	const keys = 300
	nBlocks := 40
	if testing.Short() {
		nBlocks = 12
	}
	blocks := rsBlocks(nBlocks, keys, 2)

	run := func(t *testing.T, snapshot bool) ([]types.Hash, []*qmdb.BlockUndo) {
		defer SetReadSnapshot(SetReadSnapshot(snapshot))
		rc := NewQMDBRootComputer()
		rc.EnableUndoRecording()
		base := rc.AppliedState().Gen
		views := make([]map[qmdb.Hash][]byte, 0, nBlocks+1)
		views = append(views, map[qmdb.Hash][]byte{})
		for _, b := range blocks {
			views = append(views, b.after)
		}
		var viewsMu sync.RWMutex
		viewAt := func(gen uint64) map[qmdb.Hash][]byte {
			viewsMu.RLock()
			defer viewsMu.RUnlock()
			return views[gen-base]
		}
		stop := make(chan struct{})
		var wg sync.WaitGroup
		var checked, torn atomic.Int64
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(locked bool) {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					g0 := rc.AppliedState().Gen
					var bad int
					if locked {
						unlock := rc.LockReaders()
						bad = rsCheck(rc, keys, viewAt(g0), true)
						unlock()
					} else {
						bad = rsCheck(rc, keys, viewAt(g0), false)
					}
					if rc.AppliedState().Gen != g0 {
						continue // a root landed mid-span: no single version to hold it to
					}
					checked.Add(1)
					if bad != 0 {
						torn.Add(1)
					}
				}
			}(w%2 == 0)
		}
		roots := make([]types.Hash, 0, nBlocks)
		undos := make([]*qmdb.BlockUndo, 0, nBlocks)
		for _, b := range blocks {
			roots = append(roots, rsCompute(t, rc, b))
			undos = append(undos, rc.TakeUndo())
			time.Sleep(200 * time.Microsecond)
		}
		close(stop)
		wg.Wait()
		if torn.Load() != 0 {
			t.Fatalf("%d of %d single-version spans read values of another version", torn.Load(), checked.Load())
		}
		if checked.Load() == 0 {
			t.Fatal("no span completed")
		}
		return roots, undos
	}
	var oldRoots, newRoots []types.Hash
	var oldUndo, newUndo []*qmdb.BlockUndo
	t.Run("old", func(t *testing.T) { oldRoots, oldUndo = run(t, false) })
	t.Run("snapshot", func(t *testing.T) { newRoots, newUndo = run(t, true) })
	for i := range oldRoots {
		if oldRoots[i] != newRoots[i] {
			t.Fatalf("block %d: root %x (snapshot) vs %x (old)", i, newRoots[i], oldRoots[i])
		}
		if !bytes.Equal(oldUndo[i].Marshal(), newUndo[i].Marshal()) {
			t.Fatalf("block %d: undo records differ", i)
		}
	}
}
