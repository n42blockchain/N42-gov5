// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"bytes"
	"context"
	"errors"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// The lag pipeline's QMDB cadence, modelled on the live one
// (internal/lag_writer.go, lean hand-off): import b installs its read tx as
// the cold source (SetCold, or SetColdLean under the switch) and computes
// its root; its hand-off joins write b-1 (CommitFlushed), takes the undo and
// starts the capture of b (StartFlushTo with the eviction first, holding the
// tree's lock on its own goroutine); a writer goroutine replays the
// captured rows into its own RwTx once the capture is done. Import b+1 then
// runs while capture b may still hold the lock: SetColdLean parks and the
// root adopts. The root and the undo record of every block are compared with
// a serial reference computer that never evicts (so every undo value the
// lagged tree faults through its cold source must match the resident one).

const (
	clAccounts = 3000
	clChanges  = 1100
)

func clDB(t *testing.T) kv.RwDB {
	t.Helper()
	previous := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = previous })
	return memdb.NewTestDB(t)
}

// clBlock is block b's dirty set; sibling picks a different value set.
func clBlock(b int, sibling bool) map[types.Address]*account.StateAccount {
	rng := rand.New(rand.NewSource(int64(b)*7919 + map[bool]int64{false: 0, true: 1}[sibling]))
	out := make(map[types.Address]*account.StateAccount, clChanges)
	for len(out) < clChanges {
		i := rng.Intn(clAccounts)
		nonce := uint64(b)
		if sibling {
			nonce += 100000
		}
		var a *account.StateAccount
		if rng.Intn(23) != 0 { // a few deletes
			a = qmAcct(nonce, uint64(i)*1000+uint64(b))
		}
		out[rlAddr(i)] = a
	}
	return out
}

// clRows buffers a capture's rows (the writer's qmdbFlushRows).
type clRows struct {
	rows []clRow
}

type clRow struct {
	table      string
	key, value []byte
	del        bool
}

func (f *clRows) Put(table string, key, value []byte) error {
	f.rows = append(f.rows, clRow{table: table, key: key, value: value})
	return nil
}

func (f *clRows) Delete(table string, key []byte) error {
	f.rows = append(f.rows, clRow{table: table, key: key, del: true})
	return nil
}

// clWrite is one block's write in flight.
type clWrite struct {
	done chan struct{}
	err  error
}

// clPipe is the live tree with its write pipeline.
type clPipe struct {
	t        *testing.T
	db       kv.RwDB
	rc       *QMDBRootComputer
	lean     bool
	inflight *clWrite
	failNext bool // the next write fails (rolled back)
	skipNext bool // the next write never begins (fails)
	openTxs  []kv.Tx
	applied  atomic.Int64 // the last block whose root was computed
}

func newCLPipe(t *testing.T, lean bool) *clPipe {
	p := &clPipe{t: t, db: clDB(t), rc: NewQMDBRootComputer(), lean: lean}
	p.rc.EnableUndoRecording()
	p.rc.SetEvictLag(0)
	t.Cleanup(func() {
		if p.inflight != nil {
			<-p.inflight.done
		}
		for _, tx := range p.openTxs {
			tx.Rollback()
		}
	})
	return p
}

// importRoot is the import's part up to the root: the cold source, the root.
func (p *clPipe) importRoot(b int, changes map[types.Address]*account.StateAccount) types.Hash {
	tx, err := p.db.BeginRo(context.Background())
	if err != nil {
		p.t.Fatal(err)
	}
	// Like the import's tx, it stays open past the root; closed a block later.
	p.openTxs = append(p.openTxs, tx)
	if len(p.openTxs) > 2 {
		p.openTxs[0].Rollback()
		p.openTxs = p.openTxs[1:]
	}
	if p.lean {
		p.rc.SetColdLean(tx)
	} else {
		p.rc.SetCold(tx)
	}
	root, err := p.rc.ComputeRoot(changes, nil)
	if err != nil {
		p.t.Fatal(err)
	}
	if p.rc.coldNext.Load() != nil {
		p.t.Fatalf("block %d: a parked cold source survived the root", b)
	}
	p.applied.Store(int64(b))
	return root
}

// join waits for the write in flight; true if it succeeded (its cursor is
// adopted). A failed write is recovered as recoverLagWrite does.
func (p *clPipe) join() bool {
	w := p.inflight
	if w == nil {
		return true
	}
	<-w.done
	p.inflight = nil
	if w.err != nil {
		p.rc.AbortFlushed()
		p.rc.TakeUndo()
		if err := p.db.Update(context.Background(), func(tx kv.RwTx) error {
			p.rc.SetCold(tx)
			err := p.rc.LoadFrom(tx)
			p.rc.SetCold(nil)
			return err
		}); err != nil {
			p.t.Fatal(err)
		}
		return false
	}
	p.rc.CommitFlushed()
	return true
}

