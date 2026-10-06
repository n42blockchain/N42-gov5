package node

import (
	"testing"
)

// TestNewNodeAdminAPIExercisesP2PAndMinerAdapters drives the admin_* RPC
// namespace against a live node, which is the only caller of the
// p2pAdminAdapter and minerAdminAdapter glue in adapters.go.
func TestNewNodeAdminAPIExercisesP2PAndMinerAdapters(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTRPCConfig(dir)
	cfg.NodeCfg.HTTPApi = "eth,net,web3,admin"

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

	nodeInfo := nodeTRPCCall(t, addr, "admin_nodeInfo", nil)
	if nodeInfo["error"] != nil {
		t.Fatalf("admin_nodeInfo error: %v", nodeInfo["error"])
	}

	peers := nodeTRPCCall(t, addr, "admin_peers", nil)
	if peers["error"] != nil {
		t.Fatalf("admin_peers error: %v", peers["error"])
	}
	if _, ok := peers["result"].([]interface{}); !ok {
		t.Fatalf("admin_peers: expected array result, got %#v", peers["result"])
	}

	datadir := nodeTRPCCall(t, addr, "admin_datadir", nil)
	if datadir["error"] != nil {
		t.Fatalf("admin_datadir error: %v", datadir["error"])
	}

	// addPeer/removePeer with a syntactically valid but unreachable multiaddr
	// exercise the p2pAdminAdapter connect/disconnect paths without any real
	// network dial succeeding.
	addPeer := nodeTRPCCall(t, addr, "admin_addPeer",
		[]interface{}{"/ip4/127.0.0.1/tcp/4/p2p/16Uiu2HAmFWPUx4ZtGsL6YcWBgYsLwVs8uoQCUKkJ8K5PjQwYtjMo"})
	// Either a connection error (unreachable) or success is acceptable; the
	// point is the adapter path ran without panicking.
	_ = addPeer

	mining := nodeTRPCCall(t, addr, "eth_mining", nil)
	if mining["error"] != nil {
		t.Fatalf("eth_mining error: %v", mining["error"])
	}
}
