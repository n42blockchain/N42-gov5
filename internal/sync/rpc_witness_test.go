package sync

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"

	types "github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
)

func newWitnessTestService(t *testing.T, chain *syncTBlockByHashChain) (*Service, *fakeP2P) {
	t.Helper()
	fp := newFakeP2P(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp, chain: chain}}
	svc.rateLimiter = newRateLimiter(fp)
	return svc, fp
}

func newWitnessStreamPair(t *testing.T, fp *fakeP2P) (client, server *fakeStream) {
	t.Helper()
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	return newFakeStreamPair(remote, fp.self)
}

func TestWitnessRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newWitnessTestService(t, &syncTBlockByHashChain{})
	client, server := newWitnessStreamPair(t, fp)
	defer client.Close()
	defer server.Close()

	wrong := "not-a-request"
	if err := svc.witnessRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestWitnessRPCHandlerRejectsEmptyHash(t *testing.T) {
	svc, fp := newWitnessTestService(t, &syncTBlockByHashChain{})
	client, server := newWitnessStreamPair(t, fp)
	defer client.Close()

	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &GetBlockWitnessRequest{}
	if err := svc.witnessRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error for empty block hash")
	}
}

func TestWitnessRPCHandlerBlockNotFound(t *testing.T) {
	svc, fp := newWitnessTestService(t, &syncTBlockByHashChain{})
	client, server := newWitnessStreamPair(t, fp)
	defer client.Close()

	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &GetBlockWitnessRequest{BlockHash: comtypes.Hash{0x1}}
	if err := svc.witnessRPCHandler(context.Background(), req, server); err == nil {
		t.Fatal("expected error when the block does not exist")
	}
}

// TestWitnessRPCHandlerUnsupportedChainType exercises the type-assertion
// failure branch: the block exists, but the chain's concrete type is not
// *internal.BlockChain, so witness lookup cannot proceed. This is the only
// branch reachable from a test chain stub, since the witness cache itself is
// only exposed on the concrete internal.BlockChain type.
func TestWitnessRPCHandlerUnsupportedChainType(t *testing.T) {
	blk := syncTSmallBlock(1)
	hash := blk.Hash()
	chain := &syncTBlockByHashChain{byHash: map[comtypes.Hash]types.IBlock{hash: blk}}
	svc, fp := newWitnessTestService(t, chain)
	client, server := newWitnessStreamPair(t, fp)
	defer client.Close()

	go func() { _, _, _ = ReadStatusCode(client, fp.Encoding()) }()

	req := &GetBlockWitnessRequest{BlockHash: hash}
	if err := svc.witnessRPCHandler(context.Background(), req, server); err != nil {
		t.Fatalf("witnessRPCHandler: %v", err)
	}
}
