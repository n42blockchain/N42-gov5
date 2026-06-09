package qmdb

import "testing"

// TestCompactionReclaimsSpace drives heavy overwrite churn (which leaves many
// obsolete entries in old twigs), then compacts and checks that the footprint
// shrinks toward the live set while the live state and its proofs are preserved.
func TestCompactionReclaimsSpace(t *testing.T) {
	tr := New()
	const nKeys = 8000
	// Insert nKeys, then overwrite each ~5x → ~6x as many entries as live keys,
	// so old twigs become sparse with obsolete entries.
	for i := uint64(0); i < nKeys; i++ {
		tr.Set(key(i), val(i))
	}
	for round := uint64(0); round < 5; round++ {
		for i := uint64(0); i < nKeys; i++ {
			tr.Set(key(i), val(i*100+round))
		}
	}

	before := tr.Stats()
	rootBefore := tr.Root()
	if before.LiveEntries != nKeys {
		t.Fatalf("expected %d live, got %d", nKeys, before.LiveEntries)
	}
	// Stored slots should be much larger than live (obsolete accumulated).
	if before.StoredSlots < before.LiveEntries*3 {
		t.Fatalf("expected heavy obsolete accumulation, stored=%d live=%d", before.StoredSlots, before.LiveEntries)
	}

	pruned := tr.Compact(0.25)
	after := tr.Stats()
	rootAfter := tr.Root()

	t.Logf("before: twigs=%d stored=%d live=%d | after: twigs=%d pruned=%d stored=%d live=%d | prunedNow=%d",
		before.Twigs, before.StoredSlots, before.LiveEntries,
		after.Twigs, after.PrunedTwigs, after.StoredSlots, after.LiveEntries, pruned)

	if pruned == 0 || after.PrunedTwigs == 0 {
		t.Fatal("expected compaction to prune sparse twigs")
	}
	// Footprint (resident entry records) must drop materially.
	if after.StoredSlots >= before.StoredSlots {
		t.Fatalf("compaction did not reduce stored slots: %d -> %d", before.StoredSlots, after.StoredSlots)
	}
	// Live set is unchanged.
	if after.LiveEntries != nKeys {
		t.Fatalf("compaction lost/added live keys: %d", after.LiveEntries)
	}
	// In this all-overwrite workload the old twigs were FULLY dead (every slot
	// deactivated), so pruning them is ROOT-PRESERVING — a fully-dead twig already
	// contributes nullTwigRoot. (Only moving live entries out of a sparse-but-live
	// twig changes the root; see TestCompactionMovesLiveChangesRoot.)
	if rootAfter != rootBefore {
		t.Fatalf("pruning fully-dead twigs should preserve the root: %x -> %x", rootBefore, rootAfter)
	}

	// Every live key still resolves and proves against the NEW root.
	for i := uint64(0); i < nKeys; i++ {
		v, ok := tr.Get(key(i))
		if !ok {
			t.Fatalf("key %d lost after compaction", i)
		}
		want := val(i*100 + 4) // last round
		if string(v) != string(want) {
			t.Fatalf("key %d wrong value after compaction", i)
		}
		if i%200 == 0 {
			p, _ := tr.GetProof(key(i))
			if !VerifyProof(rootAfter, p) {
				t.Fatalf("key %d proof failed against post-compaction root", i)
			}
		}
	}
}

// TestCompactionMovesLiveChangesRoot: when a sparse twig still holds LIVE
// entries, compaction re-appends them to new slots, which changes the world root
// (the price of physical-layout-dependent roots). The live state is preserved.
func TestCompactionMovesLiveChangesRoot(t *testing.T) {
	tr := New()
	// Fill 2 twigs; then delete most of twig 0 so it is sparse-but-live, while
	// keeping it non-active (twig 1 is active).
	for i := uint64(0); i < TwigSize+50; i++ {
		tr.Set(key(i), val(i))
	}
	// Delete most of twig 0 (slots 0..2047), leaving a few live -> sparse, live>0.
	for i := uint64(0); i < TwigSize-10; i++ {
		tr.Delete(key(i))
	}
	rootBefore := tr.Root()
	live := tr.LiveCount()
	pruned := tr.Compact(0.25)
	rootAfter := tr.Root()
	if pruned == 0 {
		t.Fatal("expected the sparse-live twig to be compacted+pruned")
	}
	if rootAfter == rootBefore {
		t.Fatal("expected root to change when live entries are moved")
	}
	if tr.LiveCount() != live {
		t.Fatalf("live count changed: %d -> %d", live, tr.LiveCount())
	}
	// surviving keys still resolve + prove
	for i := uint64(TwigSize - 10); i < uint64(TwigSize)+50; i++ {
		if _, ok := tr.Get(key(i)); !ok {
			t.Fatalf("key %d lost", i)
		}
		p, _ := tr.GetProof(key(i))
		if !VerifyProof(rootAfter, p) {
			t.Fatalf("key %d proof failed post-compaction", i)
		}
	}
}

// TestCompactionDeterministic: compaction is reproducible (same tree state +
// same threshold -> same post-compaction root), which is required for cross-node
// agreement.
func TestCompactionDeterministic(t *testing.T) {
	build := func() Hash {
		tr := New()
		for i := uint64(0); i < 5000; i++ {
			tr.Set(key(i), val(i))
		}
		for i := uint64(0); i < 5000; i += 2 {
			tr.Delete(key(i)) // create sparse twigs
		}
		tr.Compact(0.25)
		return tr.Root()
	}
	if build() != build() {
		t.Fatal("compaction is non-deterministic")
	}
}
