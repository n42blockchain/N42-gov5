package devp2p

import (
	"context"
	"testing"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/network/eth69"
)

func TestNewDownloaderStoresServer(t *testing.T) {
	srv := &Server{}
	d := NewDownloader(srv)
	if d.server != srv {
		t.Fatal("NewDownloader did not store the given server")
	}
}

func TestDownloaderRequestHeadersSendsGetBlockHeaders(t *testing.T) {
	d := NewDownloader(nil)
	peer := gethp2p.NewPeer(enode.ID{}, "downloader-test", nil)
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- d.RequestHeaders(context.Background(), peer, rw1, 100, 50) }()

	msg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Code != eth69.GetBlockHeadersMsg {
		t.Fatalf("msg code = %d, want %d", msg.Code, eth69.GetBlockHeadersMsg)
	}
	// Decode (consuming the payload) BEFORE waiting on errCh: WriteMsg blocks
	// until the payload is fully read, so reading errCh first would deadlock.
	var req eth69.GetBlockHeadersPacket
	if err := msg.Decode(&req); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if req.RequestID != 100 || req.Amount != 50 || req.Origin.Number != 100 {
		t.Fatalf("decoded request = %+v", req)
	}
}

func TestDownloaderRequestBodiesSendsGetBlockBodies(t *testing.T) {
	d := NewDownloader(nil)
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	hashes := []types.Hash{types.HexToHash("0x01"), types.HexToHash("0x02")}
	errCh := make(chan error, 1)
	go func() { errCh <- d.RequestBodies(context.Background(), rw1, hashes) }()

	msg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	if msg.Code != eth69.GetBlockBodiesMsg {
		t.Fatalf("msg code = %d, want %d", msg.Code, eth69.GetBlockBodiesMsg)
	}
	var req eth69.GetBlockBodiesPacket
	if err := msg.Decode(&req); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if len(req.Hashes) != 2 || req.Hashes[0] != hashes[0] || req.Hashes[1] != hashes[1] {
		t.Fatalf("decoded hashes = %v, want %v", req.Hashes, hashes)
	}
}
