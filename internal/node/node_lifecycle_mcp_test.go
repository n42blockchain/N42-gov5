package node

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
)

// nodeTFreeLoopbackPort finds a currently-unused loopback TCP port by
// binding and immediately releasing it. There is a small unavoidable race
// against the OS handing the port to someone else before our server binds
// it, acceptable for a short-lived local test.
func nodeTFreeLoopbackPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// nodeTMCPConfig builds a minimal N42-profile private-chain config (MCP is
// only supported on the N42 execution profile, not Ethereum-EL) with the
// MCP server enabled on a loopback port.
func nodeTMCPConfig(dataDir string, port int) *conf.Config {
	cfg := &conf.Config{
		NodeCfg: conf.NodeConfig{
			Chain:   "private",
			DataDir: dataDir,
		},
		P2PCfg: &conf.P2PConfig{
			NoDiscovery: true,
		},
		MCPCfg: conf.MCPCfg{
			Enabled: true,
			Host:    "127.0.0.1",
			Port:    port,
		},
	}
	return cfg
}

func TestNewNodeMCPServerStartsOnN42Profile(t *testing.T) {
	dir := t.TempDir()
	port := nodeTFreeLoopbackPort(t)
	cfg := nodeTMCPConfig(dir, port)

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if !n.runtimePlan.startMCPServer {
		t.Fatal("expected MCP runtime to be enabled for the N42 profile")
	}

	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if n.mcpServer == nil {
		t.Fatal("expected mcpServer to be constructed")
	}

	// The MCP server starts its HTTP listener in a goroutine; poll for the
	// port to accept connections instead of sleeping a fixed amount.
	addr := net.JoinHostPort("127.0.0.1", itoaMCP(port))
	deadline := time.Now().Add(2 * time.Second)
	var dialErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			dialErr = nil
			break
		}
		dialErr = err
		time.Sleep(20 * time.Millisecond)
	}
	if dialErr != nil {
		t.Fatalf("MCP server never accepted connections on %s: %v", addr, dialErr)
	}
}

func itoaMCP(port int) string {
	return fmt.Sprintf("%d", port)
}
