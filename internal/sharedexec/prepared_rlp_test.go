package sharedexec

import (
	"bytes"
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
)

// Exercise the owner directly: no Unix listener, load generator or retained DB.
func TestPreparedBuildRLPOwnershipAndFallback(t *testing.T) {
	chain, _, leader, template, _, live := unsignedBuildFixtureWithSet(t, 7, nil)
	strict, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(strict.Close)
	reuse, err := New(chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reuse.Close)
	if err := reuse.EnableOwnBuildReuse(); err != nil {
		t.Fatal(err)
	}
	request := buildRequest(t, strict.Identity(), template)
	build := func(s *Server, reserved int) (BuildResult, error) {
		s.execute.Lock()
		defer s.execute.Unlock()
		return s.executeBuildLocked(request.TemplateRLP, request.TemplateHash, reserved)
	}
	reserved := 2*len(request.TemplateRLP) + 4096
	ordinary, err := build(strict, reserved)
	if err != nil || strict.prepared != nil {
		t.Fatalf("ordinary Marshal fallback: %v", err)
	}
	if _, err := build(reuse, len(request.TemplateRLP)); err == nil || err.Error() != "candidate exceeds reserved payload budget" || reuse.prepared != nil {
		t.Fatalf("prepared output bypassed payload budget: %v", err)
	}
	built, err := build(reuse, reserved)
	if err != nil {
		t.Fatal(err)
	}
	prepared := reuse.prepared
	if prepared == nil || prepared.RetainedBytes() <= 0 || prepared.RetainedBytes() > internal.MaxPreparedCandidateBytes {
		t.Fatal("prepared output missing or over retained budget")
	}
	if !bytes.Equal(ordinary.BlockRLP, built.BlockRLP) || ordinary.BlockHash != built.BlockHash || ordinary.Result != built.Result {
		t.Fatal("prepared output differs from strict Marshal path")
	}
	var candidate block.Block
	if err := candidate.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	oracle, err := candidate.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oracle, built.BlockRLP) || crypto.Keccak256Hash(oracle) != crypto.Keccak256Hash(built.BlockRLP) || candidate.Hash() != built.BlockHash {
		t.Fatal("prepared output differs from fresh encoding/hash oracle")
	}
	retained := prepared.RetainedBytes()
	copy := prepared.CopyUnsignedRLP()
	if !bytes.Equal(copy, oracle) || len(copy) == 0 {
		t.Fatal("prepared unsigned bytes differ")
	}
	copy[0] ^= 1
	built.BlockRLP[0] ^= 1
	if !bytes.Equal(prepared.CopyUnsignedRLP(), oracle) || prepared.RetainedBytes() != retained {
		t.Fatal("returned bytes alias the private authentication bytes or change accounting")
	}
	var absent *internal.PreparedUnsignedCandidate
	if absent.CopyUnsignedRLP() != nil {
		t.Fatal("missing prepared candidate must retain Marshal fallback")
	}
	if live.Root() != chain.GenesisBlock().StateRoot() {
		t.Fatal("build changed live state")
	}
	sealed := make(chan block.IBlock, 1)
	if err := leader.Seal(chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	adopted, err := chain.ImportPreparedCandidate(context.Background(), b, prepared)
	if err != nil || !adopted {
		t.Fatalf("mutating returned copies damaged prepared authentication: %v %v", adopted, err)
	}
	if adopted, err := chain.ImportPreparedCandidate(context.Background(), b, prepared); err != nil || adopted {
		t.Fatalf("consumed candidate reused: %v %v", adopted, err)
	}
}
