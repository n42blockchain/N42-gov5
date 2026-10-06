package node

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/ddn/gateway"
	"github.com/n42blockchain/N42/internal/ddn/provider"
	"github.com/n42blockchain/N42/internal/ddn/quorum"
	"github.com/n42blockchain/N42/internal/ddn/receipt"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func (n *Node) startDDNRuntime() {
	c := n.config.AICfg.DDN
	if !n.runtimePlan.startDistributed || !c.Enabled || !c.GatewayEnabled {
		return
	}
	backend, err := n.ddnProvider(c)
	if err != nil {
		log.Error("DDN disabled: invalid provider configuration", "err", err)
		return
	}
	if n.config.ChainCfg == nil || n.config.ChainCfg.ChainID == nil || !n.config.ChainCfg.ChainID.IsUint64() {
		log.Error("DDN disabled: unsupported chain ID")
		return
	}
	g, err := gateway.New(gateway.Config{MaxQuorum: uint32(c.QuorumSize), ChainID: n.config.ChainCfg.ChainID.Uint64(), ShadowMode: c.ShadowMode, MaxConcurrency: c.MaxConcurrency, QueueSize: c.QueueSize, MaxItems: c.MaxItems, MaxInputBytes: c.MaxInputBytes, MaxLatency: time.Duration(c.MaxLatencyMs) * time.Millisecond, ReceiptTTL: time.Duration(c.ReceiptTTLSec) * time.Second}, backend)
	if err != nil {
		log.Error("DDN disabled: invalid gateway configuration", "err", err)
		return
	}

	if c.SigningKeyFile != "" {
		path := c.SigningKeyFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(n.config.NodeCfg.DataDir, path)
		}
		signer, loadErr := receipt.LoadSigner(path, os.Getenv("N42_DDN_KEY_PASSWORD"))
		if loadErr != nil {
			g.Stop()
			log.Error("DDN disabled: receipt key unavailable")
			return
		}
		if err = g.SetSigner(signer); err != nil {
			signer.Close()
			g.Stop()
			log.Error("DDN disabled: signer identity mismatch")
			return
		}
		n.ddnSigner = signer
	}
	n.ddnGateway = g
	n.rpcAPIs = append(n.rpcAPIs, jsonrpc.API{Namespace: "n42", Service: &gateway.API{Gateway: g}, Authenticated: true})
	log.Info("DDN shadow gateway enabled", "workers", c.MaxConcurrency, "queue", c.QueueSize)
}

func (n *Node) ddnProvider(c conf.DDNCfg) (provider.DecisionProvider, error) {
	if c.QuorumSize < 1 || c.QuorumSize > 16 {
		return nil, errors.New("quorum size must be between 1 and 16")
	}
	build := func(sc conf.DDNSidecarCfg) (provider.DecisionProvider, error) {
		var modelHash types.Hash
		if err := modelHash.UnmarshalText([]byte(sc.ModelHash)); err != nil {
			return nil, errors.New("invalid pinned model hash")
		}
		backend, err := provider.NewSidecar(sc.URL, provider.Identity{DID: sc.ProviderDID, Model: sc.Model, ModelVersion: sc.ModelVersion, ModelHash: modelHash, Family: sc.ModelFamily, Tasks: sc.Tasks, Schemas: sc.Schemas}, time.Duration(c.MaxLatencyMs)*time.Millisecond)
		if err != nil {
			return nil, err
		}
		if !c.RequireRegisteredProvider {
			return backend, nil
		}
		if n.coprocessorService == nil {
			return nil, errors.New("provider registry unavailable")
		}
		var address types.Address
		if !strings.HasPrefix(sc.ProviderDID, "did:n42:") || address.UnmarshalText([]byte(strings.TrimPrefix(sc.ProviderDID, "did:n42:"))) != nil {
			return nil, errors.New("invalid registry provider DID")
		}
		adapter := &provider.CoprocessorAdapter{Registry: n.coprocessorService.Providers(), Address: address, Backend: backend, MinReputation: c.MinProviderReputation}
		if err = adapter.Eligible(); err != nil {
			return nil, err
		}
		return adapter, nil
	}
	if len(c.Sidecars) == 0 {
		if c.QuorumSize != 1 {
			return nil, errors.New("multi-provider quorum requires sidecars")
		}
		return build(conf.DDNSidecarCfg{URL: c.SidecarURL, ProviderDID: c.ProviderDID, Model: c.Model, ModelVersion: c.ModelVersion, ModelHash: c.ModelHash, ModelFamily: c.ModelFamily, Tasks: c.Tasks, Schemas: c.Schemas})
	}
	if len(c.Sidecars) != c.QuorumSize {
		return nil, errors.New("quorum size must match configured sidecars")
	}
	members := make([]quorum.Member, 0, len(c.Sidecars))
	for _, sc := range c.Sidecars {
		p, err := build(sc)
		if err != nil {
			return nil, err
		}
		members = append(members, quorum.Member{Provider: p, Price: sc.Price})
	}
	return quorum.New(c.ProviderDID, members, c.MaxProviderConcurrency)
}
