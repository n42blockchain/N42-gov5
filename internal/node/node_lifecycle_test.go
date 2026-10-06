package node

import (
	"context"
	"testing"

	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/conf"
)

func nodeTMinimalConfig(dataDir string) *conf.Config {
	return &conf.Config{
		NodeCfg: conf.NodeConfig{
			Profile: "eth",
			Chain:   "private",
			DataDir: dataDir,
		},
		P2PCfg: &conf.P2PConfig{
			NoDiscovery: true,
			TCPPort:     0,
			UDPPort:     0,
			MaxPeers:    0,
		},
	}
}

func nodeTNewContext() *cli.Context {
	app := cli.NewApp()
	cctx := cli.NewContext(app, nil, nil)
	cctx.Context = context.Background()
	return cctx
}

func TestNewNodeStartAndStop(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)
	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}

	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if n.BlockChain() == nil {
		t.Fatal("expected blockchain to be set")
	}
	head := n.BlockChain().CurrentBlock()
	if head == nil || head.Number64().Uint64() != 0 {
		t.Fatalf("expected genesis head, got %+v", head)
	}

	if len(n.rpcAPIs) == 0 {
		t.Fatal("expected registered RPC APIs")
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}
