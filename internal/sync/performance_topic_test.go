package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p"
)

func TestPerformanceSyncWireTopicLifecycleAndDecode(t *testing.T) {
	raw, err := os.ReadFile("../../scripts/gov5-perf/isolated-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		t.Fatal(err)
	}
	network, err := p2p.NewService(context.Background(), types.Hash{1},
		&conf.P2PConfig{LocalIP: "127.0.0.1", DataDir: t.TempDir(), MaxPeers: 8},
		conf.NodeConfig{Chain: "private", Profile: "n42"}, p2p.WithPerformanceChain(genesis.Config))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = network.Stop(); _ = network.Host().Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	svc := &Service{ctx: ctx, cancel: cancel, cfg: &config{p2p: network}, subHandler: newSubTopicHandler()}
	digest := [4]byte{1, 2, 3, 4}
	base := fmt.Sprintf(p2p.BlockTopicFormat, digest)
	logical := base + network.Encoding().ProtocolSuffix()
	wire := p2p.WireTopic(network, logical)
	sub := svc.subscribeWithBase(base, svc.noopValidator, func(context.Context, any) error { return nil })
	if sub == nil || sub.Topic() != wire || !svc.subHandler.topicExists(wire) {
		t.Fatal("sync subscription was not bound to wire namespace")
	}
	if svc.subscribeWithBase(base, svc.noopValidator, func(context.Context, any) error { return nil }) != nil {
		t.Fatal("duplicate scoped subscription was not detected")
	}
	var encoded bytes.Buffer
	if _, err := network.Encoding().EncodeGossip(&encoded, &rawSSZBytes{data: []byte("payload")}); err != nil {
		t.Fatal(err)
	}
	decoded, err := svc.decodePubsubMessage(&pubsub.Message{Message: &pb.Message{Topic: &wire, Data: encoded.Bytes()}})
	if err != nil || string(decoded.(*rawSSZBytes).data) != "payload" {
		t.Fatalf("scoped topic lost its decoder: %v", err)
	}
	svc.unSubscribeFromTopic(logical)
	if svc.subHandler.topicExists(wire) || len(network.PubSub().GetTopics()) != 0 {
		t.Fatal("scoped unsubscribe leaked topic or subscription")
	}
	// Registration must succeed again: a leaked topic validator would reject it.
	again := svc.subscribeWithBase(base, svc.noopValidator, func(context.Context, any) error { return nil })
	if again == nil {
		t.Fatal("scoped topic validator was not unregistered")
	}
	svc.unSubscribeFromTopic(wire)
	cancel()
	svc.wg.Wait()
}
