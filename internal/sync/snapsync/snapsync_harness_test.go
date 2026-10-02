package snapsync

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/p2p/peers"
	"github.com/n42blockchain/N42/internal/p2p/peers/scorers"
)

// snapsyncFakeP2P is a minimal p2p.P2P stand-in for the Service/Manager
// tests in this package, mirroring internal/sync/fake_p2p_test.go. It embeds
// the interface (nil) so any un-overridden method panics loudly.
type snapsyncFakeP2P struct {
	p2p.P2P
	peerStatus *peers.Status
	self       peer.ID
	cfg        *conf.P2PConfig

	sendStream network.Stream
	sendErr    error
}

func newSnapsyncFakeP2P(t *testing.T, cfg *conf.P2PConfig) *snapsyncFakeP2P {
	t.Helper()
	st := peers.NewStatus(context.Background(), &peers.StatusConfig{
		PeerLimit:    32,
		ScorerParams: &scorers.Config{},
	})
	self, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	if cfg == nil {
		cfg = &conf.P2PConfig{}
	}
	return &snapsyncFakeP2P{peerStatus: st, self: self, cfg: cfg}
}

func (f *snapsyncFakeP2P) Peers() *peers.Status       { return f.peerStatus }
func (f *snapsyncFakeP2P) PeerID() peer.ID            { return f.self }
func (f *snapsyncFakeP2P) GetConfig() *conf.P2PConfig { return f.cfg }

func (f *snapsyncFakeP2P) Send(_ context.Context, _ interface{}, _ string, _ peer.ID) (network.Stream, error) {
	return f.sendStream, f.sendErr
}

var _ p2p.P2P = (*snapsyncFakeP2P)(nil)
