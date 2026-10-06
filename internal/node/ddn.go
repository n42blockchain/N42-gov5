package node

import (
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ddn/gateway"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func (n *Node) startDDNRuntime() {
	c := n.config.AICfg.DDN
	if !n.runtimePlan.startDistributed || !c.Enabled || !c.GatewayEnabled {
		return
	}
	var modelHash types.Hash
	if err := modelHash.UnmarshalText([]byte(c.ModelHash)); err != nil {
		log.Error("DDN disabled: invalid pinned model hash")
		return
	}
	p, err := provider.NewSidecar(c.SidecarURL, provider.Identity{DID: c.ProviderDID, Model: c.Model, ModelVersion: c.ModelVersion, ModelHash: modelHash, Family: c.ModelFamily, Tasks: c.Tasks, Schemas: c.Schemas}, time.Duration(c.MaxLatencyMs)*time.Millisecond)
	if err != nil {
		log.Error("DDN disabled: invalid sidecar configuration", "err", err)
		return
	}
	if n.config.ChainCfg == nil || n.config.ChainCfg.ChainID == nil || !n.config.ChainCfg.ChainID.IsUint64() {
		log.Error("DDN disabled: unsupported chain ID")
		return
	}
	g, err := gateway.New(gateway.Config{ChainID: n.config.ChainCfg.ChainID.Uint64(), ShadowMode: c.ShadowMode, MaxConcurrency: c.MaxConcurrency, QueueSize: c.QueueSize, MaxItems: c.MaxItems, MaxInputBytes: c.MaxInputBytes, MaxLatency: time.Duration(c.MaxLatencyMs) * time.Millisecond, ReceiptTTL: time.Duration(c.ReceiptTTLSec) * time.Second}, p)
	if err != nil {
		log.Error("DDN disabled: invalid gateway configuration", "err", err)
		return
	}
	n.ddnGateway = g
	n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{Namespace: "n42", Service: &gateway.API{Gateway: g}, Authenticated: true})
	log.Info("DDN shadow gateway enabled", "workers", c.MaxConcurrency, "queue", c.QueueSize)
}
