// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// njOutcome is what a block leaves after its fold: the finalize digest
// after each block-end call, the root's dirty set, the post-state the root
// publishes, CapturePostState's and CommitBlock's rows.
type njOutcome struct {
	fold, fin1, fin2, rewards string
	rootRows                  []string
	post, captured            *PostState
	rows                      []string
	records                   int // journal records after the fold
}

// njPicks are the fixture's addresses the block end touches.
type njPicks struct {
	credits  []types.Address // credited, unloaded
	prepared []types.Address // written in full, non-empty, prepared
}

func pickNJ(f *bulkFixture) njPicks {
	var p njPicks
	special := map[types.Address]bool{f.live: true, f.pending: true, f.nilAcc: true, f.wiped: true, f.deletedLive: true, ripemd: true}
	for _, e := range f.entries {
		if special[e.Addr] {
			continue
		}
		if _, cached := f.known[e.Addr]; !cached {
			continue
		}
		switch {
		case e.Delta != nil && len(p.credits) < 8:
			p.credits = append(p.credits, e.Addr)
		case e.Delta == nil && !e.Delete && e.Nonce != 0 && len(p.prepared) < 4:
			p.prepared = append(p.prepared, e.Addr)
		}
	}
	if len(p.credits) < 8 || len(p.prepared) < 4 {
		panic("fixture too small")
	}
	return p
}

// runNJBlock folds f's entries (fold: sharded at that many workers, 0 for
// ApplyAccounts) with the no-journal fold on or off, then runs a block end
// shaped like the import's: two system calls, each finalized with the
// no-op writer, touching credited and written accounts the fold left (one
// of them in a reverted call), the system address and an account emptied;
// the rewards; the root with its post-state; CommitBlock.
func runNJBlock(t *testing.T, f *bulkFixture, nj, detach bool, fold int) njOutcome {
	t.Helper()
	p := pickNJ(f)
	rules := &params.Rules{IsSpuriousDragon: true}
	noop := NewNoopWriter()
	ibs, _, _ := f.build()
	ibs.SetDetachedPendingCredits(detach)
	ibs.SetFoldNoJournal(nj)
	if fold > 0 {
		ibs.ApplyAccountsSharded(f.entries, fold)
	} else {
		ibs.ApplyAccounts(f.entries, 8)
	}
	ibs.SetFoldNoJournal(false)
	if nj && detach {
		if ibs.FoldCredits() == 0 {
			t.Fatal("the no-journal fold marked no credit")
		}
	}
	var out njOutcome
	out.records = len(ibs.journal.entries)
	out.fold = fmt.Sprint(len(ibs.stateObjects), len(ibs.balanceInc))

	// The first system call.
	ibs.GetBalance(p.credits[0]) // a credit loaded
	snap := ibs.Snapshot()
	ibs.GetBalance(p.credits[1]) // a credit loaded in a reverted call
	ibs.AddBalance(p.credits[1], uint256.NewInt(5))
	ibs.RevertToSnapshot(snap)
	ibs.AddBalance(p.prepared[0], uint256.NewInt(1)) // a written account
	snap = ibs.Snapshot()
	ibs.SetNonce(p.prepared[1], 99) // reverted
	ibs.RevertToSnapshot(snap)
	ibs.SetBalance(p.prepared[2], new(uint256.Int)) // emptied: deleted
	ibs.SetNonce(p.prepared[2], 0)
	ibs.AddBalance(SystemAddress, new(uint256.Int))
	ibs.GetBalance(SystemAddress)
	if err := ibs.FinalizeTx(rules, noop); err != nil {
		t.Fatal(err)
	}
	out.fin1 = ibs.FinalizeDigestForTest()

	// The second.
	ibs.GetBalance(p.credits[2])
	ibs.AddBalance(p.credits[0], uint256.NewInt(2))
	ibs.CreateAccount(p.credits[3], true) // over a credit
	ibs.SetNonce(p.credits[3], 1)
	ibs.AddBalance(SystemAddress, new(uint256.Int))
	if err := ibs.FinalizeTx(rules, noop); err != nil {
		t.Fatal(err)
	}
	out.fin2 = ibs.FinalizeDigestForTest()

	// The rewards, without a finalize.
	ibs.AddBalance(types.Address{0xc0, 0xff, 0xee}, uint256.NewInt(2))
	ibs.AddBalance(p.credits[4], uint256.NewInt(3))
	ibs.GetBalance(p.credits[5])
	ibs.AddBalance(p.prepared[3], uint256.NewInt(4))
	out.rewards = ibs.FinalizeDigestForTest()

	rc := &capturingRootComputer{}
	ibs.SetRootComputer(rc)
	ibs.SetRootPostStateHook(func(ps *PostState) { out.post = ps })
	ibs.IntermediateRoot()
	for a, acc := range rc.accts {
		if acc == nil {
			out.rootRows = append(out.rootRows, fmt.Sprintf("%x del", a))
		} else {
			out.rootRows = append(out.rootRows, fmt.Sprintf("%x %d/%s %x", a, acc.Nonce, acc.Balance.String(), acc.CodeHash))
		}
	}
	sort.Strings(out.rootRows)
	out.captured = CapturePostState(ibs)
	w := &recordingWriter{}
	if err := ibs.CommitBlock(rules, w); err != nil {
		t.Fatal(err)
	}
	out.rows = w.log
	return out
}

