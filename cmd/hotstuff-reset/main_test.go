package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func init() {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
}

func TestValidateApplyFlags(t *testing.T) {
	if err := validateApplyFlags(false, false, ""); err != nil {
		t.Fatalf("dry run rejected: %v", err)
	}
	if err := validateApplyFlags(true, false, "backup.bin"); err == nil {
		t.Fatal("apply without force accepted")
	}
	if err := validateApplyFlags(true, true, ""); err == nil {
		t.Fatal("apply without backup accepted")
	}
	if err := validateApplyFlags(true, true, "backup.bin"); err != nil {
		t.Fatalf("fully acknowledged apply rejected: %v", err)
	}
}

func TestWriteExclusiveBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hotstuff-state.bin")
	want := []byte{0x01, 0x02, 0x03}
	if err := writeExclusiveBackup(path, want); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("backup bytes = %x, want %x", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits: os.OpenFile(..., 0600) reports 0666
	// back, so assert the mode only where it is actually enforced. The write
	// itself still asks for 0600 on every platform.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o, want 600", info.Mode().Perm())
	}
	if err := writeExclusiveBackup(path, []byte{0xFF}); err == nil {
		t.Fatal("existing backup was overwritten")
	}
	got, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("existing backup changed: %x", got)
	}
}

func s131State() *hotstuff.ConsensusState {
	return &hotstuff.ConsensusState{
		View:                25760,
		ConsecutiveTimeouts: 7,
		LockedQC:            hotstuff.QuorumCertificate{View: 25755, BlockHash: types.Hash{0xbb}},
		LastCommittedQC:     hotstuff.QuorumCertificate{View: 25750, BlockHash: types.Hash{0xf3, 0x43}},
		LastVotedView:       25759,
		LastVotedHash:       types.Hash{0xcc},
		LastCommitVotedView: 25758,
		LastCommitVotedHash: types.Hash{0xdd},
	}
}

// TestApplyResetKeepsTheCommittedFloor: the default reset keeps the committed
// QC -- the record a full wipe destroyed in round S131 -- and lowers the
// lock, both votes and the view to it (S132: votes and a lock on a
// speculative block survived a lock-only reset and wedged the fleet).
func TestApplyResetKeepsTheCommittedFloor(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()
	st := s131State()
	if err := db.Update(ctx, func(tx kv.RwTx) error { return hotstuff.SaveConsensusState(tx, st) }); err != nil {
		t.Fatal(err)
	}
	if err := applyReset(ctx, db, st, false); err != nil {
		t.Fatalf("applyReset: %v", err)
	}
	var got *hotstuff.ConsensusState
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		got, err = hotstuff.LoadConsensusState(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("floor record missing after reset")
	}
	qc := st.LastCommittedQC
	if got.View != qc.View+1 || got.LastVotedView != qc.View || got.LastVotedHash != qc.BlockHash ||
		got.LastCommitVotedView != qc.View || got.LastCommitVotedHash != qc.BlockHash {
		t.Fatalf("view/votes not lowered to the committed QC: %+v", got)
	}
	if got.LastCommittedQC.View != 25750 || got.LastCommittedQC.BlockHash != st.LastCommittedQC.BlockHash {
		t.Fatalf("committed QC not kept: %+v", got.LastCommittedQC)
	}
	if got.LockedQC.View != 25750 || got.LockedQC.BlockHash != st.LastCommittedQC.BlockHash {
		t.Fatalf("lock not lowered to the committed QC: %+v", got.LockedQC)
	}
	if got.ConsecutiveTimeouts != 0 {
		t.Fatalf("timeouts = %d, want 0", got.ConsecutiveTimeouts)
	}
}

func TestApplyResetForceWipeDeletes(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()
	st := s131State()
	if err := db.Update(ctx, func(tx kv.RwTx) error { return hotstuff.SaveConsensusState(tx, st) }); err != nil {
		t.Fatal(err)
	}
	if err := applyReset(ctx, db, st, true); err != nil {
		t.Fatalf("applyReset(wipe): %v", err)
	}
	raw, got, _, _, _, _, err := readDatadir(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 0 || got != nil {
		t.Fatalf("record survived -force-wipe: %d bytes", len(raw))
	}
}

// TestPrintLineAndCommittedQCCanonical: -print reports the QC marker and the
// canonical head, and a committed QC is only kept when it is canonical.
func TestPrintLineAndCommittedQCCanonical(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()
	parent := &block.Header{Number: uint256.NewInt(14131246), Difficulty: uint256.NewInt(0)}
	committed := &block.Header{ParentHash: parent.Hash(), Number: uint256.NewInt(14131247), Difficulty: uint256.NewInt(0)}
	st := s131State()
	st.LastCommittedQC.BlockHash = committed.Hash()
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, parent)
		rawdb.WriteHeader(tx, committed)
		if err := rawdb.WriteCanonicalHash(tx, parent.Hash(), 14131246); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, parent.Hash())
		if err := rawdb.WriteHotStuffCommittedHead(tx, parent.Hash()); err != nil {
			return err
		}
		return hotstuff.SaveConsensusState(tx, st)
	}); err != nil {
		t.Fatal(err)
	}
	_, got, c, h, qcCanonical, qcAhead, err := readDatadir(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if qcCanonical {
		t.Fatal("a committed QC whose block is not canonical was reported canonical")
	}
	// R52: the committed QC is the canonical head's child -- committed, but
	// its lagged write was not durable at shutdown. Its floor is kept.
	if !qcAhead {
		t.Fatal("a committed QC one block above the canonical head on its lineage was not kept as a floor")
	}
	line := printLine("/d/node0", c, h, got)
	want := "datadir=/d/node0 committed_height=14131246 committed_hash=" + parent.Hash().Hex() +
		" canonical_height=14131246 canonical_hash=" + parent.Hash().Hex() +
		" hs_committed_qc_view=25750 hs_committed_qc_hash=" + committed.Hash().Hex()
	if line != want {
		t.Fatalf("print line\n got %s\nwant %s", line, want)
	}
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return rawdb.WriteCanonicalHash(tx, committed.Hash(), 14131247)
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, qcCanonical, _, err = readDatadir(ctx, db); err != nil || !qcCanonical {
		t.Fatalf("canonical committed QC rejected: %v %v", qcCanonical, err)
	}
	empty := printLine("/d/x", chainPoint{}, chainPoint{}, nil)
	if empty != "datadir=/d/x committed_height=- committed_hash="+(types.Hash{}).Hex()+
		" canonical_height=- canonical_hash="+(types.Hash{}).Hex()+" hs_committed_qc_view=0 hs_committed_qc_hash="+(types.Hash{}).Hex() {
		t.Fatalf("empty print line: %s", empty)
	}
}

