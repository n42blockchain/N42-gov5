package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// buildPendingVotes signs the Round 1 voting message for a block hash with
// the first n validator keys from setup, returning one pendingVote per
// signer. corruptIdx, if >= 0, replaces that signer's signature bytes with
// garbage so batch verification fails and the per-vote fallback has exactly
// one bad signature to isolate.
func buildPendingVotes(setup *testSetup, view ViewNumber, blockHash types.Hash, n int, corruptIdx int) []pendingVote {
	msg := SigningMessage(uint64(view), blockHash)
	votes := make([]pendingVote, n)
	for i := 0; i < n; i++ {
		sig := setup.keys[i].Sign(msg).Marshal()
		if i == corruptIdx {
			sig = append([]byte(nil), sig...)
			sig[0] ^= 0xFF // flip bits: still well-formed length, wrong signature
		}
		votes[i] = pendingVote{
			voter:    ValidatorIndex(i),
			sigBytes: sig,
			pk:       setup.pubKeys[i],
			message:  msg,
		}
	}
	return votes
}

// TestFlushPrepareVotesBatchFallbackIsolatesInvalidSignature drives
// flushPrepareVotes with a buffer at/above batchVerifyThreshold where one
// signature is corrupted. Batch verification must fail as a whole, fall back
// to individual verification, admit only the valid votes into the collector,
// and silently drop the bad one (no error).
func TestFlushPrepareVotesBatchFallbackIsolatesInvalidSignature(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	view := engine.roundState.CurrentView()
	blockHash := types.Hash{0xD1}
	engine.voteCollector = NewVoteCollector(view, blockHash, setup.vs.Len())

	// 4 votes, index 2's signature corrupted: >= batchVerifyThreshold so this
	// goes through the batch path, which must fail and fall back.
	engine.prepareVoteBuf = buildPendingVotes(setup, view, blockHash, 4, 2)

	if err := engine.flushPrepareVotes(); err != nil {
		t.Fatalf("flushPrepareVotes: %v", err)
	}

	if len(engine.prepareVoteBuf) != 0 {
		t.Fatalf("expected the buffer to be drained, len=%d", len(engine.prepareVoteBuf))
	}
	if got := engine.voteCollector.VoteCount(); got != 3 {
		t.Fatalf("VoteCollector.VoteCount() = %d, want 3 (one of four rejected)", got)
	}
	for _, i := range []ValidatorIndex{0, 1, 3} {
		if _, tracked := engine.equivocationTracker[i]; !tracked {
			t.Fatalf("expected validator %d's verified vote to be tracked for equivocation", i)
		}
	}
	if _, tracked := engine.equivocationTracker[2]; tracked {
		t.Fatal("expected the rejected validator's vote to NOT be tracked")
	}
}

// TestFlushPrepareVotesBatchAllValidAdmitsAll exercises the batch success
// path (no corrupted signature) as a control alongside the fallback test
// above: all votes in the >= batchVerifyThreshold batch must be admitted.
func TestFlushPrepareVotesBatchAllValidAdmitsAll(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	view := engine.roundState.CurrentView()
	blockHash := types.Hash{0xD2}
	engine.voteCollector = NewVoteCollector(view, blockHash, setup.vs.Len())
	engine.prepareVoteBuf = buildPendingVotes(setup, view, blockHash, 4, -1)

	if err := engine.flushPrepareVotes(); err != nil {
		t.Fatalf("flushPrepareVotes: %v", err)
	}
	if got := engine.voteCollector.VoteCount(); got != 4 {
		t.Fatalf("VoteCollector.VoteCount() = %d, want 4", got)
	}
}

// buildPendingCommitVotes is buildPendingVotes's Round 2 (CommitVote)
// counterpart: it signs the commit-signing message instead of the prepare one.
func buildPendingCommitVotes(setup *testSetup, view ViewNumber, blockHash types.Hash, n int, corruptIdx int) []pendingVote {
	msg := CommitSigningMessage(uint64(view), blockHash)
	votes := make([]pendingVote, n)
	for i := 0; i < n; i++ {
		sig := setup.keys[i].Sign(msg).Marshal()
		if i == corruptIdx {
			sig = append([]byte(nil), sig...)
			sig[0] ^= 0xFF
		}
		votes[i] = pendingVote{
			voter:    ValidatorIndex(i),
			sigBytes: sig,
			pk:       setup.pubKeys[i],
			message:  msg,
		}
	}
	return votes
}

// TestFlushCommitVotesBatchFallbackIsolatesInvalidSignature is the Round 2
// counterpart of TestFlushPrepareVotesBatchFallbackIsolatesInvalidSignature:
// flushCommitVotes must fall back to individual verification and admit only
// the valid commit votes.
func TestFlushCommitVotesBatchFallbackIsolatesInvalidSignature(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	view := engine.roundState.CurrentView()
	blockHash := types.Hash{0xD3}
	engine.commitCollector = NewVoteCollector(view, blockHash, setup.vs.Len())
	engine.commitVoteBuf = buildPendingCommitVotes(setup, view, blockHash, 4, 1)

	if err := engine.flushCommitVotes(); err != nil {
		t.Fatalf("flushCommitVotes: %v", err)
	}
	if len(engine.commitVoteBuf) != 0 {
		t.Fatalf("expected the buffer to be drained, len=%d", len(engine.commitVoteBuf))
	}
	if got := engine.commitCollector.VoteCount(); got != 3 {
		t.Fatalf("CommitCollector.VoteCount() = %d, want 3 (one of four rejected)", got)
	}
}
