package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// syncTLinkedBlocks builds count RLP-round-trippable blocks starting at from,
// each one's ParentHash set to the previous block's hash, so the server-side
// linkage guard in writeBodiesRangeToStream does not truncate them.
func syncTLinkedBlocks(from uint64, count int) []block.IBlock {
	out := make([]block.IBlock, 0, count)
	for i := 0; i < count; i++ {
		b := syncTSmallBlock(from + uint64(i)).(*block.Block)
		h := b.Header().(*block.Header)
		if i > 0 {
			h.ParentHash = out[i-1].Hash()
		}
		out = append(out, block.NewBlock(h, nil))
	}
	return out
}

func syncTRangeChain(head uint64, blocks []block.IBlock) *rangeChainStub {
	c := &rangeChainStub{byNumber: map[uint64]block.IBlock{}}
	c.current = &syncBlockStub{number: uint256.NewInt(head)}
	for _, b := range blocks {
		c.byNumber[b.Number64().Uint64()] = b
	}
	return c
}

// A peer whose stored canonical chain ends inside the requested range (the
// deferred-execution case: the CommitQC target runs ahead of the canonical
// rows every peer has written) must serve the stored prefix, not fail the
// whole request with "invalid block number".
func TestWriteBodiesRangeToStreamServesStoredPrefix(t *testing.T) {
	svc, fp := newRangeTestService(t, 103)
	svc.cfg.chain = syncTRangeChain(103, syncTLinkedBlocks(101, 3)) // 101..103 stored
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	fp.sendStream = client

	writeErr := make(chan error, 1)
	go func() {
		defer server.Close()
		writeErr <- svc.writeBodiesRangeToStream(context.Background(), uint256.NewInt(101), uint256.NewInt(107), 1, false, server)
	}()

	got, err := SendBodiesByRangeRequest(context.Background(), svc.cfg.chain, fp, remote, rangeReq(101, 7, 1), nil)
	if err != nil {
		t.Fatalf("SendBodiesByRangeRequest: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("fetched %d blocks, want the 3 stored", len(got))
	}
	for i, b := range got {
		if n := b.Number64().Uint64(); n != 101+uint64(i) {
			t.Fatalf("block %d has number %d, want %d", i, n, 101+uint64(i))
		}
	}
	if err := <-writeErr; !errors.Is(err, errRangeTruncated) {
		t.Fatalf("writeBodiesRangeToStream err = %v, want errRangeTruncated", err)
	}
}

// A later sub-batch that finds nothing stored ends the response cleanly: no
// error chunk may follow blocks already streamed by an earlier sub-batch.
func TestWriteBodiesRangeToStreamEmptyLaterBatchEndsCleanly(t *testing.T) {
	svc, fp := newRangeTestService(t, 100)
	svc.cfg.chain = syncTRangeChain(100, nil)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	fp.sendStream = client

	writeErr := make(chan error, 1)
	go func() {
		defer server.Close()
		writeErr <- svc.writeBodiesRangeToStream(context.Background(), uint256.NewInt(101), uint256.NewInt(104), 1, true, server)
	}()

	got, err := SendBodiesByRangeRequest(context.Background(), svc.cfg.chain, fp, remote, rangeReq(101, 4, 1), nil)
	if err != nil {
		t.Fatalf("SendBodiesByRangeRequest: %v (an error chunk was written)", err)
	}
	if len(got) != 0 {
		t.Fatalf("fetched %d blocks, want 0", len(got))
	}
	if err := <-writeErr; !errors.Is(err, errRangeTruncated) {
		t.Fatalf("writeBodiesRangeToStream err = %v, want errRangeTruncated", err)
	}
}

// syncTOldServerP2P answers every Send with a fresh stream served like a peer
// that predates prefix serving: any request reaching past its stored height
// fails outright with "invalid block number".
type syncTOldServerP2P struct {
	*fakeP2P
	stored map[uint64]block.IBlock
	counts []uint64
}

func (f *syncTOldServerP2P) Send(_ context.Context, msg interface{}, _ string, pid peer.ID) (network.Stream, error) {
	req := msg.(*sync_pb.BodiesByRangeRequest)
	f.counts = append(f.counts, req.Count)
	client, server := newFakeStreamPair(pid, f.self)
	start := utils.ConvertH256ToUint256Int(req.StartBlockNumber).Uint64()
	blocks := make([]block.IBlock, 0, req.Count)
	for n := start; n < start+req.Count; n++ {
		b, ok := f.stored[n]
		if !ok {
			go func() {
				defer server.Close()
				writeErrorResponseToStream(responseCodeServerError, p2ptypes.ErrInvalidBlockNr.Error(), server, f)
			}()
			return client, nil
		}
		blocks = append(blocks, b)
	}
	go syncTServeRangeOnce(server, blocks...)
	return client, nil
}

// When every peer rejects the range outright, catch-up must shrink the range
// and retry the same start instead of re-sending the identical failing range.
func TestCatchUpToShrinksRangeRejectedByEveryPeer(t *testing.T) {
	svc, fp, chain := newCatchUpEndToEndService(t, 100)
	stored := map[uint64]block.IBlock{}
	for _, b := range syncTLinkedBlocks(101, 3) { // peer stores 101..103 only
		stored[b.Number64().Uint64()] = b
	}
	op := &syncTOldServerP2P{fakeP2P: fp, stored: stored}
	svc.cfg.p2p = op
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}

	svc.catchUpTo(107, []peer.ID{remote})

	if len(chain.inserted) != 3 {
		t.Fatalf("inserted %d blocks, want 3 (counts requested: %v)", len(chain.inserted), op.counts)
	}
	if len(op.counts) != 2 || op.counts[0] != 7 || op.counts[1] != 3 {
		t.Fatalf("requested counts %v, want [7 3]", op.counts)
	}
}

// A peer that has nothing at all keeps failing; the shrink must terminate at
// a single-block request rather than loop.
func TestCatchUpToShrinkTerminates(t *testing.T) {
	svc, fp, chain := newCatchUpEndToEndService(t, 100)
	op := &syncTOldServerP2P{fakeP2P: fp, stored: map[uint64]block.IBlock{}}
	svc.cfg.p2p = op
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}

	svc.catchUpTo(107, []peer.ID{remote})

	if len(chain.inserted) != 0 {
		t.Fatalf("inserted %d blocks, want 0", len(chain.inserted))
	}
	want := []uint64{7, 3, 1}
	if len(op.counts) != len(want) {
		t.Fatalf("requested counts %v, want %v", op.counts, want)
	}
	for i := range want {
		if op.counts[i] != want[i] {
			t.Fatalf("requested counts %v, want %v", op.counts, want)
		}
	}
}
