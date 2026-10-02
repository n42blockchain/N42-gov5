package sync

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"

	types "github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
)

// syncTBlockByHashChain is a syncChainStub extended with a hash index, enough
// to exercise blockByHashStreamHandler's found/not-found branches.
type syncTBlockByHashChain struct {
	syncChainStub
	byHash map[comtypes.Hash]types.IBlock
}

func (c *syncTBlockByHashChain) GetBlockByHash(h comtypes.Hash) (types.IBlock, error) {
	if c.byHash == nil {
		return nil, nil
	}
	return c.byHash[h], nil
}

func (c *syncTBlockByHashChain) GenesisBlock() types.IBlock {
	return syncTSmallBlock(0)
}

func newBlockByHashTestService(t *testing.T, chain *syncTBlockByHashChain) (*Service, *fakeP2P) {
	t.Helper()
	fp := newFakeP2P(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp, chain: chain}}
	svc.rateLimiter = newRateLimiter(fp)
	return svc, fp
}

func TestBlockByHashStreamHandlerNotFound(t *testing.T) {
	chain := &syncTBlockByHashChain{}
	svc, fp := newBlockByHashTestService(t, chain)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()

	var reqHash comtypes.Hash
	reqHash[0] = 0x42
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.blockByHashStreamHandler(server)
	}()
	if _, err := client.Write(reqHash[:]); err != nil {
		t.Fatalf("write request hash: %v", err)
	}

	// Drain the full error response (code + encoded message): net.Pipe's
	// Write blocks until every byte is read, so a partial read on the client
	// side would hang the handler goroutine forever.
	code, _, err := ReadStatusCode(client, fp.Encoding())
	if err != nil {
		t.Fatalf("ReadStatusCode: %v", err)
	}
	if code != responseCodeServerError {
		t.Fatalf("response code = %d, want serverError", code)
	}
	<-done
}

func TestBlockByHashStreamHandlerShortReadReturnsEarly(t *testing.T) {
	chain := &syncTBlockByHashChain{}
	svc, fp := newBlockByHashTestService(t, chain)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.blockByHashStreamHandler(server)
	}()
	// Close the client immediately: the handler's io.ReadFull(32 bytes) must
	// fail and return without writing anything or panicking.
	client.Close()
	<-done
}

func TestBlockByHashStreamHandlerServesFoundBlock(t *testing.T) {
	blk := syncTSmallBlock(7)
	hash := blk.Hash()
	chain := &syncTBlockByHashChain{byHash: map[comtypes.Hash]types.IBlock{hash: blk}}
	svc, fp := newBlockByHashTestService(t, chain)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.blockByHashStreamHandler(server)
	}()
	if _, err := client.Write(hash[:]); err != nil {
		t.Fatalf("write request hash: %v", err)
	}

	got, err := ReadChunkedBlock(client, fp, true)
	if err != nil {
		t.Fatalf("ReadChunkedBlock: %v", err)
	}
	if got.Hash() != blk.Hash() {
		t.Fatalf("got hash %s, want %s", got.Hash(), blk.Hash())
	}
	<-done
}
