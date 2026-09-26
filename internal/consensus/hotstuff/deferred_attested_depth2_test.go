// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestDeferredAttestedGrandparentWalk is S55's own core hotstuff-side
// regression (docs/QS_BLOCK_TIME_BUDGET.md 6f7): under depth-2,
// deferredAttested must be satisfied by the GRANDPARENT's import, not the
// parent's -- CheckDeferredBlock (internal package) resolves the correct
// ancestor and hands it back as ReferenceHash on EventBlockChecked; this
// proves the engine actually uses it, by importing ONLY the grandparent
// (never the parent) and requiring deferredAttested to still fire.
func TestDeferredAttestedGrandparentWalk(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)

	blockHash := types.Hash{0x01}
	parent := types.Hash{0x02}      // never imported in this test
	grandparent := types.Hash{0x03} // imported below

	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockChecked, Hash: blockHash, ParentHash: parent, ReferenceHash: grandparent}); err != nil {
		t.Fatalf("EventBlockChecked: %v", err)
	}
	if e.deferredAttested(blockHash) {
		t.Fatal("must not be attested before the grandparent is imported")
	}
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: grandparent, ParentHash: types.Hash{0x04}}); err != nil {
		t.Fatalf("EventBlockImported: %v", err)
	}
	if !e.deferredAttested(blockHash) {
		t.Fatal("must be attested once the grandparent is imported")
	}
	if e.importedBlocks[parent] {
		t.Fatal("test setup error: the parent must never be imported here -- proving the grandparent, not the parent, is what satisfied attestation")
	}
}

// TestDeferredAttestedDepth1Unchanged: a zero ReferenceHash (depth-1, or a
// pre-fork block) must fall back to importedParents exactly as before this
// existed -- importing the grandparent alone must NOT be enough.
func TestDeferredAttestedDepth1Unchanged(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)

	blockHash := types.Hash{0x11}
	parent := types.Hash{0x12}
	unrelated := types.Hash{0x13}

	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockChecked, Hash: blockHash, ParentHash: parent}); err != nil {
		t.Fatalf("EventBlockChecked: %v", err)
	}
	// Import something else entirely: must not satisfy depth-1's own parent
	// requirement.
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: unrelated, ParentHash: types.Hash{0x14}}); err != nil {
		t.Fatalf("EventBlockImported: %v", err)
	}
	if e.deferredAttested(blockHash) {
		t.Fatal("must not be attested: the parent (not an unrelated block) must be imported under depth-1")
	}
	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockImported, Hash: parent, ParentHash: types.Hash{0x15}}); err != nil {
		t.Fatalf("EventBlockImported: %v", err)
	}
	if !e.deferredAttested(blockHash) {
		t.Fatal("must be attested once the parent is imported (depth-1, unchanged)")
	}
}

// TestExtendsJustifyUnaffectedByCheckedReference (S26/S34 guard, unchanged):
// importedParents -- what extendsJustify reads -- must always be the
// LITERAL parent, never substituted with the grandparent, even when
// ReferenceHash (depth-2) is set on the same EventBlockChecked.
func TestExtendsJustifyUnaffectedByCheckedReference(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, _ := newTestEngine(t, setup, 0)

	blockHash := types.Hash{0x21}
	parent := types.Hash{0x22}
	grandparent := types.Hash{0x23}

	if err := e.ProcessEvent(ConsensusEvent{Type: EventBlockChecked, Hash: blockHash, ParentHash: parent, ReferenceHash: grandparent}); err != nil {
		t.Fatalf("EventBlockChecked: %v", err)
	}
	got, known := e.importedParents[blockHash]
	if !known || got != parent {
		t.Fatalf("importedParents[blockHash] = (%x, known=%v), want the literal parent %x -- extendsJustify must never see the grandparent",
			got[:8], known, parent[:8])
	}
}