var errCLWriteFailed = errors.New("injected write failure")

// handoff joins the previous write, takes b's undo and starts its capture
// and write. Returns the undo and whether the previous write succeeded.
func (p *clPipe) handoff() (*qmdb.BlockUndo, bool) {
	ok := p.join()
	if !ok {
		return nil, false
	}
	undo := p.rc.TakeUndo()
	rows := &clRows{}
	capture := p.rc.StartFlushTo(rows, true)
	w := &clWrite{done: make(chan struct{})}
	fail := p.failNext
	p.failNext = false
	p.inflight = w
	go func() {
		defer close(w.done)
		if _, err := capture.Wait(); err != nil {
			w.err = err
			return
		}
		w.err = p.db.Update(context.Background(), func(tx kv.RwTx) error {
			for _, r := range rows.rows {
				var err error
				if r.del {
					err = tx.Delete(r.table, r.key)
				} else {
					err = tx.Put(r.table, r.key, r.value)
				}
				if err != nil {
					return err
				}
			}
			if fail {
				return errCLWriteFailed
			}
			return nil
		})
	}()
	return undo, true
}

// clRef is the serial reference: plain SetCold-free in-RAM tree.
type clRef struct {
	rc    *QMDBRootComputer
	roots []types.Hash
	undos [][]byte
}

func newCLRef() *clRef {
	r := &clRef{rc: NewQMDBRootComputer()}
	r.rc.EnableUndoRecording()
	return r
}

func (r *clRef) apply(t *testing.T, changes map[types.Address]*account.StateAccount) (types.Hash, []byte) {
	root, err := r.rc.ComputeRoot(changes, nil)
	if err != nil {
		t.Fatal(err)
	}
	u := r.rc.TakeUndo()
	return root, u.Marshal()
}

// clReaders runs lookups (each with its own tx, retried through a fresh one
// when evicted, as NewLookupSourceRefreshing does) and lock spans beside
// the pipeline, and checks every value against the states the tree held
// around the read.
func clReaders(t *testing.T, p *clPipe, states []map[types.Address][]byte, stop <-chan struct{}, n int) *sync.WaitGroup {
	var wg sync.WaitGroup
	var bad atomic.Int64
	for w := 0; w < n; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w) + 1))
			for {
				select {
				case <-stop:
					return
				default:
				}
				if w%2 == 1 {
					// A lock span (a non-ahead run's LockReaders): SetColdLean
					// finds the lock held by a reader and parks.
					release := p.rc.LockReaders()
					time.Sleep(200 * time.Microsecond)
					release()
					continue
				}
				i := rng.Intn(clAccounts)
				addr := rlAddr(i)
				kh := qmdb.Hash(AccountKeyHash(addr))
				g0 := p.applied.Load()
				var v []byte
				var found bool
				for try := 0; ; try++ {
					tx, err := p.db.BeginRo(context.Background())
					if err != nil {
						t.Error(err)
						return
					}
					var evicted bool
					v, found, evicted = p.rc.Lookup(kh, tx)
					v = bytes.Clone(v)
					tx.Rollback()
					if !evicted {
						break
					}
					if try > 1000 {
						t.Errorf("account %d: still evicted after %d fresh transactions", i, try)
						return
					}
				}
				g1 := p.applied.Load()
				ok := false
				for j := g0; j <= g1+1 && int(j) < len(states); j++ {
					want, live := states[j][addr]
					if live == found && bytes.Equal(want, v) {
						ok = true
						break
					}
				}
				if !ok && bad.Add(1) <= 5 {
					t.Errorf("account %d: value %x (found %v) matches no state in blocks [%d, %d]", i, v, found, g0, g1+1)
				}
			}
		}(w)
	}
	return &wg
}

// clStates returns the live encoding of every account after each block.
func clStates(blocks int) []map[types.Address][]byte {
	states := make([]map[types.Address][]byte, blocks+1)
	cur := map[types.Address][]byte{}
	states[0] = map[types.Address][]byte{}
	for b := 1; b <= blocks; b++ {
		next := make(map[types.Address][]byte, len(cur))
		for k, v := range cur {
			next[k] = v
		}
		for addr, a := range clBlock(b, false) {
			if a == nil || isAccountEmpty(a) {
				delete(next, addr)
			} else {
				next[addr] = EncodeAccountValue(a)
			}
		}
		states[b], cur = next, next
	}
	return states
}

