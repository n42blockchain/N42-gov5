package sync

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// newStatusSendTestService builds a *Service over a real (socket-less) host
// -- sendRPCStatusRequest's peer path (via reValidatePeer -> sendPingRequest)
// touches Host().Peerstore(), which a bare fakeP2P does not provide.
func newStatusSendTestService(t *testing.T, headHeight uint64) (*Service, *fakeP2P, types.Hash) {
	t.Helper()
	fp := newFakeP2PWithHost(t)
	chain := &syncChainStub{current: &syncBlockStub{number: uint256.NewInt(headHeight)}}
	genesis := types.Hash{0xAA, 0xBB}
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp, chain: chain, overrideGenesisHash: &genesis}}
	svc.rateLimiter = newRateLimiter(fp)
	return svc, fp, genesis
}

// TestSendRPCStatusRequestSuccess drives sendRPCStatusRequest's happy path:
// the remote replies with a success code and a status matching our fork
// digest, which must be recorded via PeerStatusScorer.SetPeerStatus.
func TestSendRPCStatusRequestSuccess(t *testing.T) {
	svc, fp, genesis := newStatusSendTestService(t, 50)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 10)

	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()
	fp.sendStream = server

	go func() {
		_, _ = client.Write([]byte{responseCodeSuccess})
		resp := &sync_pb.Status{
			GenesisHash:   utils.ConvertHashToH256(genesis),
			CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(10)),
		}
		_, _ = fp.Encoding().EncodeWithMaxLength(client, resp)
	}()

	if err := svc.sendRPCStatusRequest(context.Background(), remote); err != nil {
		t.Fatalf("sendRPCStatusRequest: %v", err)
	}
	state, err := fp.Peers().ChainState(remote)
	if err != nil || state == nil {
		t.Fatalf("expected chain state recorded, err=%v state=%v", err, state)
	}
}

// TestSendRPCStatusRequestBadResponseCode covers the non-zero response-code
// branch: the peer is scored bad and the error message is surfaced.
func TestSendRPCStatusRequestBadResponseCode(t *testing.T) {
	svc, fp, _ := newStatusSendTestService(t, 50)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 10)

	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()
	fp.sendStream = server

	go func() {
		resp, _ := svc.generateErrorResponse(responseCodeInvalidRequest, "nope")
		_, _ = client.Write(resp)
	}()

	err = svc.sendRPCStatusRequest(context.Background(), remote)
	if err == nil || err.Error() != "nope" {
		t.Fatalf("sendRPCStatusRequest() error = %v, want %q", err, "nope")
	}
}

// TestReValidatePeerRunsStatusAndPing exercises reValidatePeer's happy path:
// both the status round trip and the (best-effort) ping round trip succeed.
func TestReValidatePeerRunsStatusAndPing(t *testing.T) {
	svc, fp, genesis := newStatusSendTestService(t, 50)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 10)

	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()
	fp.sendStream = server

	go func() {
		// Status response.
		_, _ = client.Write([]byte{responseCodeSuccess})
		resp := &sync_pb.Status{
			GenesisHash:   utils.ConvertHashToH256(genesis),
			CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(10)),
		}
		_, _ = fp.Encoding().EncodeWithMaxLength(client, resp)
		// Ping response.
		_, _ = client.Write([]byte{responseCodeSuccess})
		seq := fp.GetPing().SeqNumber
		pingResp := seq
		_ = pingResp
		_, _ = fp.Encoding().EncodeWithMaxLength(client, &sync_pb.Ping{SeqNumber: seq})
	}()

	if err := svc.reValidatePeer(context.Background(), remote); err != nil {
		t.Fatalf("reValidatePeer: %v", err)
	}
}

// TestStatusRPCHandlerForkDigestMismatchSendsGoodbye covers the
// ErrWrongForkDigestVersion branch: the handler responds with its own
// status, closes the stream, then sends a goodbye and disconnects.
func TestStatusRPCHandlerForkDigestMismatchSendsGoodbye(t *testing.T) {
	svc, fp, _ := newStatusTestService(t, 50)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 10)

	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()
	_ = server.SetProtocol(protocol.ID(p2p.RPCStatusTopicV1 + fp.Encoding().ProtocolSuffix()))

	// sendGoodByeAndDisconnect (triggered by the fork digest mismatch) needs
	// a Host reporting Connected so it actually sends+disconnects, plus a
	// second stream for its outbound goodbye message.
	fp.realHost = &fakeConnectednessHost{state: network.Connected}
	gbClient, gbServer := newFakeStreamPair(fp.self, remote)
	defer gbClient.Close()
	fp.sendStream = gbServer
	go func() { _, _ = gbClient.Write([]byte{0}) }()

	badMsg := &sync_pb.Status{
		GenesisHash:   utils.ConvertHashToH256(types.Hash{0xFF}),
		CurrentHeight: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
	}

	done := make(chan error, 1)
	go func() { done <- svc.statusRPCHandler(context.Background(), badMsg, server) }()

	// Drain our status response.
	buf := make([]byte, 1)
	if _, err := client.Read(buf); err != nil {
		t.Fatalf("client read response code: %v", err)
	}
	var resp sync_pb.Status
	if err := fp.Encoding().DecodeWithMaxLength(client, &resp); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	// closeStreamAndWait(stream) blocks reading for our acknowledgement;
	// closing unblocks it immediately instead of waiting out the deadline.
	_ = client.Close()

	if err := <-done; err != nil {
		t.Fatalf("statusRPCHandler returned error: %v", err)
	}
	if len(fp.disconnected) == 0 {
		t.Fatal("expected a goodbye-triggered disconnect for a fork digest mismatch")
	}
}
