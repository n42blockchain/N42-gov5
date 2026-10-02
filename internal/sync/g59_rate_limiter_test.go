package sync

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/internal/p2p/p2ptypes"
)

// TestValidateRawRpcRequestAllowsThenRejects exercises
// limiter.validateRawRpcRequest's happy path (a fresh peer has budget) and
// its rejection path (budget exhausted triggers rejectStream, which scores
// the peer and writes an error response over the stream).
func TestValidateRawRpcRequestAllowsThenRejects(t *testing.T) {
	fp := newFakeP2P(t)
	l := newRateLimiter(fp)

	local, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	fp.addConnectedPeer(t, remote, 0)

	client, server := newFakeStreamPair(local, remote)
	defer client.Close()
	defer server.Close()

	// rpcLimiterTopic collector: rate 5, burst 10 (defaultBurstLimit*2).
	// Drain the burst, then one more request must be rejected.
	for i := 0; i < 10; i++ {
		if err := l.validateRawRpcRequest(server); err != nil {
			t.Fatalf("validateRawRpcRequest() request %d: unexpected error %v", i, err)
		}
		l.addRawStream(server)
	}
	// net.Pipe is unbuffered: rejectStream's write blocks until someone reads,
	// so the read must run concurrently with the call that triggers it.
	readErrCh := make(chan error, 1)
	readBuf := make([]byte, 64)
	go func() {
		_, err := client.Read(readBuf)
		readErrCh <- err
	}()

	if err := l.validateRawRpcRequest(server); err != p2ptypes.ErrRateLimited {
		t.Fatalf("validateRawRpcRequest() after budget exhausted = %v, want ErrRateLimited", err)
	}

	if err := <-readErrCh; err != nil {
		t.Fatalf("client read after rejectStream: %v", err)
	}
	if readBuf[0] != responseCodeInvalidRequest {
		t.Fatalf("response code = %x, want %x", readBuf[0], responseCodeInvalidRequest)
	}
}

// TestValidateRawRpcRequestTrustedPeerBypassesLimit covers the
// IsTrusted short-circuit in validateRawRpcRequest.
func TestValidateRawRpcRequestTrustedPeerBypassesLimit(t *testing.T) {
	fp := newFakeP2P(t)
	l := newRateLimiter(fp)

	local, _ := test.RandPeerID()
	remote, _ := test.RandPeerID()
	fp.addConnectedPeer(t, remote, 0)
	fp.peerStatus.SetTrusted([]peer.ID{remote})

	_, server := newFakeStreamPair(local, remote)
	defer server.Close()

	for i := 0; i < 20; i++ {
		if err := l.validateRawRpcRequest(server); err != nil {
			t.Fatalf("trusted peer request %d unexpectedly rate limited: %v", i, err)
		}
	}
}
