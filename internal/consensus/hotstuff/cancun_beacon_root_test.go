// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// With no committee pool wired there is no evidence to link, and Prepare used to
// leave ParentBeaconRoot nil on a Cancun chain. That is not a neutral choice: a
// nil field skips the EIP-4788 system call entirely, and the header cannot be
// expressed over the Engine API at all, whose newPayloadV3 and later require the
// root. A zero root is what genesis and every Engine API client write when there
// is nothing to link, and VerifyHeader already accepts it (it only rejects a
// non-zero root when no parent evidence exists).
func TestPrepareWritesZeroBeaconRootOnCancunWithoutCommittee(t *testing.T) {
	cfg := *params.TestChainConfig
	cfg.CancunBlock = big.NewInt(0)
	if !cfg.IsCancunAt(6, 1003) {
		t.Fatal("fixture chain config is not Cancun; the case under test cannot run")
	}

	h := New(nil, &cfg) // no SetCommitteeEvidence: no pool, no evidence reader
	parentHeader := &block.Header{Number: uint256.NewInt(5), Time: 1000}
	chain := &fakeChain{parent: parentHeader}

	child := &block.Header{Number: uint256.NewInt(6), Time: 1003}
	if err := h.Prepare(chain, child); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if child.ParentBeaconRoot == nil {
		t.Fatal("Prepare left ParentBeaconRoot nil on a Cancun header: EIP-4788 is " +
			"skipped and the header cannot be carried over the Engine API")
	}
	if *child.ParentBeaconRoot != (types.Hash{}) {
		t.Fatalf("ParentBeaconRoot = %x, want the zero root when there is no evidence "+
			"to link", *child.ParentBeaconRoot)
	}

	// The zero root must survive the engine's own verification, otherwise the
	// proposer would build blocks its peers reject.
	if err := h.VerifyHeader(chain, child, false); err != nil {
		t.Fatalf("VerifyHeader rejected a zero ParentBeaconRoot: %v", err)
	}
}

// The zero-root fallback must never overwrite a real committee-evidence link.
func TestPrepareZeroRootDoesNotOverrideCommitteeEvidence(t *testing.T) {
	pool := newTestPool(t)

	const parentNum = uint64(5)
	parentHeader := &block.Header{Number: uint256.NewInt(parentNum), Time: 1000}
	parentCE, err := pool.BuildSimulatedCE(parentNum, parentHeader.Hash(), parentHeader.ReceiptHash)
	if err != nil {
		t.Fatalf("BuildSimulatedCE: %v", err)
	}

	h := New(nil, params.TestChainConfig)
	h.SetCommitteeEvidence(pool, mapCEReader{parentNum: parentCE})

	child := &block.Header{Number: uint256.NewInt(parentNum + 1)}
	if err := h.Prepare(&fakeChain{parent: parentHeader}, child); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if child.ParentBeaconRoot == nil {
		t.Fatal("Prepare did not set ParentBeaconRoot")
	}
	if want := parentCE.BeaconRoot(); *child.ParentBeaconRoot != want {
		t.Fatalf("ParentBeaconRoot = %x, want the parent CE root %x — the Cancun "+
			"fallback overwrote a real evidence link", *child.ParentBeaconRoot, want)
	}
}
