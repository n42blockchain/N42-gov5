package sync

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/internal/p2p"
)

// newRegisterTestService builds a bare Service (no NewService side effects)
// over a real in-process host, for exercising registerRPCHandlers and
// unregisterHandlers in isolation.
func newRegisterTestService(t *testing.T) (*Service, *fakeP2P) {
	t.Helper()
	fp := newFakeP2PWithHost(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp}}
	svc.rateLimiter = newRateLimiter(fp)
	return svc, fp
}

// TestRegisterRPCHandlersInstallsAllProtocols verifies every RPC topic (plus
// the two raw stream handlers) ends up registered on the host, and that
// unregisterHandlers removes them again.
func TestRegisterRPCHandlersInstallsAllProtocols(t *testing.T) {
	svc, fp := newRegisterTestService(t)
	svc.registerRPCHandlers()

	suffix := fp.Encoding().ProtocolSuffix()
	want := []string{
		p2p.RPCStatusTopicV1, p2p.RPCGoodByeTopicV1, p2p.RPCPingTopicV1,
		p2p.RPCBodiesDataTopicV1, p2p.RPCBlobSidecarsByRangeTopicV1,
		p2p.RPCBlobSidecarsByRootTopicV1, p2p.RPCGetAccountRangeTopicV1,
		p2p.RPCGetStorageRangeTopicV1, p2p.RPCGetCodeTopicV1,
		p2p.RPCGetBlockWitnessTopicV1, p2p.RPCGetSnapshotInfoTopicV1,
		p2p.RPCGetSnapshotAccountRangeTopicV1, p2p.RPCGetSnapshotStorageRangeTopicV1,
		p2p.RPCGetChangeSetRangeTopicV1, p2p.RPCBlockPushTopicV1, p2p.RPCBlockByHashTopicV1,
	}

	installed := make(map[string]bool)
	for _, proto := range fp.realHost.Mux().Protocols() {
		installed[string(proto)] = true
	}
	for _, topic := range want {
		if !installed[topic+suffix] {
			t.Errorf("protocol %s not registered after registerRPCHandlers", topic+suffix)
		}
	}

	svc.unregisterHandlers()
	remaining := fp.realHost.Mux().Protocols()
	for _, proto := range remaining {
		for _, topic := range want {
			if string(proto) == topic+suffix {
				t.Errorf("protocol %s still registered after unregisterHandlers", proto)
			}
		}
	}
}