// runColdLeanModes runs fn under the switch off, on and check.
func runColdLeanModes(t *testing.T, fn func(t *testing.T, lean bool)) {
	for _, mode := range []string{"", "1", "check"} {
		t.Run("mode="+map[string]string{"": "off", "1": "on", "check": "check"}[mode], func(t *testing.T) {
			defer SetImportSetColdLeanForTest(mode)()
			parks0, adopts0 := ColdLeanCounts()
			_, misses0 := ColdLeanCheckCounts()
			fn(t, mode != "")
			parks, adopts := ColdLeanCounts()
			if parks-parks0 != adopts-adopts0 {
				t.Fatalf("parked %d, adopted %d", parks-parks0, adopts-adopts0)
			}
			if _, misses := ColdLeanCheckCounts(); misses != misses0 {
				t.Fatalf("check mode: %d cold sources did not cover the evicted window", misses-misses0)
			}
			t.Logf("parked %d", parks-parks0)
		})
	}
}

// Readers, captures and cold swaps run concurrently; every root and undo
// record equals the serial reference and every lookup a state the tree held.
func TestColdLeanConcurrentPipeline(t *testing.T) {
	const blocks = 14
	states := clStates(blocks)
	runColdLeanModes(t, func(t *testing.T, lean bool) {
		p := newCLPipe(t, lean)
		ref := newCLRef()
		stop := make(chan struct{})
		wg := clReaders(t, p, states, stop, 4)
		defer func() { close(stop); wg.Wait() }()
		check := func(b int, undo *qmdb.BlockUndo, wantUndo []byte) {
			if got := undo.Marshal(); !bytes.Equal(got, wantUndo) {
				t.Fatalf("block %d: undo differs from the reference (%d vs %d bytes)", b, len(got), len(wantUndo))
			}
		}
		refUndos := map[int][]byte{}
		for b := 1; b <= blocks; b++ {
			changes := clBlock(b, false)
			wantRoot, wantUndo := ref.apply(t, changes)
			refUndos[b] = wantUndo
			if got := p.importRoot(b, changes); got != wantRoot {
				t.Fatalf("block %d: root %x, reference %x", b, got[:8], wantRoot[:8])
			}
			undo, ok := p.handoff()
			if !ok {
				t.Fatalf("block %d: write failed", b-1)
			}
			check(b, undo, refUndos[b])
		}
		if !p.join() {
			t.Fatal("last write failed")
		}
		if lean {
			if s := p.rc.LastSetCold(); s.Parked && s.AdoptBy == holderNone {
				t.Fatalf("last parked source has no adopter: %+v", s)
			}
		}
	})
}

// A write fails with the switch on: the tree is reloaded from the store
// (recoverLagWrite's path, which installs its own source over any parked
// one) and the blocks are imported again; the roots still match.
func TestColdLeanFailedWriteReload(t *testing.T) {
	runColdLeanModes(t, func(t *testing.T, lean bool) {
		p := newCLPipe(t, lean)
		ref := newCLRef()
		var refRoots []types.Hash
		refRoots = append(refRoots, ref.rc.Root())
		for b := 1; b <= 8; b++ {
			r, _ := ref.apply(t, clBlock(b, false))
			refRoots = append(refRoots, r)
		}
		const failAt = 5
		for b := 1; b <= 8; b++ {
			if got := p.importRoot(b, clBlock(b, false)); got != refRoots[b] {
				t.Fatalf("block %d: root %x, reference %x", b, got[:8], refRoots[b][:8])
			}
			p.failNext = b == failAt
			if _, ok := p.handoff(); !ok {
				// Write failAt failed: the tree was reloaded to failAt-1
				// (block b on top of it is gone too). Park a source while a
				// reader holds the lock, then re-import from failAt.
				if got := p.rc.Root(); got != refRoots[failAt-1] {
					t.Fatalf("after the reload: root %x, want block %d's %x", got[:8], failAt-1, refRoots[failAt-1][:8])
				}
				for rb := failAt; rb <= b; rb++ {
					if got := p.importRoot(rb, clBlock(rb, false)); got != refRoots[rb] {
						t.Fatalf("re-import %d: root %x, reference %x", rb, got[:8], refRoots[rb][:8])
					}
					if _, ok := p.handoff(); !ok {
						t.Fatalf("re-import %d: write failed again", rb)
					}
				}
			}
		}
		if !p.join() {
			t.Fatal("last write failed")
		}
	})
}