func compareNJ(t *testing.T, tag string, want, got njOutcome) {
	t.Helper()
	check := func(what string, w, g any) {
		if !reflect.DeepEqual(w, g) {
			t.Fatalf("%s: %s differs\nwant %v\n got %v", tag, what, w, g)
		}
	}
	check("fold", want.fold, got.fold)
	check("first finalize", want.fin1, got.fin1)
	check("second finalize", want.fin2, got.fin2)
	check("rewards", want.rewards, got.rewards)
	check("root rows", want.rootRows, got.rootRows)
	check("post-state", want.post, got.post)
	check("captured post-state", want.captured, got.captured)
	check("commit rows", want.rows, got.rows)
}

// The no-journal fold leaves, after every block-end step, the same
// objects, flags, stateObjectsDirty, balance increases and journal dirty
// set (FinalizeDigestForTest), the same root dirty set, post-states and
// committed rows as the journaled fold: ApplyAccounts and the sharded
// pass, credits detached or not (a state that does not keep its pending
// credits takes the old code on the spilled dirty set), the fast finalize
// on and off, the system calls finalized per call or once with the
// parallel walk.
func TestFoldNoJournalMatchesJournaled(t *testing.T) {
	fixtures := map[string]*bulkFixture{
		"bulk":       newBulkFixture(6000),
		"long-runs":  newRandomShardFixture(7, 20000, 0.0002),
		"no-serial":  newRandomShardFixture(8, 9000, 0),
		"many-short": newRandomShardFixture(9, 9000, 0.002),
	}
	for name, f := range fixtures {
		for _, detach := range []bool{true, false} {
			for _, fast := range []bool{true, false} {
				for _, once := range []bool{false, true} {
					for _, fold := range []int{0, 4, 16} {
						tag := fmt.Sprintf("%s detach=%v fast=%v once=%v fold=%d", name, detach, fast, once, fold)
						func() {
							defer SetFinalizeFastForTest(fast)()
							defer SetSyscallFinalizeOnceForTest(once)()
							if once {
								defer SetSyscallFinalizeParMinForTest(1)()
							}
							want := runNJBlock(t, f, false, detach, fold)
							got := runNJBlock(t, f, true, detach, fold)
							compareNJ(t, tag, want, got)
							if detach && got.records >= want.records/2 {
								t.Fatalf("%s: the no-journal fold left %d journal records, the journaled %d", tag, got.records, want.records)
							}
						}()
					}
				}
			}
		}
	}
}

// A non-keep-pending finalize (a real writer) after the no-journal fold
// takes the old code on the spilled dirty set: the same rows reach the
// writer, in the same order.
func TestFoldNoJournalRealWriterFinalize(t *testing.T) {
	f := newRandomShardFixture(11, 9000, 0.0002)
	run := func(nj bool) ([]string, string) {
		ibs, _, _ := f.build()
		ibs.SetDetachedPendingCredits(true)
		ibs.SetFoldNoJournal(nj)
		ibs.ApplyAccountsSharded(f.entries, 8)
		ibs.SetFoldNoJournal(false)
		w := &recordingWriter{}
		if err := ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, w); err != nil {
			t.Fatal(err)
		}
		return w.log, ibs.FinalizeDigestForTest()
	}
	wantRows, wantFin := run(false)
	gotRows, gotFin := run(true)
	if !reflect.DeepEqual(wantRows, gotRows) {
		t.Fatalf("writer rows differ (%d vs %d)", len(gotRows), len(wantRows))
	}
	if wantFin != gotFin {
		t.Fatal("finalize digests differ")
	}
}

// SoftFinalise and a journal reset with marked credits: the credits leave
// the dirty set as they leave the journal.
func TestFoldNoJournalSoftFinalise(t *testing.T) {
	f := newRandomShardFixture(12, 9000, 0)
	run := func(nj bool) (string, []string) {
		ibs, _, _ := f.build()
		ibs.SetDetachedPendingCredits(true)
		ibs.SetFoldNoJournal(nj)
		ibs.ApplyAccountsSharded(f.entries, 8)
		ibs.SetFoldNoJournal(false)
		ibs.SoftFinalise()
		fin := ibs.FinalizeDigestForTest()
		w := &recordingWriter{}
		if err := ibs.CommitBlock(&params.Rules{IsSpuriousDragon: true}, w); err != nil {
			t.Fatal(err)
		}
		return fin, w.log
	}
	wf, wr := run(false)
	gf, gr := run(true)
	if wf != gf || !reflect.DeepEqual(wr, gr) {
		t.Fatal("SoftFinalise after the no-journal fold differs")
	}
}

// Reset clears the fold's marks: a state reused for the next block starts
// with none.
func TestFoldNoJournalReset(t *testing.T) {
	f := newRandomShardFixture(13, 5000, 0)
	ibs, _, _ := f.build()
	ibs.SetDetachedPendingCredits(true)
	ibs.SetFoldNoJournal(true)
	ibs.ApplyAccountsSharded(f.entries, 8)
	if ibs.FoldCredits() == 0 {
		t.Fatal("no credit marked")
	}
	ibs.Reset()
	if ibs.FoldCredits() != 0 || len(ibs.foldTouched) != 0 || ibs.FoldNoJournal() {
		t.Fatal("Reset left the no-journal fold's state")
	}
}
