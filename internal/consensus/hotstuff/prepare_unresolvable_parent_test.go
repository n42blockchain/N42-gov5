// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// With committee evidence wired, a header whose parent the chain cannot resolve
// must not be prepared: the ParentBeaconRoot link can only come from the parent
// header, and a block built without it is rejected by every follower. Round
// 35zo built exactly such a block on an own, still-unwritten parent, and round
// 35zq re-proposed it from the store after a restart. Refusing at Prepare keeps
// the block out of the store altogether.
func TestPrepareRefusesUnresolvableParentWithCommittee(t *testing.T) {
	h := New(nil, params.TestChainConfig)
	h.SetCommitteeEvidence(newTestPool(t), mapCEReader{})

	child := &block.Header{Number: uint256.NewInt(6), ParentHash: types.Hash{0xab}}
	err := h.Prepare(&fakeChain{parent: nil}, child)
	if err == nil {
		t.Fatal("Prepare built a header on a parent the chain cannot resolve; the " +
			"committee-evidence link is unset and every follower rejects the block")
	}
	if !strings.Contains(err.Error(), "not resolvable") {
		t.Fatalf("Prepare error = %q, want the unresolvable-parent refusal", err)
	}
	if child.ParentBeaconRoot != nil {
		t.Fatalf("Prepare stamped ParentBeaconRoot %x on a refused header", *child.ParentBeaconRoot)
	}
}

// Without committee evidence there is no link to break, and Prepare keeps its
// tolerant shape for chains that carry no evidence (the parent lookup is only
// needed for the timestamp and the link).
func TestPrepareToleratesUnresolvableParentWithoutCommittee(t *testing.T) {
	h := New(nil, params.TestChainConfig)
	child := &block.Header{Number: uint256.NewInt(6), Time: 1003, ParentHash: types.Hash{0xab}}
	if err := h.Prepare(&fakeChain{parent: nil}, child); err != nil {
		t.Fatalf("Prepare without a committee pool refused an unresolvable parent: %v", err)
	}
}
