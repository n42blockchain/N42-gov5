package node

import (
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
	"math/big"
	"testing"
)

func TestDDNRuntimeOptInAndAuthentication(t *testing.T) {
	cfg := &conf.Config{AICfg: conf.DefaultAICfg(), ChainCfg: &params.ChainConfig{ChainID: big.NewInt(94)}}
	n := &Node{config: cfg, runtimePlan: auxiliaryRuntimePlan{startDistributed: true}}
	n.startDDNRuntime()
	if n.ddnGateway != nil {
		t.Fatal("default config started DDN")
	}
	cfg.AICfg.DDN.Enabled = true
	cfg.AICfg.DDN.GatewayEnabled = true
	cfg.AICfg.DDN.SidecarURL = "http://127.0.0.1:65530/decide"
	cfg.AICfg.DDN.ProviderDID = "did:n42:test"
	cfg.AICfg.DDN.ModelHash = "0x0100000000000000000000000000000000000000000000000000000000000000"
	n.startDDNRuntime()
	if n.ddnGateway == nil {
		t.Fatal("enabled gateway failed to start")
	}
	defer n.ddnGateway.Stop()
	if len(n.rpcAPIs) != 1 || n.rpcAPIs[0].Namespace != "n42" || !n.rpcAPIs[0].Authenticated {
		t.Fatal("DDN RPC must be authenticated")
	}
}