// An unwind with the switch on: a source parked while a reader holds the
// lock is adopted by RevertBlock, which then installs its own tx; the
// reverted tree equals the reference's, and a sibling block on top agrees.
func TestColdLeanUnwindSibling(t *testing.T) {
	runColdLeanModes(t, func(t *testing.T, lean bool) {
		p := newCLPipe(t, lean)
		ref := newCLRef()
		var undos []*qmdb.BlockUndo
		var refUndos []*qmdb.BlockUndo
		for b := 1; b <= 6; b++ {
			changes := clBlock(b, false)
			wantRoot, err := ref.rc.ComputeRoot(changes, nil)
			if err != nil {
				t.Fatal(err)
			}
			refUndos = append(refUndos, ref.rc.TakeUndo())
			if got := p.importRoot(b, changes); got != wantRoot {
				t.Fatalf("block %d: root differs", b)
			}
			undo, ok := p.handoff()
			if !ok {
				t.Fatalf("block %d: write failed", b)
			}
			undos = append(undos, undo)
		}
		if !p.join() {
			t.Fatal("write failed")
		}
		// Park a source behind a reader's span, then unwind the last block.
		ro, err := p.db.BeginRo(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer ro.Rollback()
		if lean {
			release := p.rc.LockReaders()
			parked := p.rc.SetColdLean(ro)
			release()
			if !parked {
				t.Fatal("SetColdLean under a reader's span did not park")
			}
		} else {
			p.rc.SetCold(ro) // off: SetColdLean would wait for this goroutine's own span
		}
		if err := p.db.Update(context.Background(), func(tx kv.RwTx) error {
			return p.rc.RevertBlock(tx, undos[len(undos)-1])
		}); err != nil {
			t.Fatal(err)
		}
		if p.rc.coldNext.Load() != nil {
			t.Fatal("RevertBlock left the parked source")
		}
		if err := ref.rc.ApplyUndo(refUndos[len(refUndos)-1]); err != nil {
			t.Fatal(err)
		}
		if got, want := p.rc.Root(), ref.rc.Root(); got != want {
			t.Fatalf("after the unwind: root %x, reference %x", got[:8], want[:8])
		}
		for b := 6; b <= 8; b++ {
			changes := clBlock(b, b == 6)
			wantRoot, err := ref.rc.ComputeRoot(changes, nil)
			if err != nil {
				t.Fatal(err)
			}
			ref.rc.TakeUndo()
			if got := p.importRoot(b, changes); got != wantRoot {
				t.Fatalf("sibling %d: root %x, reference %x", b, got[:8], wantRoot[:8])
			}
			if _, ok := p.handoff(); !ok {
				t.Fatalf("sibling %d: write failed", b)
			}
		}
		if !p.join() {
			t.Fatal("write failed")
		}
	})
}

// Check mode reports a cold source that does not cover the evicted window
// (a transaction older than the flush whose slots were evicted).
func TestColdLeanCheckDetectsStaleSource(t *testing.T) {
	defer SetImportSetColdLeanForTest("check")()
	p := newCLPipe(t, true)
	var stale kv.Tx
	for b := 1; b <= 4; b++ {
		p.importRoot(b, clBlock(b, false))
		if _, ok := p.handoff(); !ok {
			t.Fatal("write failed")
		}
		if b == 1 {
			// Write 1 committed (joined): a source that sees only block 1's
			// rows.
			if !p.join() {
				t.Fatal("write failed")
			}
			var err error
			if stale, err = p.db.BeginRo(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer stale.Rollback()
		}
	}
	if !p.join() {
		t.Fatal("write failed")
	}
	p.rc.EvictFlushed()
	if p.rc.Tree().EntriesBase() == 0 {
		t.Fatal("nothing evicted")
	}
	// The check panics inside the install, with the tree's lock held: the
	// computer is not used afterwards.
	defer func() {
		if recover() == nil {
			t.Fatal("check mode accepted a source older than the evicted window")
		}
	}()
	p.rc.SetColdLean(stale)
}

// The switch's parse.
func TestParseColdLean(t *testing.T) {
	for v, want := range map[string]uint32{"": coldLeanOff, "0": coldLeanOff, "1": coldLeanOn, "on": coldLeanOn, "check": coldLeanCheck} {
		if got := parseColdLean(v); got != want {
			t.Fatalf("%q: %d, want %d", v, got, want)
		}
	}
	defer SetImportSetColdLeanForTest("")()
	r := NewQMDBRootComputer()
	release := r.LockReaders()
	done := make(chan bool)
	go func() { done <- r.SetColdLean(emptyGetter{}) }()
	select {
	case <-done:
		t.Fatal("off: SetColdLean did not wait for the lock")
	case <-time.After(20 * time.Millisecond):
	}
	release()
	if <-done {
		t.Fatal("off: parked")
	}
}
