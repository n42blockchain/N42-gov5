package sync

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/protocol"

	block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/p2p"
)

// failingRangeChain returns an error for one block number, like a store read
// failure in the middle of a range.
type failingRangeChain struct {
	*rangeChainStub
	failAt uint64
}

func (c *failingRangeChain) GetBlockByNumber(n *uint256.Int) (block.IBlock, error) {
	if n.Uint64() == c.failAt {
		return nil, errors.New("store read failed")
	}
	return c.rangeChainStub.GetBlockByNumber(n)
}

// serveRange runs the real handler for rangeReq(start,count,step) over a
// stream pair and returns the block numbers received plus the terminal error
// of the reader (io.EOF for a clean end).
func serveRange(t *testing.T, svc *Service, fp *fakeP2P, start, count, step uint64) ([]uint64, error) {
	t.Helper()
	client, server := connectedStreamServer(t, fp)
	defer client.Close()
	defer server.Close()
	if err := server.SetProtocol(protocol.ID(p2p.RPCBodiesDataTopicV1 + fp.Encoding().ProtocolSuffix())); err != nil {
		t.Fatal(err)
	}
	go func() { _ = svc.bodiesByRangeRPCHandler(context.Background(), rangeReq(start, count, step), server) }()
	var got []uint64
	for i := 0; ; i++ {
		b, err := ReadChunkedBlock(client, fp, i == 0)
		if err != nil {
			return got, err
		}
		got = append(got, b.Number64().Uint64())
	}
}

func rangeServiceWithBlocks(t *testing.T, nums ...uint64) (*Service, *fakeP2P, *rangeChainStub) {
	svc, fp := newRangeTestService(t, 100)
	chain := newRangeChainStub(100)
	for _, n := range nums {
		chain.byNumber[n] = syncTSmallBlock(n)
	}
	svc.cfg.chain = chain
	return svc, fp, chain
}

// A read failure after blocks were already collected ends the reply cleanly
// with the prefix; the n42-rs and n42-26 readers discard a prefix that is
// followed by an error chunk.
func TestRangeServerEndsCleanlyOnReadErrorAfterPrefix(t *testing.T) {
	svc, fp, chain := rangeServiceWithBlocks(t, 1, 3, 5)
	svc.cfg.chain = &failingRangeChain{rangeChainStub: chain, failAt: 5}
	got, err := serveRange(t, svc, fp, 1, 3, 2)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("reply ended with %v, want a clean EOF", err)
	}
	if len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("got blocks %v, want [1 3]", got)
	}
}

// With nothing sent yet the failure is still reported as an error chunk.
func TestRangeServerReportsReadErrorBeforeFirstBlock(t *testing.T) {
	svc, fp, chain := rangeServiceWithBlocks(t, 1, 3)
	svc.cfg.chain = &failingRangeChain{rangeChainStub: chain, failAt: 1}
	got, err := serveRange(t, svc, fp, 1, 2, 2)
	if err == nil || errors.Is(err, io.EOF) || len(got) != 0 {
		t.Fatalf("got %v, %v; want an error chunk and no blocks", got, err)
	}
}

// A missing block after the prefix also ends cleanly (already the case; kept
// as a guard for the clean-end rule).
func TestRangeServerEndsCleanlyAtMissingBlockAfterPrefix(t *testing.T) {
	svc, fp, _ := rangeServiceWithBlocks(t, 1, 3)
	got, err := serveRange(t, svc, fp, 1, 3, 2)
	if !errors.Is(err, io.EOF) || len(got) != 2 {
		t.Fatalf("got %v, %v; want [1 3] and EOF", got, err)
	}
}

// The per-reply byte cap ends the reply cleanly with the blocks sent so far,
// and always sends at least the first block.
func TestRangeServerByteCapEndsReplyCleanly(t *testing.T) {
	data, err := encodeBlockChunkPayload(syncTSmallBlock(1))
	if err != nil {
		t.Fatal(err)
	}
	size := uint64(len(data))
	old := rangeReplyByteCap
	defer func() { rangeReplyByteCap = old }()

	for _, tc := range []struct {
		name string
		cap  uint64
		want int
	}{
		{"two fit", size*2 + size/2, 2},
		{"exactly two", size * 2, 2},
		{"cap below one block still sends one", size / 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rangeReplyByteCap = tc.cap
			svc, fp, _ := rangeServiceWithBlocks(t, 1, 3, 5, 7)
			got, err := serveRange(t, svc, fp, 1, 4, 2)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("reply ended with %v, want a clean EOF", err)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d blocks %v, want %d", len(got), got, tc.want)
			}
		})
	}
}
