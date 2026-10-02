package qmdb

import "testing"

// TestBeginEndBlockGuards covers the error/no-op branches around the
// history-recorder block lifecycle: no-ops when no recorder is attached,
// rejection of block 0 and overflowing block numbers, and EndBlock without a
// matching BeginBlock.
func TestBeginEndBlockGuards(t *testing.T) {
	tr := New()

	// No recorder attached: both are silent no-ops.
	if err := tr.BeginBlock(1); err != nil {
		t.Fatalf("BeginBlock without a recorder: %v", err)
	}
	if err := tr.EndBlock(); err != nil {
		t.Fatalf("EndBlock without a recorder: %v", err)
	}

	store := newMapHistoryStore()
	tr.SetHistoryRecorder(NewHistoryRecorder(store))

	if err := tr.BeginBlock(0); err == nil {
		t.Fatalf("BeginBlock(0) accepted (block 0 is reserved)")
	}
	if err := tr.BeginBlock(uint64(^uint32(0)) + 1); err == nil {
		t.Fatalf("BeginBlock accepted a block number beyond uint32 range")
	}

	// EndBlock without a prior successful BeginBlock.
	if err := tr.EndBlock(); err == nil {
		t.Fatalf("EndBlock without BeginBlock accepted")
	}

	// Normal lifecycle succeeds and PutBlockPos/PutTopBand both land.
	if err := tr.BeginBlock(1); err != nil {
		t.Fatalf("BeginBlock(1): %v", err)
	}
	tr.Set(key(1), val(1))
	if err := tr.EndBlock(); err != nil {
		t.Fatalf("EndBlock: %v", err)
	}
	if _, ok := store.blockPos[1]; !ok {
		t.Fatalf("EndBlock did not record a block position")
	}
	if _, ok := store.topBand[1]; !ok {
		t.Fatalf("EndBlock did not record a top band")
	}

	// A second EndBlock without an intervening BeginBlock must fail again (the
	// recorder's block number resets to 0 after a successful EndBlock).
	if err := tr.EndBlock(); err == nil {
		t.Fatalf("EndBlock accepted twice without a BeginBlock in between")
	}
}

// TestFlushHistoryPersistsAccumulatedDeathStamps drives several blocks of
// deletes/overwrites (which call recordDeath and accumulate into
// HistoryRecorder.stampDelta), then calls FlushHistory and checks the death
// stamps actually landed in the store and the pending delta cleared — the
// batched write path EndBlock itself never exercises.
func TestFlushHistoryPersistsAccumulatedDeathStamps(t *testing.T) {
	tr := New()
	store := newMapHistoryStore()
	hist := NewHistoryRecorder(store)
	tr.SetHistoryRecorder(hist)

	for block := uint64(1); block <= 3; block++ {
		if err := tr.BeginBlock(block); err != nil {
			t.Fatalf("BeginBlock(%d): %v", block, err)
		}
		for i := uint64(0); i < 10; i++ {
			tr.Set(key(i), val(i*10+block))
		}
		if block > 1 {
			tr.Delete(key(0)) // only meaningful once key 0 already exists
		}
		if err := tr.EndBlock(); err != nil {
			t.Fatalf("EndBlock(%d): %v", block, err)
		}
	}
	if len(hist.stampDelta) == 0 {
		t.Fatalf("expected pending death-stamp deltas before FlushHistory")
	}
	if err := tr.FlushHistory(); err != nil {
		t.Fatalf("FlushHistory: %v", err)
	}
	if len(hist.stampDelta) != 0 {
		t.Fatalf("FlushHistory left %d twigs with pending deltas", len(hist.stampDelta))
	}
	stamps, err := store.GetDeathStamps(0)
	if err != nil {
		t.Fatalf("GetDeathStamps: %v", err)
	}
	if len(stamps) == 0 {
		t.Fatalf("FlushHistory did not persist any death-stamp bytes")
	}

	// FlushHistory on a tree with no recorder is a no-op, not an error.
	plain := New()
	if err := plain.FlushHistory(); err != nil {
		t.Fatalf("FlushHistory without a recorder: %v", err)
	}

	// A second FlushHistory with nothing new pending must also succeed.
	if err := tr.FlushHistory(); err != nil {
		t.Fatalf("FlushHistory with nothing pending: %v", err)
	}
}

// TestFlushHistoryMergesWithExistingBlob covers the read-modify-write branch
// where a twig already has a persisted death-stamp blob of the correct size
// (TwigSize*4) and a second batch's deltas must merge into it, not replace it
// — exercising the `len(blob) == TwigSize*4` copy branch distinctly from the
// "no prior blob" allocation branch.
func TestFlushHistoryMergesWithExistingBlob(t *testing.T) {
	tr := New()
	store := newMapHistoryStore()
	tr.SetHistoryRecorder(NewHistoryRecorder(store))

	if err := tr.BeginBlock(1); err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 5; i++ {
		tr.Set(key(i), val(i))
	}
	tr.Set(key(0), val(100)) // overwrite stamps the original slot 0 as dead
	if err := tr.EndBlock(); err != nil {
		t.Fatal(err)
	}
	if err := tr.FlushHistory(); err != nil {
		t.Fatal(err)
	}
	firstBlob, _ := store.GetDeathStamps(0)
	if len(firstBlob) != TwigSize*4 {
		t.Fatalf("first flush blob size = %d, want %d", len(firstBlob), TwigSize*4)
	}

	if err := tr.BeginBlock(2); err != nil {
		t.Fatal(err)
	}
	tr.Delete(key(1)) // stamps slot 1's twig, which already has a persisted blob
	if err := tr.EndBlock(); err != nil {
		t.Fatal(err)
	}
	if err := tr.FlushHistory(); err != nil {
		t.Fatal(err)
	}
	secondBlob, _ := store.GetDeathStamps(0)
	if len(secondBlob) != TwigSize*4 {
		t.Fatalf("second flush blob size = %d, want %d", len(secondBlob), TwigSize*4)
	}
	// Slot 0's stamp from the merge must be unaffected by the key-1 deletion.
	if string(secondBlob[:4]) != string(firstBlob[:4]) {
		t.Fatalf("merge corrupted an untouched slot's death stamp")
	}
}
