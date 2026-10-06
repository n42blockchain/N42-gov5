package sync

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/test"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/p2p/peers"
	"github.com/n42blockchain/N42/internal/p2p/peers/scorers"
)

func TestIsDigestValidAcceptsMatchingDigest(t *testing.T) {
	blockNr := uint256.NewInt(10)
	genesis := types.Hash{0xAA}
	digest, err := utils.CreateForkDigest(blockNr, genesis)
	if err != nil {
		t.Fatalf("compute digest: %v", err)
	}
	ok, err := isDigestValid(digest, blockNr, genesis)
	if err != nil {
		t.Fatalf("isDigestValid: %v", err)
	}
	if !ok {
		t.Fatal("expected a matching digest to validate")
	}
}

func TestIsDigestValidRejectsMismatchedDigest(t *testing.T) {
	blockNr := uint256.NewInt(10)
	genesis := types.Hash{0xAA}
	var wrong [4]byte
	wrong[0] = 0xFF
	ok, err := isDigestValid(wrong, blockNr, genesis)
	if err != nil {
		t.Fatalf("isDigestValid: %v", err)
	}
	if ok {
		t.Fatal("expected a mismatched digest to be rejected")
	}
}

func TestGenesisHashForStatusUsesOverride(t *testing.T) {
	override := types.Hash{0x01, 0x02}
	svc := &Service{cfg: &config{overrideGenesisHash: &override}}
	if got := svc.genesisHashForStatus(); got != override {
		t.Fatalf("genesisHashForStatus() = %s, want override %s", got, override)
	}
}

func TestCurrentForkDigestUsesChainHeadAndGenesis(t *testing.T) {
	override := types.Hash{0x01, 0x02}
	chain := &syncTBlockByHashChain{syncChainStub: syncChainStub{current: &syncBlockStub{number: uint256.NewInt(5)}}}
	svc := &Service{cfg: &config{chain: chain, overrideGenesisHash: &override}}
	digest, err := svc.currentForkDigest()
	if err != nil {
		t.Fatalf("currentForkDigest: %v", err)
	}
	want, err := utils.CreateForkDigest(uint256.NewInt(5), override)
	if err != nil {
		t.Fatalf("CreateForkDigest: %v", err)
	}
	if digest != want {
		t.Fatalf("currentForkDigest() = %x, want %x", digest, want)
	}
}

func TestMultiAddrEmptyForUnknownPeer(t *testing.T) {
	st := peers.NewStatus(context.Background(), &peers.StatusConfig{
		PeerLimit:    32,
		ScorerParams: &scorers.Config{},
	})
	pid, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	if got := multiAddr(pid, st); got != "" {
		t.Fatalf("multiAddr() = %q, want empty for an unregistered peer", got)
	}
}

func TestNoopValidatorRejectsUndecodableMessage(t *testing.T) {
	fp := newFakeP2P(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp}}

	topic := "/n42/totally/unknown/topic/1/ssz_snappy"
	msg := &pubsub.Message{Message: &pubsubpb.Message{Topic: &topic}}

	res, err := svc.noopValidator(context.Background(), peer.ID(""), msg)
	if err != nil {
		t.Fatalf("noopValidator returned error: %v", err)
	}
	if res != pubsub.ValidationReject {
		t.Fatalf("noopValidator result = %v, want ValidationReject for an undecodable topic", res)
	}
}

func TestWrapAndReportValidationRejectsNilTopic(t *testing.T) {
	fp := newFakeP2P(t)
	svc := &Service{ctx: context.Background(), cfg: &config{p2p: fp}}

	called := false
	_, wrapped := svc.wrapAndReportValidation("/n42/some/topic/1/ssz_snappy", func(context.Context, peer.ID, *pubsub.Message) (pubsub.ValidationResult, error) {
		called = true
		return pubsub.ValidationAccept, nil
	})

	// msg.Topic left nil deliberately: wrapAndReportValidation must reject
	// before ever calling the inner validator.
	msg := &pubsub.Message{Message: &pubsubpb.Message{}}
	res := wrapped(context.Background(), peer.ID(""), msg)
	if res != pubsub.ValidationReject {
		t.Fatalf("wrapAndReportValidation result = %v, want ValidationReject for a nil topic", res)
	}
	if called {
		t.Fatal("inner validator must not run when msg.Topic is nil")
	}
}
