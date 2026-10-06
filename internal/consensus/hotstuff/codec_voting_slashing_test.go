package hotstuff

import (
	"bytes"
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestDecodeQC_ExportedWrapper covers the exported DecodeQC entry point
// (used by the cross-chain bridge) via an encode/decode round trip, plus its
// error passthrough for malformed input.
func TestDecodeQC_ExportedWrapper(t *testing.T) {
	qc := &QuorumCertificate{
		View:               9,
		BlockHash:          types.Hash{0x44},
		AggregateSignature: bytes.Repeat([]byte{0x77}, 96),
		Signers:            []bool{true, false, true},
	}
	data, err := encodeQC(qc)
	if err != nil {
		t.Fatalf("encodeQC: %v", err)
	}
	got, err := DecodeQC(data)
	if err != nil {
		t.Fatalf("DecodeQC: %v", err)
	}
	if got.View != qc.View || got.BlockHash != qc.BlockHash || !bytes.Equal(got.AggregateSignature, qc.AggregateSignature) {
		t.Fatalf("DecodeQC round trip mismatch: %+v vs %+v", got, qc)
	}

	if _, err := DecodeQC([]byte("not ssz")); err == nil {
		t.Fatalf("expected error decoding malformed QC bytes")
	}
}

// TestVoteCollectorCount_NilSafe covers the nil-collector and populated
// branches of prepareVoteCollectorCount / commitVoteCollectorCount.
func TestVoteCollectorCount_NilSafe(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	if n := engine.prepareVoteCollectorCount(); n != 0 {
		t.Fatalf("expected 0 with nil voteCollector, got %d", n)
	}
	if n := engine.commitVoteCollectorCount(); n != 0 {
		t.Fatalf("expected 0 with nil commitCollector, got %d", n)
	}

	engine.voteCollector = NewVoteCollector(1, types.Hash{0x01}, 4)
	_ = engine.voteCollector.AddVote(0, nil)
	if n := engine.prepareVoteCollectorCount(); n != 1 {
		t.Fatalf("expected 1 after adding a vote, got %d", n)
	}

	engine.commitCollector = NewVoteCollector(1, types.Hash{0x01}, 4)
	_ = engine.commitCollector.AddVote(0, nil)
	if n := engine.commitVoteCollectorCount(); n != 1 {
		t.Fatalf("expected 1 after adding a commit vote, got %d", n)
	}
}

// TestSlashing_LoadClearProcess exercises the full equivocation-evidence
// lifecycle: save (via persistence.go), load, process (slash + clear), and
// confirm a second pass finds no more evidence.
func TestSlashing_LoadClearProcess(t *testing.T) {
	db := memdb.NewTestDB(t)
	setup := newTestSetup(t, 4)

	prev := types.Hash{0x01}
	next := types.Hash{0x02}
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveEquivocationEvidence(tx, ViewNumber(4), ValidatorIndex(1), prev, next)
	}); err != nil {
		t.Fatalf("SaveEquivocationEvidence: %v", err)
	}

	var loaded []EquivocationRecord
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadEquivocationEvidence(tx)
		return err
	}); err != nil {
		t.Fatalf("LoadEquivocationEvidence: %v", err)
	}
	if len(loaded) != 1 || loaded[0].View != 4 || loaded[0].Validator != 1 {
		t.Fatalf("unexpected loaded evidence: %+v", loaded)
	}
	if loaded[0].PrevHash != prev || loaded[0].NewHash != next {
		t.Fatalf("mismatched hashes in loaded evidence: %+v", loaded[0])
	}

	se := NewSlashingExecutor(db, func() *ValidatorSet { return setup.vs })
	n, err := se.ProcessPendingSlashing(context.Background())
	if err != nil {
		t.Fatalf("ProcessPendingSlashing: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 slashed validator, got %d", n)
	}

	// Evidence is cleared after processing.
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		remaining, err := LoadEquivocationEvidence(tx)
		if err != nil {
			return err
		}
		if len(remaining) != 0 {
			t.Fatalf("expected no remaining evidence, got %+v", remaining)
		}
		return nil
	}); err != nil {
		t.Fatalf("read back: %v", err)
	}

	// A nil validator-set function surfaces as an error rather than a panic.
	seNoSet := NewSlashingExecutor(db, func() *ValidatorSet { return nil })
	if _, err := seNoSet.ProcessPendingSlashing(context.Background()); err == nil {
		t.Fatalf("expected error when validator set is unavailable")
	}
}

// TestClearEquivocationEvidence_Idempotent covers deleting evidence that was
// never written (and deleting it twice) without error.
func TestClearEquivocationEvidence_Idempotent(t *testing.T) {
	db := memdb.NewTestDB(t)
	do := func() error {
		return db.Update(context.Background(), func(tx kv.RwTx) error {
			return ClearEquivocationEvidence(tx, ViewNumber(1), ValidatorIndex(2))
		})
	}
	if err := do(); err != nil {
		t.Fatalf("first clear: %v", err)
	}
	if err := do(); err != nil {
		t.Fatalf("second clear: %v", err)
	}
}
