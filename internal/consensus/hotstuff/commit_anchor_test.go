// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
)

type anchorFetcher struct {
	fakeBlockFetcher
	anchors []types.Hash
}

func (f *anchorFetcher) NoteCommitAnchor(h types.Hash, _ uint64) { f.anchors = append(f.anchors, h) }

// Every qs commit path notes the committed hash as an S1 anchor, and a
// commit the chain refused as conflicting (safety halt) never does.
func TestCommitAnchorNotedUnlessHalted(t *testing.T) {
	s, _, producer := hsTNewOutputService(t)
	f := &anchorFetcher{}
	s.blockFetcher = f
	ok := types.HexToHash("0xa1")
	s.handleBlockCommitted(EngineOutput{Type: OutputBlockCommitted, View: 3, Hash: ok})
	if len(f.anchors) != 1 || f.anchors[0] != ok {
		t.Fatalf("anchors = %x, want [%x]", f.anchors, ok)
	}
	bad := types.HexToHash("0xbb")
	producer.canonErr = &consensus.ConflictingCommitError{Target: bad, Height: 9, Incoming: bad}
	s.handleOutput(EngineOutput{Type: OutputBlockCommitted, View: 4, Hash: bad}, time.Time{})
	if !s.SafetyHalted() {
		t.Fatal("not halted")
	}
	producer.canonErr = nil
	s.handleBlockCommitted(EngineOutput{Type: OutputBlockCommitted, View: 5, Hash: types.HexToHash("0xc1")})
	if len(f.anchors) != 1 {
		t.Fatalf("anchor noted on a refused or post-halt commit: %x", f.anchors)
	}
}
