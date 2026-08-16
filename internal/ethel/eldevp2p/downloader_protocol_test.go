//go:build n42el

package eldevp2p

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
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

func TestInvalidAncestorObserverReportsRejectedBranchTip(t *testing.T) {
	rejectedHead := types.HexToHash("0x11")
	latestValid := types.HexToHash("0x33")
	d := &Downloader{}
	var gotRejected, gotLatest types.Hash
	d.SetInvalidAncestorObserver(func(rejected, latest types.Hash) {
		gotRejected, gotLatest = rejected, latest
	})
	d.reportInvalidAncestor(rejectedHead, latestValid)
	if gotRejected != rejectedHead || gotLatest != latestValid {
		t.Fatalf("observer got rejected=%s latest=%s, want rejected=%s latest=%s",
			gotRejected, gotLatest, rejectedHead, latestValid)
	}
}

func TestRejectedBranchTipStopsAtFirstDisconnectedHeader(t *testing.T) {
	h9 := &block.Header{Number: uint256.NewInt(9)}
	h10 := &block.Header{Number: uint256.NewInt(10), ParentHash: h9.Hash()}
	disconnected := &block.Header{Number: uint256.NewInt(11), ParentHash: types.HexToHash("0xff")}
	if got := rejectedBranchTip([]*block.Header{h9, h10, disconnected}, 0); got != h10.Hash() {
		t.Fatalf("rejectedBranchTip = %s, want %s", got, h10.Hash())
	}
}

func TestNewBlockHashesRefreshesPeerTip(t *testing.T) {
	announced := types.HexToHash("0x44")
	d := &Downloader{peers: map[string]*peerState{
		"peer": {head: 10, headHash: types.HexToHash("0x10")},
	}}
	d.OnNewBlockHashes("peer", eth69.NewBlockHashesPacket{
		{Hash: types.HexToHash("0x33"), Number: 11},
		{Hash: announced, Number: 12},
	})
	if got := d.peers["peer"]; got.head != 12 || got.headHash != announced {
		t.Fatalf("peer tip = (%d, %s), want (12, %s)", got.head, got.headHash, announced)
	}
}
