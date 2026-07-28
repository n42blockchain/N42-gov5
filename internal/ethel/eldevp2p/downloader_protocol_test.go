//go:build n42el

package eldevp2p

import (
	"testing"

	"github.com/n42blockchain/N42/internal/network/eth69"
)

func TestBALPeerSelectionRequiresETH71(t *testing.T) {
	d := &Downloader{peers: map[string]*peerState{
		"eth69": {head: 100, version: eth69.ETH69},
		"eth71": {head: 100, version: eth69.ETH71},
	}}
	pp := d.pickPeerWithMinVersion(50, eth69.ETH71)
	if pp == nil {
		t.Fatal("no ETH71 peer selected")
	}
	defer d.releasePeer(pp)
	if pp.id != "eth71" {
		t.Fatalf("selected %q for BAL request, want eth71", pp.id)
	}
}

func TestBALPeerSelectionRejectsLegacyOnlyPool(t *testing.T) {
	d := &Downloader{peers: map[string]*peerState{
		"eth68": {head: 100, version: eth69.ETH68},
		"eth70": {head: 100, version: eth69.ETH70},
	}}
	if pp := d.pickPeerWithMinVersion(50, eth69.ETH71); pp != nil {
		t.Fatalf("selected legacy peer %q for BAL request", pp.id)
	}
}
