// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package api

import (
	"context"
	"time"

	"github.com/n42blockchain/N42/internal/api/filters"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/utils"
)

// =============================================================================
// API Router
// =============================================================================

// Router manages the registration of JSON-RPC API namespaces.
// It acts as a gateway that routes requests to the appropriate handler.
//
// Architecture:
//
//	┌─────────────────────────────────────────────────────────┐
//	│                      Router                             │
//	│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐    │
//	│  │   eth    │ │   web3   │ │   net    │ │  debug   │    │
//	│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘    │
//	│       │            │            │            │          │
//	│       └────────────┴────────────┴────────────┘          │
//	│                         │                               │
//	│                    ┌────┴────┐                          │
//	│                    │ Backend │                          │
//	│                    └─────────┘                          │
//	└─────────────────────────────────────────────────────────┘
type Router struct {
	api     *API
	metrics *RPCMetrics

	// Feature flags for namespace enablement
	enableEth      bool
	enableN42      bool
	enableDebug    bool
	enableNet      bool
	enableWeb3     bool
	enableTxPool   bool
	enableAdmin    bool
	enablePersonal bool
	enableMiner    bool
	enableRPC      bool
}

// RouterConfig holds configuration for the API router.
type RouterConfig struct {
	// Feature flags
	EnableEth      bool
	EnableN42      bool
	EnableDebug    bool
	EnableNet      bool
	EnableWeb3     bool
	EnableTxPool   bool
	EnableAdmin    bool
	EnablePersonal bool
	EnableMiner    bool
	EnableRPC      bool

	// Metrics configuration
	MetricsLogInterval time.Duration
}

// DefaultRouterConfig returns the default router configuration.
func DefaultRouterConfig() *RouterConfig {
	return &RouterConfig{
		EnableEth:          true,
		EnableN42:          true,
		EnableDebug:        true,
		EnableNet:          true,
		EnableWeb3:         true,
		EnableTxPool:       true,
		EnableAdmin:        true,
		EnablePersonal:     false, // Disabled by default for security
		EnableMiner:        true,
		EnableRPC:          true,
		MetricsLogInterval: 60 * time.Second,
	}
}

// NewRouter creates a new API router.
func NewRouter(api *API, config *RouterConfig) *Router {
	if config == nil {
		config = DefaultRouterConfig()
	}

	return &Router{
		api:            api,
		metrics:        NewRPCMetrics(),
		enableEth:      config.EnableEth,
		enableN42:      config.EnableN42,
		enableDebug:    config.EnableDebug,
		enableNet:      config.EnableNet,
		enableWeb3:     config.EnableWeb3,
		enableTxPool:   config.EnableTxPool,
		enableAdmin:    config.EnableAdmin,
		enablePersonal: config.EnablePersonal,
		enableMiner:    config.EnableMiner,
		enableRPC:      config.EnableRPC,
	}
}

// APIs returns all registered JSON-RPC APIs.
// This method returns the same APIs as API.Apis() but through the Router.
func (r *Router) APIs() []jsonrpc.API {
	var apis []jsonrpc.API

	nonceLock := new(AddrLocker)

	// eth namespace (standard Ethereum methods)
	if r.enableEth {
		apis = append(apis,
			jsonrpc.API{
				Namespace: "eth",
				Service:   NewBlockChainAPI(r.api),
			},
			jsonrpc.API{
				Namespace: "eth",
				Service:   NewastAPI(r.api),
			},
			jsonrpc.API{
				Namespace: "eth",
				Service:   NewTransactionAPI(r.api, nonceLock),
			},
			jsonrpc.API{
				Namespace: "eth",
				Service:   filters.NewFilterAPI(r.api, 5*time.Minute),
			},
		)
	}

	// web3 namespace
	if r.enableWeb3 {
		apis = append(apis, jsonrpc.API{
			Namespace: "web3",
			Service:   &Web3API{r.api},
		})
	}

	// net namespace
	if r.enableNet {
		var chainID uint64
		if chainConfig := r.api.GetChainConfig(); chainConfig != nil && chainConfig.ChainID != nil {
			chainID = chainConfig.ChainID.Uint64()
		}
		apis = append(apis, jsonrpc.API{
			Namespace: "net",
			Service:   NewNetAPI(r.api, chainID),
		})
	}

	// debug namespace
	if r.enableDebug {
		apis = append(apis, jsonrpc.API{
			Namespace: "debug",
			Service:   NewDebugAPI(r.api),
		})
	}

	// txpool namespace
	if r.enableTxPool {
		apis = append(apis, jsonrpc.API{
			Namespace: "txpool",
			Service:   NewTxsPoolAPI(r.api),
		})
	}

	// admin namespace (node info)
	if r.enableAdmin {
		apis = append(apis, jsonrpc.API{
			Namespace: "admin",
			Service:   NewAdminAPI(r.api),
		})
	}

	// personal namespace (account management)
	if r.enablePersonal {
		apis = append(apis, jsonrpc.API{
			Namespace: "personal",
			Service:   NewPersonalAPI(r.api),
		})
	}

	// miner namespace (mining control)
	if r.enableMiner {
		apis = append(apis, jsonrpc.API{
			Namespace: "miner",
			Service:   NewMinerAPI(r.api),
		})
	}

	// rpc namespace (module info)
	if r.enableRPC {
		apis = append(apis, jsonrpc.API{
			Namespace: "rpc",
			Service:   NewRPCAPI(r.api),
		})
	}

	return apis
}

// Metrics returns the RPC metrics collector.
func (r *Router) Metrics() *RPCMetrics {
	return r.metrics
}

// StartMetricsLogger starts the periodic metrics logger.
func (r *Router) StartMetricsLogger(ctx context.Context, interval time.Duration) {
	utils.RunEvery(ctx, interval, func() {
		r.metrics.LogStats()
	})
}

// =============================================================================
// Namespace Registration Helpers
// =============================================================================

// NamespaceConfig is a helper to create a namespace registration.
type NamespaceConfig struct {
	Name    string
	Version string
	Service interface{}
	Public  bool
}

// ToJSONRPCAPI converts a NamespaceConfig to a jsonrpc.API.
func (nc *NamespaceConfig) ToJSONRPCAPI() jsonrpc.API {
	return jsonrpc.API{
		Namespace: nc.Name,
		Service:   nc.Service,
	}
}
