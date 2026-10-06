package node

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/n42blockchain/N42/conf"
)

// nodeTRPCConfig builds the minimal private-chain config with the public
// HTTP JSON-RPC server enabled on an ephemeral loopback port.
func nodeTRPCConfig(dataDir string) *conf.Config {
	cfg := nodeTMinimalConfig(dataDir)
	cfg.NodeCfg.HTTP = true
	cfg.NodeCfg.HTTPHost = "127.0.0.1"
	cfg.NodeCfg.HTTPPort = "0"
	cfg.NodeCfg.HTTPApi = "eth,net,web3"
	return cfg
}

func nodeTRPCCall(t *testing.T, addr, method string, params []interface{}) map[string]interface{} {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post("http://"+addr, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("http post to %s: %v", method, err)
	}
	defer resp.Body.Close()
	var out map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return out
}

func TestNewNodeRPCEnabledChainIDAndBlockNumber(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTRPCConfig(dir)

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

	chainIDResp := nodeTRPCCall(t, addr, "eth_chainId", nil)
	if chainIDResp["error"] != nil {
		t.Fatalf("eth_chainId error: %v", chainIDResp["error"])
	}
	if _, ok := chainIDResp["result"].(string); !ok {
		t.Fatalf("eth_chainId: expected string result, got %#v", chainIDResp["result"])
	}

	blockNumResp := nodeTRPCCall(t, addr, "eth_blockNumber", nil)
	if blockNumResp["error"] != nil {
		t.Fatalf("eth_blockNumber error: %v", blockNumResp["error"])
	}
	if blockNumResp["result"] != "0x0" {
		t.Fatalf("eth_blockNumber: expected 0x0 at genesis, got %#v", blockNumResp["result"])
	}
}
