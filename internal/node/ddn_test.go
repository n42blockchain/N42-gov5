package node

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
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

func TestDDNQuorumConfiguration(t *testing.T) {
	c := conf.DefaultAICfg().DDN
	c.ProviderDID = "did:n42:aggregate"
	c.QuorumSize = 2
	n := &Node{}
	if _, err := n.ddnProvider(c); err == nil {
		t.Fatal("missing quorum sidecars accepted")
	}
	for i := 0; i < 2; i++ {
		c.Sidecars = append(c.Sidecars, conf.DDNSidecarCfg{URL: "http://127.0.0.1:65530/decide", ProviderDID: fmt.Sprintf("did:n42:provider%d", i), ModelHash: fmt.Sprintf("0x%064x", i+1), ModelFamily: fmt.Sprintf("family%d", i), Tasks: c.Tasks, Schemas: c.Schemas})
	}
	p, err := n.ddnProvider(c)
	if err != nil {
		t.Fatal(err)
	}
	if p.Identity().Model != "DDN-quorum-v1" {
		t.Fatal("group not wired")
	}
	c.RequireRegisteredProvider = true
	if _, err = n.ddnProvider(c); err == nil {
		t.Fatal("registry requirement silently bypassed")
	}
}
