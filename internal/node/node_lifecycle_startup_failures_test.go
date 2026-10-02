package node

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// TestNewNodeBadDatadirPermissionsFails confirms that NewNode surfaces an
// error, rather than panicking, when the datadir cannot be created because
// its parent is not writable.
func TestNewNodeBadDatadirPermissionsFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root: permission bits do not block access")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o000); err != nil {
		t.Fatalf("chmod parent: %v", err)
	}
	defer os.Chmod(parent, 0o700) // restore so t.TempDir() cleanup can remove it

	dataDir := filepath.Join(parent, "datadir")
	cfg := nodeTMinimalConfig(dataDir)

	n, err := NewNode(nodeTNewContext(), cfg)
	if err == nil {
		if n != nil {
			n.Close()
		}
		t.Fatal("expected NewNode to fail against an unwritable datadir parent")
	}
}

// TestNewNodeHTTPPortAlreadyInUseFails confirms Start() returns an error
// instead of silently continuing when the configured HTTP RPC port is
// already bound by another listener.
func TestNewNodeHTTPPortAlreadyInUseFails(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a port: %v", err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)
	cfg.NodeCfg.HTTP = true
	cfg.NodeCfg.HTTPHost = "127.0.0.1"
	cfg.NodeCfg.HTTPPort = strconv.Itoa(port)
	cfg.NodeCfg.HTTPApi = "eth,net,web3"

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if err := n.Start(); err == nil {
		t.Fatal("expected Start to fail: HTTP port already in use")
	}
}

// TestNewNodeInconsistentStateSchemeFails confirms that reopening a private
// chain's datadir under a different execution profile -- which implies a
// different state commitment scheme (QMDB for N42 vs Ethereum-MPT for the
// eth profile) -- is rejected rather than silently corrupting the stored
// chain config. This exercises the StateScheme immutability guard in
// NewNode rather than constructing a node successfully.
func TestNewNodeInconsistentStateSchemeFails(t *testing.T) {
	dir := t.TempDir()

	n42Cfg := nodeTMCPConfig(dir, 0)
	n42Cfg.MCPCfg.Enabled = false // only need genesis bootstrap, not the server
	first, err := NewNode(nodeTNewContext(), n42Cfg)
	if err != nil {
		t.Fatalf("first NewNode (N42 profile) failed: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing first node: %v", err)
	}

	ethCfg := nodeTMinimalConfig(dir) // profile "eth", same datadir/chain
	_, err = NewNode(nodeTNewContext(), ethCfg)
	if err == nil {
		t.Fatal("expected a state scheme mismatch error when reopening under a different profile")
	}
}
