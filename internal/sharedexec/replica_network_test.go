package sharedexec_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func replicaNetworkKey(t *testing.T) (string, peer.ID) {
	t.Helper()
	key, _, err := libcrypto.GenerateSecp256k1Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := key.Raw()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "p2p-key")
	if err := os.WriteFile(file, []byte(hex.EncodeToString(raw)), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return file, id
}

func TestReplicaNetworkFetchesAvailabilityThenCertifiedCatchup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	fixture := sharedexec.NewExecutionFixtureForTest(t)
	server, err := sharedexec.New(fixture.Chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	socket := sharedexec.StartUnixExecutionForTest(t, server)
	client := sharedexec.NewClient(socket, server.Identity())
	defer client.Close()
	b := signedFixtureBlock(t, fixture, client)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	keyA, idA := replicaNetworkKey(t)
	keyB, idB := replicaNetworkKey(t)
	makeHost := func(key string, other peer.ID) *p2p.Service {
		cfg := &conf.P2PConfig{PrivateKey: key, DataDir: t.TempDir(), TCPPort: 0, UDPPort: 0, MaxPeers: 10, LocalIP: "127.0.0.1", StaticPeers: []string{"/ip4/127.0.0.1/tcp/1/p2p/" + other.String()}}
		pub, err := p2p.NewService(ctx, fixture.Chain.GenesisBlock().Hash(), cfg, conf.NodeConfig{Chain: "private", Profile: "n42"}, p2p.WithPerformanceChain(fixture.Chain.Config()))
		if err != nil {
			t.Fatal(err)
		}
		pub.Start()
		t.Cleanup(func() { _ = pub.Stop(); _ = pub.Host().Close() })
		return pub
	}
	a := makeHost(keyA, idB)
	bb := makeHost(keyB, idA)
	if err := a.Host().Connect(ctx, peer.AddrInfo{ID: idB, Addrs: bb.Host().Addrs()}); err != nil {
		t.Fatal(err)
	}
	makeReplica := func(pub *p2p.Service) (*sharedvalidator.Replica, *sharedvalidator.Network, *hotstuff.Service) {
		db := memdb.New(t.TempDir())
		t.Cleanup(db.Close)
		remote := sharedexec.NewClient(socket, server.Identity())
		t.Cleanup(remote.Close)
		r, err := sharedvalidator.New(ctx, db, fixture.Chain.Config(), fixture.Chain.GenesisBlock().(*block.Block), fixture.Public, remote)
		if err != nil {
			t.Fatal(err)
		}
		network, err := sharedvalidator.NewNetwork(ctx, r, pub)
		if err != nil {
			t.Fatal(err)
		}
		service := hotstuff.NewService(fixture.Public, nil, db, "unused", "unused")
		t.Cleanup(service.Stop)
		if err := network.AttachService(service); err != nil {
			t.Fatal(err)
		}
		if err := network.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(network.Stop)
		return r, network, service
	}
	origin, originNetwork, originService := makeReplica(a)
	receiver, network, _ := makeReplica(bb)
	if result, err := origin.Import(ctx, raw); err != nil || !result.Durable {
		t.Fatalf("origin remote import: %v", err)
	}
	network.FetchBlockByHash(b.Hash())
	wait := func(check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatal("network convergence timed out")
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	wait(func() bool { return receiver.BlockApplied(b.Hash(), 1) })
	if receiver.CurrentBlock().Hash() != fixture.Chain.GenesisBlock().Hash() {
		t.Fatal("availability fetch became finality")
	}
	if err := originService.CommitCertificate(ctx, replicaQC(fixture.Keys, b.Hash(), 5)); err != nil {
		t.Fatal(err)
	}
	network.CatchUp()
	wait(func() bool { return receiver.CurrentBlock().Hash() == b.Hash() })
	if network.HeightBehind() != 0 || server.ImportCalls() != 1 {
		t.Fatal("certified catchup failed or repeated execution")
	}
	for _, connection := range a.Host().Network().ConnsToPeer(idB) {
		if string(connection.ConnState().Security) != a.WireTopic("/noise") {
			t.Fatal("block RPC bypassed genesis-bound Noise")
		}
	}
	network.FetchBlockByHash(types.Hash{99})
	if receiver.BlockApplied(types.Hash{99}, 1) {
		t.Fatal("missing body claimed execution")
	}
	// A peer holding an incomplete request cannot delay Stop for its ten
	// second stream deadline; Stop resets tracked streams and drains workers.
	held, err := bb.Host().NewStream(ctx, idA, protocol.ID("/n42/shared-validator/blocks/1"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if _, err := held.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { originNetwork.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not reset/drain incomplete stream")
	}
}
