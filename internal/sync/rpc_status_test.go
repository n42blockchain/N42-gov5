package sync

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// newStatusTestService builds a *Service with a fixed genesis hash (via
// overrideGenesisHash, so no real chain.GenesisBlock() is needed) and a
// rate limiter, suitable for exercising statusRPCHandler.
func newStatusTestService(t *testing.T, headHeight uint64) (*Service, *fakeP2P, types.Hash) {
	t.Helper()
	fp := newFakeP2P(t)
	chain := &syncChainStub{current: &syncBlockStub{number: uint256.NewInt(headHeight)}}
	genesis := types.Hash{0xAA, 0xBB}
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp, chain: chain, overrideGenesisHash: &genesis}}
	svc.rateLimiter = newRateLimiter(fp)
	return svc, fp, genesis
}

func TestStatusRPCHandlerWrongMessageType(t *testing.T) {
	svc, fp := newPingTestService(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()

	wrong := "not-a-status"
	if err := svc.statusRPCHandler(context.Background(), &wrong, server); err == nil {
		t.Fatal("expected error for wrong message type")
	}
}

func TestStatusRPCHandlerMatchingDigestRespondsSuccess(t *testing.T) {
	svc, fp, genesis := newStatusTestService(t, 50)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 10)

	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()
	_ = server.SetProtocol(protocol.ID(p2p.RPCStatusTopicV1 + fp.Encoding().ProtocolSuffix()))

	peerMsg := &sync_pb.Status{
		GenesisHash:   utils.ConvertHashToH256(genesis),
		CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(10)),
	}

	done := make(chan error, 1)
	go func() { done <- svc.statusRPCHandler(context.Background(), peerMsg, server) }()

	buf := make([]byte, 1)
	if _, err := client.Read(buf); err != nil {
		t.Fatalf("client read response code: %v", err)
	}
	if buf[0] != responseCodeSuccess {
		t.Fatalf("response code = %d, want success", buf[0])
	}
	var resp sync_pb.Status
	if err := fp.Encoding().DecodeWithMaxLength(client, &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("statusRPCHandler returned error: %v", err)
	}

	state, err := fp.Peers().ChainState(remote)
	if err != nil || state == nil {
		t.Fatalf("expected chain state recorded for peer, err=%v state=%v", err, state)
	}
}

func TestValidateStatusMessageMismatch(t *testing.T) {
	svc, _, _ := newStatusTestService(t, 50)
	badMsg := &sync_pb.Status{
		GenesisHash:   utils.ConvertHashToH256(types.Hash{0xFF}),
		CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
	}
	if err := svc.validateStatusMessage(context.Background(), badMsg); err == nil {
		t.Fatal("expected a fork digest mismatch error")
	}
}

func TestValidateStatusMessageMatch(t *testing.T) {
	svc, _, genesis := newStatusTestService(t, 50)
	goodMsg := &sync_pb.Status{
		GenesisHash:   utils.ConvertHashToH256(genesis),
		CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
	}
	if err := svc.validateStatusMessage(context.Background(), goodMsg); err != nil {
		t.Fatalf("expected matching digest to validate, got %v", err)
	}
}
