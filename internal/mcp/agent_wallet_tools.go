// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// AgentWalletProvider provides agent wallet operations for MCP tools.
type AgentWalletProvider interface {
	CreateAccount(ownerKey []byte, agentDID string) (string, error)
	GetBalance(address string) (string, error)
	AccountCount() int
}

// registerAgentWalletTools registers agent wallet tools with the MCP server.
func (s *Server) registerAgentWalletTools(provider AgentWalletProvider) {
	if provider == nil {
		return
	}

	s.RegisterTool(Tool{
		Name:        "createAgentWallet",
		Description: "Create an AI agent wallet with session key support and spending policies.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"ownerAddress": {"type": "string", "description": "Owner address (hex)"},
				"agentDID":     {"type": "string", "description": "Agent DID identifier (did:n42:...)"}
			},
			"required": ["ownerAddress", "agentDID"]
		}`),
		Handler: func(_ context.Context, params json.RawMessage) (interface{}, error) {
			var p struct {
				OwnerAddress string `json:"ownerAddress"`
				AgentDID     string `json:"agentDID"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}
			if p.OwnerAddress == "" || p.AgentDID == "" {
				return nil, fmt.Errorf("ownerAddress and agentDID are required")
			}
			addr, err := provider.CreateAccount([]byte(p.OwnerAddress), p.AgentDID)
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{
				"address":  addr,
				"agentDID": p.AgentDID,
				"status":   "created",
			}, nil
		},
	})

	s.RegisterTool(Tool{
		Name:        "getAgentBalance",
		Description: "Get the balance of an AI agent wallet.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"address": {"type": "string", "description": "Agent wallet address (hex)"}
			},
			"required": ["address"]
		}`),
		Handler: func(_ context.Context, params json.RawMessage) (interface{}, error) {
			var p struct {
				Address string `json:"address"`
			}
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, fmt.Errorf("invalid params: %w", err)
			}
			if p.Address == "" {
				return nil, fmt.Errorf("address is required")
			}
			balance, err := provider.GetBalance(p.Address)
			if err != nil {
				return nil, err
			}
			return map[string]interface{}{
				"address": p.Address,
				"balance": balance,
			}, nil
		},
	})

	// submitTransaction is a placeholder tool. It requires integration with a
	// transaction provider (e.g., txpool submission, session key validation,
	// spending policy enforcement) before it can actually submit transactions.
	// Until that integration is complete, it returns an error indicating the
	// feature is not yet active.
	s.RegisterTool(Tool{
		Name:        "submitTransaction",
		Description: "Submit a transaction through an AI agent wallet with session key authorization.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"from":     {"type": "string", "description": "Agent wallet address (hex)"},
				"to":       {"type": "string", "description": "Target contract address (hex)"},
				"value":    {"type": "string", "description": "Value in wei"},
				"data":     {"type": "string", "description": "Transaction data (hex)"},
				"sessionKeyID": {"type": "string", "description": "Session key ID for authorization (hex)"}
			},
			"required": ["from", "to"]
		}`),
		Handler: func(_ context.Context, params json.RawMessage) (interface{}, error) {
			// Placeholder: transaction submission is not yet implemented.
			// This requires provider integration (txpool, session key auth,
			// spending policy checks) before it can process real transactions.
			return nil, fmt.Errorf("submitTransaction is not yet active: transaction provider integration required")
		},
	})
}
