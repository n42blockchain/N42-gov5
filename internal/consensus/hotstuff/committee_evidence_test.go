// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Wiring test for the live BLS committee-evidence integration: Prepare stamps
// ParentBeaconRoot = parent CE BeaconRoot, and VerifyHeader enforces that link.

package hotstuff

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/blspool"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

// mapCEReader is an in-memory CEReader for tests.
type mapCEReader map[uint64]*rawdb.ConsensusEvidence

func (m mapCEReader) ReadConsensusEvidence(blockNum uint64) (*rawdb.ConsensusEvidence, error) {
	return m[blockNum], nil
}

// fakeChain returns a fixed parent header for the timestamp/parent lookups.
type fakeChain struct{ parent *block.Header }

func (c *fakeChain) Config() *params.ChainConfig      { return params.TestChainConfig }
func (c *fakeChain) CurrentBlock() block.IBlock       { return nil }
func (c *fakeChain) GetHeader(types.Hash, *uint256.Int) block.IHeader {
	return c.parent
}
func (c *fakeChain) GetHeaderByNumber(*uint256.Int) block.IHeader { return c.parent }
func (c *fakeChain) GetHeaderByHash(types.Hash) (block.IHeader, error) {
	return c.parent, nil
}
func (c *fakeChain) GetTd(types.Hash, *uint256.Int) *uint256.Int { return nil }
func (c *fakeChain) GetBlockByNumber(*uint256.Int) (block.IBlock, error) {
	return nil, nil
}

func newTestPool(t *testing.T) *blspool.Pool {
	t.Helper()
	var seed [32]byte
	seed[0] = 0x42
	pool, err := blspool.NewSimulatedPool(blspool.PoolConfig{
		Seed: seed, PoolSize: 16, CommitteeSize: 4, RampBlocks: 0,
	})
	if err != nil {
		t.Fatalf("NewSimulatedPool: %v", err)
	}
	return pool
}

// TestPrepareStampsParentBeaconRoot: Prepare sets ParentBeaconRoot to the parent
// block's CE BeaconRoot, and VerifyHeader accepts that exact value but rejects a
// tampered one.
func TestPrepareStampsParentBeaconRoot(t *testing.T) {
	pool := newTestPool(t)

	// Build CE for parent block N=5 and stash it in the reader.
	const parentNum = uint64(5)
	parentHash := types.Hash{0x11}
	parentCE, err := pool.BuildSimulatedCE(parentNum, parentHash, types.Hash{0xaa})
	if err != nil {
		t.Fatalf("BuildSimulatedCE: %v", err)
	}
	reader := mapCEReader{parentNum: parentCE}

	h := New(nil, params.TestChainConfig)
	h.SetCommitteeEvidence(pool, reader)

	parentHeader := &block.Header{
		Number: uint256.NewInt(parentNum),
		Time:   1000,
	}
	chain := &fakeChain{parent: parentHeader}

	// Prepare block N+1 = 6.
	child := &block.Header{Number: uint256.NewInt(parentNum + 1)}
	if err := h.Prepare(chain, child); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if child.ParentBeaconRoot == nil {
		t.Fatal("Prepare did not set ParentBeaconRoot")
	}
	want := parentCE.BeaconRoot()
	if *child.ParentBeaconRoot != want {
		t.Fatalf("ParentBeaconRoot = %x, want %x (parent CE BeaconRoot)", *child.ParentBeaconRoot, want)
	}

	// VerifyHeader must accept the well-formed child.
	if child.Time <= parentHeader.Time {
		child.Time = parentHeader.Time + 3
	}
	if err := h.VerifyHeader(chain, child, false); err != nil {
		t.Fatalf("VerifyHeader rejected valid child: %v", err)
	}

	// Tamper the ParentBeaconRoot — the committee-evidence link must break.
	bad := types.Hash{0xde, 0xad}
	child.ParentBeaconRoot = &bad
	if err := h.VerifyHeader(chain, child, false); err == nil {
		t.Fatal("VerifyHeader accepted tampered ParentBeaconRoot")
	}
}

// TestVerifyHeaderRejectsMissingLink: when parent evidence exists, a header that
// omits ParentBeaconRoot is rejected.
func TestVerifyHeaderRejectsMissingLink(t *testing.T) {
	pool := newTestPool(t)
	const parentNum = uint64(9)
	parentCE, err := pool.BuildSimulatedCE(parentNum, types.Hash{0x22}, types.Hash{})
	if err != nil {
		t.Fatalf("BuildSimulatedCE: %v", err)
	}
	reader := mapCEReader{parentNum: parentCE}

	h := New(nil, params.TestChainConfig)
	h.SetCommitteeEvidence(pool, reader)

	parentHeader := &block.Header{Number: uint256.NewInt(parentNum), Time: 2000}
	chain := &fakeChain{parent: parentHeader}

	child := &block.Header{Number: uint256.NewInt(parentNum + 1), Time: 2003}
	// No ParentBeaconRoot set.
	if err := h.VerifyHeader(chain, child, false); err == nil {
		t.Fatal("VerifyHeader accepted header missing the committee-evidence link")
	}
}

// TestNoEvidenceUnchanged: without SetCommitteeEvidence, Prepare leaves
// ParentBeaconRoot nil and VerifyHeader does not impose the link.
func TestNoEvidenceUnchanged(t *testing.T) {
	h := New(nil, params.TestChainConfig)
	parentHeader := &block.Header{Number: uint256.NewInt(3), Time: 500}
	chain := &fakeChain{parent: parentHeader}

	child := &block.Header{Number: uint256.NewInt(4)}
	if err := h.Prepare(chain, child); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if child.ParentBeaconRoot != nil {
		t.Fatalf("ParentBeaconRoot stamped without an evidence reader: %x", *child.ParentBeaconRoot)
	}
	child.Time = parentHeader.Time + 3
	if err := h.VerifyHeader(chain, child, false); err != nil {
		t.Fatalf("VerifyHeader rejected header on a non-evidence chain: %v", err)
	}
}
