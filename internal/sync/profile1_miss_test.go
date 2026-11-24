package sync

import (
	"math/big"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/test"

	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// setProfile1ForTest switches the package's profile flag and restores it.
func setProfile1ForTest(t *testing.T, on bool) {
	t.Helper()
	prev := profile1Chain.Load()
	t.Cleanup(func() { profile1Chain.Store(prev) })
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	if on {
		cfg.N42Profile = params.N42Profile1
	}
	ConfigureProfile1(cfg)
}

// Profile 1 (standard §3): a block_by_hash miss is answered with the single
// encoding 0x03 RESOURCE_UNAVAILABLE, and the client reads it as a miss.
func TestBlockByHashMissIsResourceUnavailableOnProfile1(t *testing.T) {
	setProfile1ForTest(t, true)
	svc, fp := newBlockByHashTestService(t, &syncTBlockByHashChain{})
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
	_, err = ReadChunkedBlock(client, fp, true)
	<-done
	if err == nil {
		t.Fatal("expected a miss error")
	}
	if !strings.Contains(err.Error(), "error code 3") {
		t.Fatalf("got %q, want the 0x03 RESOURCE_UNAVAILABLE miss", err)
	}
	if strings.Contains(err.Error(), "failed to read status code") {
		t.Fatalf("miss reported as a transport fault: %v", err)
	}
}

// Outside the profile the legacy 0x02 encoding is kept.
func TestBlockByHashMissCodeLegacyChain(t *testing.T) {
	setProfile1ForTest(t, false)
	if got := blockByHashMissCode(); got != responseCodeServerError {
		t.Fatalf("legacy miss code %#x, want 0x02", got)
	}
	setProfile1ForTest(t, true)
	if got := blockByHashMissCode(); got != responseCodeResourceUnavailable {
		t.Fatalf("profile-1 miss code %#x, want 0x03", got)
	}
}
