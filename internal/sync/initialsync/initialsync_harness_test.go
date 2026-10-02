package initialsync

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

// initialsyncFakeP2P is a minimal p2p.P2P stand-in for the blocksFetcher
// tests in this package, mirroring internal/sync/fake_p2p_test.go. It embeds
// the interface (nil) so any un-overridden method panics loudly.
type initialsyncFakeP2P struct {
	p2p.P2P
	peerStatus *peers.Status
	self       peer.ID
	cfg        *conf.P2PConfig

	sendStream network.Stream
	sendErr    error
}

func newInitialsyncFakeP2P(t *testing.T, cfg *conf.P2PConfig) *initialsyncFakeP2P {
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
	return &initialsyncFakeP2P{peerStatus: st, self: self, cfg: cfg}
}

func (f *initialsyncFakeP2P) Peers() *peers.Status       { return f.peerStatus }
func (f *initialsyncFakeP2P) PeerID() peer.ID            { return f.self }
func (f *initialsyncFakeP2P) GetConfig() *conf.P2PConfig { return f.cfg }

func (f *initialsyncFakeP2P) Send(_ context.Context, _ interface{}, _ string, _ peer.ID) (network.Stream, error) {
	return f.sendStream, f.sendErr
}

var _ p2p.P2P = (*initialsyncFakeP2P)(nil)