// TestCommittedQCOnAnotherBranchIsRefused: a committed QC above the
// canonical head whose lineage does not reach it (a sibling of the head's
// child chain) is neither canonical nor ahead, so no floor is kept.
func TestCommittedQCOnAnotherBranchIsRefused(t *testing.T) {
	db := memdb.NewTestDB(t)
	ctx := context.Background()
	head := &block.Header{Number: uint256.NewInt(100), Difficulty: uint256.NewInt(0), Extra: []byte{1}}
	sibling := &block.Header{Number: uint256.NewInt(100), Difficulty: uint256.NewInt(0), Extra: []byte{2}}
	child := &block.Header{ParentHash: sibling.Hash(), Number: uint256.NewInt(101), Difficulty: uint256.NewInt(0)}
	st := s131State()
	st.LastCommittedQC.BlockHash = child.Hash()
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, head)
		rawdb.WriteHeader(tx, sibling)
		rawdb.WriteHeader(tx, child)
		if err := rawdb.WriteCanonicalHash(tx, head.Hash(), 100); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, head.Hash())
		return hotstuff.SaveConsensusState(tx, st)
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, qcCanonical, qcAhead, err := readDatadir(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if qcCanonical || qcAhead {
		t.Fatalf("committed QC on another branch kept as a floor: canonical=%v ahead=%v", qcCanonical, qcAhead)
	}
}

// TestS132ResetClearsTheVoteJournal: the S132 B3 state -- view 46713, lock
// and both votes at 46713 on the speculative 14152208, committed QC 46712 --
// with the same record in the separate vote journal. After the reset and
// the journal clear, the record recovery builds (chain record merged with
// the journal, as mergeVoteJournal does) sits at the committed QC; with the
// journal left in place the merge raised the lock to 46713 again.
func TestS132ResetClearsTheVoteJournal(t *testing.T) {
	ctx := context.Background()
	committed := types.Hash{0xec, 0x31}
	spec := types.Hash{0x39, 0xe2}
	st := &hotstuff.ConsensusState{
		View:                46713,
		LockedQC:            hotstuff.QuorumCertificate{View: 46713, BlockHash: spec},
		LastCommittedQC:     hotstuff.QuorumCertificate{View: 46712, BlockHash: committed},
		LastVotedView:       46713,
		LastVotedHash:       spec,
		LastCommitVotedView: 46713,
		LastCommitVotedHash: spec,
	}
	chain := memdb.NewTestDB(t)
	journal := memdb.NewTestDB(t)
	for _, db := range []kv.RwDB{chain, journal} {
		if err := db.Update(ctx, func(tx kv.RwTx) error { return hotstuff.SaveConsensusState(tx, st) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyReset(ctx, chain, st, false); err != nil {
		t.Fatal(err)
	}
	load := func(db kv.RwDB) *hotstuff.ConsensusState {
		var got *hotstuff.ConsensusState
		if err := db.View(ctx, func(tx kv.Tx) error {
			var err error
			got, err = hotstuff.LoadConsensusState(tx)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	// Left in place, the journal undoes the reset.
	if merged, err := hotstuff.MergeConsensusStates(load(chain), load(journal)); err != nil {
		t.Fatal(err)
	} else if merged.LockedQC.View != 46713 {
		t.Fatalf("expected the stale journal to raise the lock, got %d", merged.LockedQC.View)
	}
	raw, err := readJournal(ctx, journal)
	if err != nil || len(raw) == 0 {
		t.Fatalf("journal record: %d bytes, %v", len(raw), err)
	}
	if err := clearJournal(ctx, journal); err != nil {
		t.Fatal(err)
	}
	if j := load(journal); j != nil {
		t.Fatalf("journal record survived the clear: %+v", j)
	}
	got := load(chain)
	if got.View != 46713 || got.LockedQC.View != 46712 || got.LockedQC.BlockHash != committed ||
		got.LastVotedView != 46712 || got.LastCommitVotedView != 46712 || got.LastCommittedQC.BlockHash != committed {
		t.Fatalf("recovered record not at the committed floor: %+v", got)
	}
	if p := defaultJournalPath("/data/blockchain/qs-node1/chaindata/"); p != "/data/blockchain/qs-node1/hotstuff-journal" {
		t.Fatalf("default journal path %s", p)
	}
}
