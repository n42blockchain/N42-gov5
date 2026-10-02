package sync

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	block "github.com/n42blockchain/N42/common/block"
)

// syncTInsertChainStub extends syncChainStub with a recording InsertChain, so
// catchUpTo/catchUpRange can be driven end to end without a real BlockChain.
type syncTInsertChainStub struct {
	syncChainStub
	inserted []block.IBlock
	// insertErr, when set, is returned by InsertChain instead of success.
	insertErr error
}

func (c *syncTInsertChainStub) InsertChain(blocks []block.IBlock) (int, error) {
	if c.insertErr != nil {
		return 0, c.insertErr
	}
	c.inserted = append(c.inserted, blocks...)
	return len(blocks), nil
}

func newCatchUpEndToEndService(t *testing.T, headHeight uint64) (*Service, *fakeP2P, *syncTInsertChainStub) {
	t.Helper()
	fp := newFakeP2P(t)
	chain := &syncTInsertChainStub{syncChainStub: syncChainStub{current: &syncBlockStub{number: uint256.NewInt(headHeight)}}}
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp, chain: chain}}
	svc.rateLimiter = newRateLimiter(fp)
	if err := svc.initCaches(); err != nil {
		t.Fatalf("initCaches: %v", err)
	}
	return svc, fp, chain
}

// syncTServeRangeOnce writes the given blocks as chunked responses on server,
// then closes it (EOF), mimicking a peer answering a bodies-by-range request.
func syncTServeRangeOnce(server *fakeStream, blocks ...block.IBlock) {
	defer server.Close()
	for _, b := range blocks {
		if err := writeBlockChunk(server, (&syncTBlockByHashChain{}).GenesisBlock().Hash(), b); err != nil {
			return
		}
	}
}

func TestCatchUpToImportsFetchedRange(t *testing.T) {
	svc, fp, chain := newCatchUpEndToEndService(t, 100)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	fp.sendStream = client

	b1 := syncTSmallBlock(101)
	b2 := syncTSmallBlock(102)
	go syncTServeRangeOnce(server, b1, b2)

	svc.catchUpTo(102, []peer.ID{remote})

	if len(chain.inserted) != 2 {
		t.Fatalf("inserted %d blocks, want 2", len(chain.inserted))
	}
	if chain.inserted[0].Number64().Uint64() != 101 || chain.inserted[1].Number64().Uint64() != 102 {
		t.Fatalf("inserted blocks in wrong order/numbers: %+v", chain.inserted)
	}
}

func TestCatchUpToNoOpWhenTargetNotAboveSelf(t *testing.T) {
	svc, _, chain := newCatchUpEndToEndService(t, 100)
	svc.catchUpTo(100, nil)
	if len(chain.inserted) != 0 {
		t.Fatalf("expected no inserts when target <= self, got %d", len(chain.inserted))
	}
}

func TestCatchUpRangeEndsRoundWhenPeerFailsOutright(t *testing.T) {
	svc, fp, _ := newCatchUpEndToEndService(t, 100)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	fp.sendStream = client
	// Close the server immediately: the client's first read fails, and
	// SendBodiesByRangeRequest returns an error (not ErrUnknownAncestor), so
	// catchUpRange should report "done" (true) via the non-fixable path...
	// actually: a plain request failure (no blocks fetched) is also not an
	// unknown-ancestor condition, so sawUnknownAncestor stays false and the
	// round reports fixable=false -> returns true (round is done).
	server.Close()

	req := rangeReq(101, 2, 1)
	done := svc.catchUpRange(req, []peer.ID{remote}, false, 101, 102)
	if !done {
		t.Fatal("expected catchUpRange to report the round as done when every peer fails outright")
	}
}

func TestCatchUpRangeImportFailureStillReturnsTrue(t *testing.T) {
	svc, fp, chain := newCatchUpEndToEndService(t, 100)
	chain.insertErr = errSyncTCatchUpInsert
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	fp.sendStream = client

	b1 := syncTSmallBlock(101)
	go syncTServeRangeOnce(server, b1)

	req := rangeReq(101, 1, 1)
	done := svc.catchUpRange(req, []peer.ID{remote}, false, 101, 101)
	// A deterministic (non-unknown-ancestor) insert failure ends the round.
	if !done {
		t.Fatal("expected catchUpRange to end the round on a non-recoverable insert error")
	}
}

var errSyncTCatchUpInsert = &syncTTestErr{"insert failed"}

type syncTTestErr struct{ msg string }

func (e *syncTTestErr) Error() string { return e.msg }
