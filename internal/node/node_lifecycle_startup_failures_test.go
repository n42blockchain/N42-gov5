package node

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
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

func TestTxIndexTailStopsWhenNodeClosesBeforeStart(t *testing.T) {
	t.Setenv("N42_TXINDEX_TAIL", "1")
	dir := t.TempDir()
	cfg := nodeTMinimalConfig(dir)
	first, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.txIndexer == nil {
		t.Fatal("enabled transaction lookup tier did not start")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening in the same process verifies that the first indexer detached
	// its exclusive resolver and closed its sealer before the database closed.
	second, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("reopen after pre-start close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("N42_TXINDEX_TAIL", "0")
	if off, err := NewNode(nodeTNewContext(), cfg); err == nil || off != nil || !strings.Contains(err.Error(), "previously enabled") {
		if off != nil {
			_ = off.Close()
		}
		t.Fatalf("disabled tier after persistent watermark must fail closed: (%v, %v)", off, err)
	}
}

func TestTxIndexFatalBeforeStartCannotBeClearedByRetry(t *testing.T) {
	t.Setenv("N42_TXINDEX_TAIL", "1")
	t.Setenv("N42_TXINDEX_SEAL_MIN_TX", "1")
	t.Setenv("N42_TXINDEX_KEEP_BLOCKS", "1")
	dir := t.TempDir()
	n, err := NewNode(nodeTNewContext(), nodeTMinimalConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if n.txIndexer == nil {
		t.Fatal("enabled transaction lookup tier did not start")
	}
	// Corrupt the index after its initial reader opened. The next seal fails
	// during publication, so a normal node must stop rather than retry an
	// ambiguous committed prefix or continue growing its in-memory tail.
	if err := os.WriteFile(filepath.Join(dir, "txindex", "txindex.cidx"), []byte{1}, 0600); err != nil {
		t.Fatal(err)
	}
	for number := uint64(1); number <= 3; number++ {
		n.txIndexer.Add(number, []types.Hash{{byte(number)}})
	}
	deadline := time.After(8 * time.Second)
	for n.txIndexer.FatalError() == nil {
		select {
		case <-deadline:
			t.Fatal("expected a bounded fatal publication error")
		case <-time.After(25 * time.Millisecond):
		}
	}
	if err := n.Start(); err == nil {
		t.Fatal("Start accepted an indexer with a pending fatal error")
	}
	if err := n.Start(); err == nil {
		t.Fatal("retry accepted an indexer whose fatal notification was consumed")
	}
	select {
	case <-n.shutDown:
	case <-time.After(8 * time.Second):
		t.Fatal("fatal-before-start did not close the node and drain the indexer")
	}
}
