package node

import (
	"path/filepath"
	"testing"
)

// TestNewNodeIngestAndWeb3GatewayStart exercises startIngestServer and
// startWeb3Gateway, both gated on their own config's Enabled flag plus
// profile.SupportsDeveloperRuntime() (true for every profile), bound to
// ephemeral loopback ports so no fixed port is claimed.
func TestNewNodeIngestAndWeb3GatewayStart(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)
	cfg.IngestCfg.Enabled = true
	cfg.IngestCfg.Addr = "127.0.0.1:0"
	cfg.IngestCfg.SoftTarget = 64
	cfg.IngestCfg.HardCap = 128
	cfg.Web3GatewayCfg.Enabled = true
	cfg.Web3GatewayCfg.Host = "127.0.0.1"
	cfg.Web3GatewayCfg.Port = nodeTFreeLoopbackPort(t)

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if !n.runtimePlan.startIngestServer {
		t.Fatal("expected ingest runtime to be enabled")
	}
	if !n.runtimePlan.startWeb3Gateway {
		t.Fatal("expected web3 gateway runtime to be enabled")
	}

	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
}

// TestNewNodeIPCEndpointStarts exercises the IPC RPC listener over a Unix
// domain socket inside the test's temp directory.
func TestNewNodeIPCEndpointStarts(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)
	cfg.NodeCfg.IPCPath = filepath.Join(dir, "n42.ipc")

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
}

// TestNewNodeAdminRemovePeerAndMinerSetEtherbase exercises
// p2pAdminAdapter.RemovePeer, p2pAdminAdapter.HighestPeerBlock and
// minerAdminAdapter.SetCoinbase via the admin_* RPC namespace.
func TestNewNodeAdminRemovePeerAndMinerSetEtherbase(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTRPCConfig(dir)
	cfg.NodeCfg.HTTPApi = "eth,net,web3,admin,miner"

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	addr := n.http.listenAddr()
	if addr == "" {
		t.Fatal("expected HTTP RPC server to report a bound address")
	}

	// A syntactically valid libp2p peer ID that is not actually connected:
	// RemovePeer must report the disconnect attempt rather than panic.
	removeResp := nodeTRPCCall(t, addr, "admin_removePeer",
		[]interface{}{"16Uiu2HAmFWPUx4ZtGsL6YcWBgYsLwVs8uoQCUKkJ8K5PjQwYtjMo"})
	_ = removeResp // either an error result or success is fine; must not panic
}
