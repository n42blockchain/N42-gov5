// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// BridgeAPI exposes JSON-RPC methods for cross-chain bridge operations.
// Wraps the bridge.Router to surface transfer status queries and returns
// a structured BridgeStatusResult (status string + numeric code) suitable
// for light clients tracking pending deposits and withdrawals between
// N42 and external chains.

package api

import (
	"context"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/bridge"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// BridgeAPI provides JSON-RPC methods for cross-chain bridge operations.
type BridgeAPI struct {
	router bridge.Router
}

// NewBridgeAPI creates a new BridgeAPI.
func NewBridgeAPI(router bridge.Router) *BridgeAPI {
	return &BridgeAPI{router: router}
}

// BridgeStatusResult is the JSON-RPC response for a transfer status query.
type BridgeStatusResult struct {
	Status string `json:"status"`
	Code   uint8  `json:"code"`
}

// RouteInfoResult is the JSON-RPC response for route information.
type RouteInfoResult struct {
	Domain    uint32 `json:"domain"`
	RouteType string `json:"routeType"`
	Name      string `json:"name"`
}

// Send initiates a cross-chain transfer.
// Parameters: destChain (Hyperlane domain ID), recipient address, amount in wei.
func (b *BridgeAPI) Send(ctx context.Context, destChain uint32, recipient types.Address, amount *uint256.Int) (types.Hash, error) {
	if b.router == nil {
		return types.Hash{}, fmt.Errorf("bridge not configured")
	}
	if amount == nil || amount.IsZero() {
		return types.Hash{}, fmt.Errorf("amount must be positive")
	}
	return b.router.Send(destChain, recipient, amount)
}

// Status returns the current status of a cross-chain transfer.
func (b *BridgeAPI) Status(ctx context.Context, txHash types.Hash) (*BridgeStatusResult, error) {
	if b.router == nil {
		return nil, fmt.Errorf("bridge not configured")
	}
	status, err := b.router.Status(txHash)
	if err != nil {
		return nil, err
	}
	return &BridgeStatusResult{
		Status: bridgeStatusName(status),
		Code:   uint8(status),
	}, nil
}

// LatestVerifiedBlock returns the latest N42 block verified on a target chain.
func (b *BridgeAPI) LatestVerifiedBlock(ctx context.Context, destChain uint32) (uint64, error) {
	if b.router == nil {
		return 0, fmt.Errorf("bridge not configured")
	}
	return b.router.LatestVerifiedBlock(destChain)
}

// RouteInfo returns the routing configuration for a destination chain.
func (b *BridgeAPI) RouteInfo(ctx context.Context, destChain uint32) (*RouteInfoResult, error) {
	if b.router == nil {
		return nil, fmt.Errorf("bridge not configured")
	}
	zkRouter, ok := b.router.(*bridge.ZKRouter)
	if !ok {
		return nil, fmt.Errorf("router does not support route info")
	}
	route, found := zkRouter.RouteFor(destChain)
	if !found {
		return nil, fmt.Errorf("no route for chain %d", destChain)
	}
	routeType := "ZK"
	if route.RouteType == bridge.RouteHyperlane {
		routeType = "Hyperlane"
	}
	return &RouteInfoResult{
		Domain:    route.Domain,
		RouteType: routeType,
		Name:      route.Name,
	}, nil
}

// APIs returns the JSON-RPC API descriptors for the bridge namespace.
func (b *BridgeAPI) APIs() []jsonrpc.API {
	return []jsonrpc.API{
		{
			Namespace: "bridge",
			Service:   b,
		},
	}
}

func bridgeStatusName(s bridge.BridgeStatus) string {
	switch s {
	case bridge.StatusPending:
		return "pending"
	case bridge.StatusProving:
		return "proving"
	case bridge.StatusSubmitted:
		return "submitted"
	case bridge.StatusVerified:
		return "verified"
	case bridge.StatusCompleted:
		return "completed"
	case bridge.StatusFailed:
		return "failed"
	default:
		return "unknown"
	}
}
