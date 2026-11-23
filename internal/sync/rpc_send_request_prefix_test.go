package sync

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// serveThenError streams blocks and then an error chunk, the way gov5 main and
// n42-26 end a range that fails after a prefix.
func serveThenError(server *fakeStream, fp *fakeP2P, blocks ...block.IBlock) {
	defer server.Close()
	for _, b := range blocks {
		if err := writeBlockChunk(server, (&syncTBlockByHashChain{}).GenesisBlock().Hash(), b); err != nil {
			return
		}
	}
	writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrInvalidBlockNr.Error(), server, fp)
}

func TestSendBodiesByRangeRequestKeepsPrefixBeforeErrorChunk(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()
	go serveThenError(server, fp, syncTSmallBlock(1), syncTSmallBlock(2))

	var processed int
	blocks, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, rangeReq(1, 5, 1), func(*block.Block) error {
		processed++
		return nil
	})
	if err != nil {
		t.Fatalf("a prefix followed by an error chunk must be kept, got error %v", err)
	}
	if len(blocks) != 2 || processed != 2 {
		t.Fatalf("got %d blocks (%d processed), want 2", len(blocks), processed)
	}
	if next := blocks[len(blocks)-1].Number64().Uint64() + 1; next != 3 {
		t.Fatalf("next request would start at %d, want 3", next)
	}
}

func TestSendBodiesByRangeRequestErrorChunkFirstStillFails(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()
	go serveThenError(server, fp)

	blocks, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, rangeReq(1, 5, 1), nil)
	if err == nil || len(blocks) != 0 {
		t.Fatalf("got %d blocks, err %v; want an error with no blocks", len(blocks), err)
	}
}

func TestSendBodiesByRangeRequestStillRejectsOutOfRangeBlockAfterPrefix(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()
	// Block 9 lies outside the requested [1,2]: a protocol violation, not a
	// short reply, so the prefix does not rescue the reply.
	go serveThenError(server, fp, syncTSmallBlock(1), syncTSmallBlock(9))

	if _, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, rangeReq(1, 2, 1), nil); err == nil {
		t.Fatal("expected ErrInvalidFetchedData for a block outside the range")
	}
}

// prefixThenErrorP2P serves a stored prefix of any request and then an error
// chunk for the rest, like a gov5 main server whose chain ends inside the range.
type prefixThenErrorP2P struct {
	*fakeP2P
	stored map[uint64]block.IBlock
	starts []uint64
}

func (f *prefixThenErrorP2P) Send(_ context.Context, msg interface{}, _ string, pid peer.ID) (network.Stream, error) {
	req := msg.(*sync_pb.BodiesByRangeRequest)
	start := utils.ConvertH256ToUint256Int(req.StartBlockNumber).Uint64()
	f.starts = append(f.starts, start)
	client, server := newFakeStreamPair(pid, f.self)
	var blocks []block.IBlock
	for n := start; n < start+req.Count; n++ {
		b, ok := f.stored[n]
		if !ok {
			break
		}
		blocks = append(blocks, b)
	}
	go serveThenError(server, f.fakeP2P, blocks...)
	return client, nil
}

// Catch-up imports the prefix of a reply that ended in an error chunk, without
// shrinking the range or scoring the peer.
func TestCatchUpImportsPrefixOfReplyEndingInErrorChunk(t *testing.T) {
	svc, fp, chain := newCatchUpEndToEndService(t, 100)
	stored := map[uint64]block.IBlock{}
	for _, b := range syncTLinkedBlocks(101, 3) {
		stored[b.Number64().Uint64()] = b
	}
	op := &prefixThenErrorP2P{fakeP2P: fp, stored: stored}
	svc.cfg.p2p = op
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatal(err)
	}
	fp.addConnectedPeer(t, remote, 1)

	svc.catchUpTo(107, []peer.ID{remote})

	if len(chain.inserted) != 3 {
		t.Fatalf("inserted %d blocks, want 3", len(chain.inserted))
	}
	if len(op.starts) != 1 || op.starts[0] != 101 {
		t.Fatalf("request starts %v, want a single request at 101 (no shrink retry)", op.starts)
	}
	if n, _ := fp.Peers().Scorers().BadResponsesScorer().Count(remote); n != 0 {
		t.Fatalf("peer scored %d times for a usable prefix", n)
	}
}
